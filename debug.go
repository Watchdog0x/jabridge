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
	"strings"
	"syscall"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/buildinfo"
	"github.com/Watchdog0x/jabridge/internal/firmware"
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
	return writeDebugReport(output)
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
	fmt.Fprintln(out, "No device commands were sent. No service was started or stopped.")
	n, err := destination.Write(report.Bytes())
	if err == nil && n != report.Len() {
		return io.ErrShortWrite
	}
	return err
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
