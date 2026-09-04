package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/daemon/pipewire"
	"github.com/Watchdog0x/jabridge/internal/buildinfo"
	"github.com/Watchdog0x/jabridge/internal/firmware"
	"golang.org/x/sys/unix"
)

// Debug reports are assembled from selected fields, never raw logs or packets.
func runDebug(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Println("Usage: jabridge debug [--output FILE]\nChecks HID access and service health without changing devices.\nThe report omits serial numbers, Bluetooth addresses, usernames and raw logs.")
		return nil
	}
	var output io.Writer = os.Stdout
	if len(args) != 0 {
		if len(args) != 2 || args[0] != "--output" {
			return errors.New("usage: jabridge debug [--output FILE]")
		}
		file, err := os.OpenFile(args[1], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("create debug report without overwrite: %w", err)
		}
		defer func() { _ = file.Close() }()
		output = file
	}
	fmt.Fprintln(os.Stderr, "Checking device access and native reads. This can take up to about two minutes...")
	if err := writeDebugReport(output); err != nil {
		return err
	}
	if len(args) == 2 {
		fmt.Fprintln(os.Stderr, "Debug report saved. Attach the report file to your issue.")
	}
	return nil
}

func diagnosticError(err error) string {
	switch {
	case err == nil:
		return "ready"
	case errors.Is(err, os.ErrPermission), errors.Is(err, syscall.EPERM):
		return "permission denied (run jabridge setup, then reconnect USB if asked)"
	case errors.Is(err, os.ErrNotExist):
		return "missing"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, syscall.EBUSY):
		return "device busy"
	case errors.Is(err, syscall.ENODEV):
		return "device disconnected"
	default:
		// Do not emit arbitrary OS paths or device data in a shareable report.
		return "failed"
	}
}

func writeDebugReport(destination io.Writer) error {
	var report bytes.Buffer
	out := &report
	fmt.Fprintf(out, "Jabridge debug %s\nPlatform: %s/%s\n", buildinfo.Version, runtime.GOOS, runtime.GOARCH)
	writeSystemDiagnostic(out)
	fmt.Fprintf(out, "Device access rule installed: %t\n", deviceAccessRuleInstalled())
	fmt.Fprintf(out, "Custom IPC socket configured: %t\n", os.Getenv("JABRIDGE_SOCKET") != "")
	devices, err := enumerateJabraUSB()
	if err != nil {
		fmt.Fprintf(out, "USB enumeration: %s\n", diagnosticError(err))
	}
	for _, device := range devices {
		fmt.Fprintf(out, "\nUSB %04x:%04x\n", device.vendorID, device.productID)
		paths := findHidrawPathsForPID(device.vendorID, device.productID)
		fmt.Fprintf(out, "HID interfaces: %d\n", len(paths))
		for _, path := range paths {
			fmt.Fprintln(out, describeHIDAccess(path))
			reports, inspectErr := firmware.InspectHIDReports(path)
			if inspectErr == nil {
				for _, report := range reports {
					fmt.Fprintf(out, "    report %d %s: %d bytes\n", report.ID, report.Kind, report.Bytes)
				}
			}
		}
	}
	fmt.Fprintln(out, "\nService:")
	for _, path := range jabraInputPaths() {
		file, openErr := os.Open(path)
		if openErr == nil {
			_ = file.Close()
		}
		fmt.Fprintf(out, "Jabra input %s: %s\n", filepath.Base(path), diagnosticError(openErr))
	}
	fmt.Fprintln(out, serviceDiagnosticSummary())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := ipc.Dial(ctx, ipcSocketPath())
	if err == nil {
		defer func() { _ = client.Close() }()
		err = client.Ping(ctx)
	}
	fmt.Fprintf(out, "IPC: %s\n", diagnosticError(err))
	if err == nil {
		writeNativeDiagnostic(out, client)
	} else {
		fmt.Fprintln(out, "Native device tests: BLOCKED (service unavailable). Run setup and repeat; access checks above still apply.")
		for _, device := range devices {
			catalogContext, stop := context.WithTimeout(context.Background(), 6*time.Second)
			capabilities, catalogErr := deviceModelClient.Lookup(catalogContext, device.productID, "", "")
			stop()
			if catalogErr != nil {
				fmt.Fprintf(out, "Catalog %04x:%04x: UNAVAILABLE (network/model/variant lookup); hardware support undetermined\n", device.vendorID, device.productID)
				continue
			}
			fmt.Fprintf(out, "Catalog %04x:%04x: INFO profile %s, %d properties; no device reads were performed\n", device.vendorID, device.productID, safeFirmwareDiagnostic(capabilities.Firmware), len(capabilities.Properties))
		}
	}
	writeAudioDiagnostic(out)
	fmt.Fprintln(out, "\nManual checks still needed: button/wheel events, audible output, microphone recording, meeting-app controls, reconnect/power cycles, setting writes and firmware recovery.")
	fmt.Fprintln(out, "Run jabridge buttons for the button check. Report PASS/FAIL/NOT TESTED for the manual checks.")
	fmt.Fprintln(out, "No settings or firmware were changed. No service was started or stopped. Native reads, when available, were performed by the service.")
	n, err := destination.Write(report.Bytes())
	if err == nil && n != report.Len() {
		return io.ErrShortWrite
	}
	return err
}

