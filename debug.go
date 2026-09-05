package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
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
	"github.com/Watchdog0x/jabridge/internal/history"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Debug reports are assembled from selected fields, never raw logs or packets.
func runDebug(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Println("Usage: jabridge debug [--buttons | --guided] [--output FILE]\nCollects device/report layouts, model settings/events/commands, native reads and service history.\nInteractive terminals include a 20-second passive observation; --buttons=false skips it.\n--guided lets you choose the physical controls to test, including no buttons/wheel.\nNormal device controls still act. Private identities, typed text and raw device payloads are omitted.")
		return nil
	}
	var output io.Writer = os.Stdout
	flags := flag.NewFlagSet("debug", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("output", "", "save report")
	buttons := flags.Bool("buttons", term.IsTerminal(int(os.Stdin.Fd())), "observe button events")
	guided := flags.Bool("guided", false, "guided button mapping")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: jabridge debug [--buttons] [--output FILE]")
	}
	steps := []buttonStep{{"free", "For 20 seconds, use any physical controls you want to test; if there are none, leave the device idle", 20 * time.Second}}
	if *guided {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("guided selection needs a terminal; use --buttons for a timed observation")
		}
		var err error
		steps, err = selectGuidedControls(os.Stdin, os.Stderr)
		if err != nil {
			return err
		}
	}
	if *path != "" {
		file, err := os.OpenFile(*path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("create debug report without overwrite: %w", err)
		}
		defer func() { _ = file.Close() }()
		output = file
	}
	fmt.Fprintln(os.Stderr, "Checking device access, native reads and firmware. This may take a few minutes...")
	var report bytes.Buffer
	if err := writeDebugReport(&report); err != nil {
		return err
	}
	if *buttons || *guided {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		observationErr := writeButtonObservation(ctx, &report, os.Stderr, steps)
		stop()
		if observationErr != nil {
			return observationErr
		}
	}
	n, err := output.Write(report.Bytes())
	if err != nil {
		return err
	}
	if n != report.Len() {
		return io.ErrShortWrite
	}
	if *path != "" {
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
	writeEnvironmentDiagnostic(out)
	fmt.Fprintf(out, "Current device access rule installed: %t\n", deviceAccessRuleInstalled())
	fmt.Fprintf(out, "Custom IPC socket configured: %t\n", os.Getenv("JABRIDGE_SOCKET") != "")
	devices, err := enumerateJabraUSB()
	if err != nil {
		fmt.Fprintf(out, "USB enumeration: %s\n", diagnosticError(err))
	}
	nodes := diagnosticHIDNodes()
	fmt.Fprintf(out, "\nDetected Jabra USB devices: %d; Jabra HID nodes: %d\n", len(devices), len(nodes))
	for _, node := range nodes {
		path := node.Path
		fmt.Fprintln(out, "\nHID source:", node.label())
		fmt.Fprintln(out, describeHIDAccess(path))
		if fingerprint, err := firmware.HIDDescriptorFingerprint(path); err == nil {
			fmt.Fprintln(out, "    descriptor-sha256:", fingerprint)
		}
		reports, inspectErr := firmware.InspectHIDReports(path)
		if inspectErr == nil {
			for _, report := range reports {
				fmt.Fprintf(out, "    report %d %s: %d bytes\n", report.ID, report.Kind, report.Bytes)
				for _, field := range report.Fields {
					fmt.Fprintf(out, "      field bit=%d size=%d count=%d page=%04x usages=%x range=%x..%x logical=%d..%d flags=%x\n", field.OffsetBits, field.SizeBits, field.Count, field.UsagePage, field.Usages, field.UsageMin, field.UsageMax, field.LogicalMin, field.LogicalMax, field.Flags)
				}
			}
			for _, candidate := range vendorControlCandidates(reports) {
				fmt.Fprintln(out, "    candidate control transport:", candidate)
			}
		} else {
			fmt.Fprintln(out, "    Descriptor parsing:", diagnosticError(inspectErr))
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
	writeRecentServiceFailures(out)
	writeHistoryReport(out)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := ipc.Dial(ctx, ipcSocketPath())
	if err == nil {
		defer func() { _ = client.Close() }()
		err = client.Ping(ctx)
	}
	fmt.Fprintf(out, "IPC: %s\n", diagnosticError(err))
	var pids []uint16
	for _, device := range devices {
		pids = append(pids, device.productID)
	}
	for _, node := range nodes {
		pids = append(pids, node.PID)
	}
	if err == nil {
		pids = append(pids, writeNativeDiagnostic(out, client)...)
	} else {
		fmt.Fprintln(out, "Native device tests: BLOCKED (service unavailable). Run setup and repeat; access checks above still apply.")
	}
	writeProfileEvidence(out, pids)
	writeAudioDiagnostic(out)
	writeButtonCapabilities(out)
	writeFirmwareDiagnostic(out, pids)
	steps := reportNextSteps(report.String())
	fmt.Fprintln(out, "\nWhat to investigate next:")
	for index, step := range steps {
		fmt.Fprintf(out, "%d. %s\n", index+1, step)
	}
	fmt.Fprintln(out, "\nManual checks still needed: button/wheel events, audible output, microphone recording, meeting-app controls, reconnect/power cycles, setting writes and firmware recovery.")
	fmt.Fprintln(out, "Run jabridge buttons for the button check. Report PASS/FAIL/NOT TESTED for the manual checks.")
	fmt.Fprintln(out, "No settings or firmware were changed. No service was started or stopped. Native reads, when available, were performed by the service.")
	n, err := destination.Write(report.Bytes())
	if err == nil && n != report.Len() {
		return io.ErrShortWrite
	}
	return err
}

func vendorControlCandidates(reports []firmware.HIDReport) []string {
	type pair struct {
		input, output int
		pages         map[uint32]bool
	}
	pairs := map[byte]*pair{}
	for _, report := range reports {
		if report.Kind != "input" && report.Kind != "output" {
			continue
		}
		vendor := false
		for _, field := range report.Fields {
			if field.UsagePage >= 0xff00 {
				vendor = true
				break
			}
		}
		if !vendor {
			continue
		}
		entry := pairs[report.ID]
		if entry == nil {
			entry = &pair{pages: map[uint32]bool{}}
			pairs[report.ID] = entry
		}
		if report.Kind == "input" {
			entry.input = report.Bytes
		} else {
			entry.output = report.Bytes
		}
		for _, field := range report.Fields {
			if field.UsagePage >= 0xff00 {
				entry.pages[field.UsagePage] = true
			}
		}
	}
	var ids []int
	for id, entry := range pairs {
		if entry.input > 0 && entry.output > 0 {
			ids = append(ids, int(id))
		}
	}
	sort.Ints(ids)
	var result []string
	for _, id := range ids {
		entry := pairs[byte(id)]
		var pages []int
		for page := range entry.pages {
			pages = append(pages, int(page))
		}
		sort.Ints(pages)
		var labels []string
		for _, page := range pages {
			labels = append(labels, fmt.Sprintf("%04x", page))
		}
		status := "framing unknown; no probe sent"
		if id == 5 && (entry.input == 63 || entry.input == 64) && (entry.output == 63 || entry.output == 64) && entry.pages[0xff00] {
			status = "GNP layout recognized; see native service read results"
		}
		result = append(result, fmt.Sprintf("report %d vendor input=%d bytes output=%d bytes pages=%s (%s)", id, entry.input, entry.output, strings.Join(labels, ","), status))
	}
	return result
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

func writeNativeDiagnostic(out *bytes.Buffer, client *ipc.Client) (pids []uint16) {
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
	var historyStatus history.RecordingStatus
	if err := client.Call(ctx, "history.status", nil, &historyStatus); err == nil {
		fmt.Fprintf(out, "Service history: enabled=%t missed=%d error=%s\n", historyStatus.Enabled, historyStatus.Missed, historyStatus.Error)
		if historyStatus.Error == "read-only-filesystem" {
			fmt.Fprintln(out, "Run jabridge service restart to install the updated history-capable service unit.")
		}
	}
	if err := client.Subscribe(ctx); err != nil {
		fmt.Fprintln(out, "IPC event subscription: BLOCKED:", ipcDiagnosticFailure(err))
	} else {
		fmt.Fprintln(out, "IPC event subscription: PASS registration; event delivery requires actual device changes")
	}
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
		pids = append(pids, device.PID)
		fmt.Fprintf(out, "\nNative device %d USB 0b0e:%04x selected=%t dongle=%t\n", device.ID, device.PID, device.Selected, device.IsDongle)
		connection := "direct USB"
		if device.Connection == "dongle" {
			connection = fmt.Sprintf("through dongle device %d", device.ParentID)
		}
		fmt.Fprintln(out, "Connection:", connection)
		fmt.Fprintln(out, "Variant reported by service:", diagnosticDeviceVariant(device.Variant))
		if index >= 8 || ctx.Err() != nil {
			fmt.Fprintln(out, "NOT TESTED: diagnostic device/time budget reached")
			continue
		}
		var checks []ipc.DiagnosticCheck
		if err := client.Call(ctx, "diagnostics.device", map[string]uint16{"id": device.ID}, &checks); err != nil {
			fmt.Fprintln(out, "BLOCKED:", ipcDiagnosticFailure(err))
			continue
		}
		for _, check := range checks {
			fmt.Fprintf(out, "  %-12s %s: %s\n", check.State, check.Feature, check.Detail)
		}
	}
	events := map[string]int{}
drainEvents:
	for count := 0; count < 64; count++ {
		select {
		case event, open := <-client.Notifications():
			if !open {
				break drainEvents
			}
			switch event.Method {
			case "device.attached", "device.detached", "device.battery.update", "device.pairing.update":
				events[event.Method]++
			}
		default:
			break drainEvents
		}
	}
	if len(events) == 0 {
		fmt.Fprintln(out, "IPC event delivery: NOT TESTED (no device-change event observed)")
	} else {
		fmt.Fprintf(out, "IPC observed event counts: %v\n", events)
	}
	return pids
}

func diagnosticDeviceVariant(value string) string {
	if value == "" || len(value) > 32 {
		return "unavailable"
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') && char != '-' {
			return "unrecognized format"
		}
	}
	return value
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