func writeSystemDiagnostic(out *bytes.Buffer) {
	var system unix.Utsname
	if unix.Uname(&system) == nil {
		fmt.Fprintf(out, "Kernel: %s\n", unix.ByteSliceToString(system.Release[:]))
	}
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok || (key != "ID" && key != "VERSION_ID") {
				continue
			}
			value = strings.Trim(value, "\"'")
			if strings.ContainsAny(value, " /\\\t\r\x1b") {
				continue
			}
			fmt.Fprintf(out, "OS %s: %s\n", key, value)
		}
	}
	for _, name := range []string{"unprivileged_userns_clone", "apparmor_restrict_unprivileged_userns"} {
		if data, err := os.ReadFile(filepath.Join("/proc/sys/kernel", name)); err == nil {
			value := strings.TrimSpace(string(data))
			if value == "0" || value == "1" {
				fmt.Fprintf(out, "Kernel %s: %s\n", name, value)
			}
		}
	}
}

func writeNativeDiagnostic(out *bytes.Buffer, client *ipc.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var version struct {
		Version string `json:"version"`
	}
	if err := client.Call(ctx, "version", nil, &version); err != nil {
		fmt.Fprintln(out, "Native tests: BLOCKED (cannot read service version)")
		return
	}
	fmt.Fprintf(out, "Service matches app version: %t\n", version.Version == buildinfo.Version)
	var devices []ipc.DeviceInfo
	if err := client.Call(ctx, "devices.list", nil, &devices); err != nil {
		fmt.Fprintln(out, "Native tests: BLOCKED (cannot read service inventory)")
		return
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
	if len(devices) == 0 {
		fmt.Fprintln(out, "Native tests: NOT TESTED (service inventory is empty; scan may still be starting)")
	}
	for index, device := range devices {
		fmt.Fprintf(out, "\nNative device %d USB 0b0e:%04x selected=%t dongle=%t\n", device.ID, device.PID, device.Selected, device.IsDongle)
		connection := "direct USB"
		if device.Connection == "dongle" {
			connection = fmt.Sprintf("through dongle device %d", device.ParentID)
		}
		fmt.Fprintln(out, "Connection:", connection)
		if index >= 8 || ctx.Err() != nil {
			fmt.Fprintln(out, "NOT TESTED: diagnostic device/time budget reached")
			continue
		}
		var checks []ipc.DiagnosticCheck
		if err := client.Call(ctx, "diagnostics.device", map[string]uint16{"id": device.ID}, &checks); err != nil {
			fmt.Fprintln(out, "BLOCKED: native diagnostic unavailable or timed out. Update the app, run jabridge service restart, then repeat.")
			continue
		}
		for _, check := range checks {
			fmt.Fprintf(out, "  %-12s %s: %s\n", check.State, check.Feature, check.Detail)
		}
	}
}

func writeAudioDiagnostic(out *bytes.Buffer) {
	fmt.Fprintln(out, "\nPipeWire:")
	snapshot, err := pipewire.TakeSnapshot()
	if err != nil {
		fmt.Fprintln(out, "UNAVAILABLE: cannot read audio graph; check PipeWire and pw-dump")
		return
	}
	fmt.Fprintf(out, "INFO: %d Jabra outputs, %d Jabra microphones\n", len(snapshot.JabraSinkNodes()), len(snapshot.JabraSourceNodes()))
	fmt.Fprintf(out, "INFO: communication stream detected on a Jabra microphone=%t\n", pipewire.DetectCall(snapshot).InCall)
	fmt.Fprintln(out, "NOT TESTED: audible playback, microphone quality and button-to-call integration.")
}

func describeHIDAccess(path string) string {
	label := filepath.Base(path)
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Sprintf("  %s: %s", label, diagnosticError(err))
	}
	_ = file.Close()
	size, err := firmware.GnpOutputReportSize(path)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Sprintf("  %s: descriptor access denied", label)
		}
		return fmt.Sprintf("  %s: read/write access ready; GNP descriptor unsupported or unreadable", label)
	}
	return fmt.Sprintf("  %s: read/write access ready; GNP output report: %d bytes", label, size)
}

func serviceDiagnosticSummary() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "systemctl", "--user", "show", "jabridge.service",
		"--property=LoadState,ActiveState,SubState,Result,ExecMainCode,ExecMainStatus", "--no-pager").Output()
	if err != nil {
		return "User service manager: unavailable; check jabridge service status"
	}
	return formatServiceDiagnostic(data)
}

func formatServiceDiagnostic(data []byte) string {
	allowed := map[string]bool{"LoadState": true, "ActiveState": true, "SubState": true, "Result": true, "ExecMainCode": true, "ExecMainStatus": true}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || !allowed[key] || strings.ContainsAny(value, " /\\\t\r\x1b") {
			continue
		}
		lines = append(lines, key+"="+value)
		if key == "ExecMainStatus" {
			switch value {
			case "226":
				lines = append(lines, "Service sandbox could not start (namespace setup failed).")
			case "203":
				lines = append(lines, "Service executable could not start. Run jabridge setup again.")
			}
		}
	}
	return strings.Join(lines, "\n")
}
