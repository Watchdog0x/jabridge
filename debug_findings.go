package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Watchdog0x/jabridge/internal/firmware"
)

var diagnoseFirmwareFile = firmware.DiagnoseFirmware

const nativeWordBits = strconv.IntSize

func parseCapabilityWord(value string) (uint64, error) {
	return strconv.ParseUint(value, 16, nativeWordBits)
}

func writeFirmwareDiagnostic(out *bytes.Buffer, pids []uint16) {
	fmt.Fprintln(out, "\nFirmware availability and cached-file checks:")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seen := map[uint16]bool{}
	for _, pid := range pids {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		if len(seen) > 8 || ctx.Err() != nil {
			fmt.Fprintln(out, "NOT TESTED: additional firmware targets exceed the metadata budget")
			continue
		}
		result, err := diagnoseFirmwareFile(ctx, pid, "firmware")
		fmt.Fprintf(out, "USB 0b0e:%04x\n", pid)
		if result.Latest.Version != "" {
			fmt.Fprintf(out, "  INFO latest published firmware: %s\n", safeFirmwareDiagnostic(result.Latest.Version))
		}
		if err != nil {
			fmt.Fprintf(out, "  UNAVAILABLE %s check: %s\n", result.Stage, diagnosticError(err))
			if result.Cached {
				fmt.Fprintln(out, "  ACTION: cached file did not pass checks. Use a fresh download directory and verify the package.")
			} else {
				fmt.Fprintln(out, "  ACTION: retry metadata lookup with internet access; this does not establish device incompatibility.")
			}
			continue
		}
		fmt.Fprintf(out, "  INFO firmware protocols: %v; official checksum published=%t\n", result.Protocols, result.ChecksumPublished)
		fmt.Fprintln(out, nativeFirmwareFinding(result))
		if !result.Cached {
			fmt.Fprintln(out, "  NOT TESTED download/file verification: latest package is not in ./firmware. Use jabridge firmware download to fetch it, then repeat debug.")
		} else if result.ChecksumMatches {
			fmt.Fprintf(out, "  PASS cached file checksum matches official metadata=%t\n", result.ChecksumMatches)
		} else {
			fmt.Fprintln(out, "  NOT TESTED cached file checksum: file exceeds diagnostic budget; verify it separately.")
		}
	}
	fmt.Fprintln(out, "No firmware package was downloaded or installed by this report.")
}

func nativeFirmwareFinding(result firmware.FirmwareDiagnostic) string {
	known := false
	for _, protocol := range result.Protocols {
		if protocol == 7 {
			known = true
		}
	}
	if len(result.Protocols) > 0 && !known {
		return "  NOT IMPLEMENTED native install: this firmware protocol needs its own transfer/recovery implementation."
	}
	if !result.Cached {
		return "  NOT TESTED native archive layout: package not available locally."
	}
	if !result.ChecksumMatches {
		return "  NOT TESTED native archive layout: cached file has not passed checksum verification."
	}
	if !result.NativeLayout {
		return "  NOT SUPPORTED native archive layout: file does not match the implemented partition format."
	}
	return "  PASS native archive layout; flashing and interrupted-update recovery are NOT TESTED."
}

func reportNextSteps(body string) []string {
	var steps []string
	add := func(value string) {
		for _, existing := range steps {
			if existing == value {
				return
			}
		}
		steps = append(steps, value)
	}
	if strings.Contains(body, "permission denied") {
		add("Device access is denied: run jabridge setup on the host, reconnect USB if asked, and repeat as the normal user.")
	}
	if strings.Contains(body, "ExecMainStatus=226") {
		add("The service failed during namespace setup: investigate the service sandbox and host namespace/AppArmor policy. Running the app as root is not the fix.")
	}
	if strings.Contains(body, "ExecMainStatus=203") {
		add("The service executable could not start: run jabridge setup on the host and inspect its executable access.")
	}
	if strings.Contains(body, "Service matches app version: false") {
		add("The service runs a different version: run jabridge service restart using the updated binary.")
	}
	if strings.Contains(body, "IPC: missing") || strings.Contains(body, "IPC: failed") || strings.Contains(body, "IPC: timed out") {
		add("The app cannot reach its service: check the service state and whether the app and service run on the same host/session.")
	}
	if strings.Contains(body, "GNP descriptor unsupported") {
		add("An accessible HID interface lacks the currently supported management report: extend transport support from the descriptor evidence; do not assume permissions are the cause.")
	}
	if strings.Contains(body, "reply timed out") || strings.Contains(body, "IDENT/") && strings.Contains(body, "timeout") {
		add("A known query received no matching reply: investigate interface, destination, packet framing and firmware behavior. This alone does not prove a udev problem.")
	}
	if strings.Contains(body, "response format/value rejected") {
		add("The native decoder rejected a reply: investigate the decoder/model mapping before treating the displayed value as valid.")
	}
	if strings.Contains(body, "NOT COVERED") {
		add("Catalog properties without diagnostic coverage are listed above; implement or map these individually.")
	}
	if strings.Contains(body, "NOT IMPLEMENTED native install") {
		add("This firmware family needs a native updater implementation; a matching download is not proof that flashing works.")
	}
	if len(steps) == 0 {
		add("No specific blocker was classified. Review NOT TESTED items and run the physical button, audio and reconnect checks before claiming full support.")
	}
	return steps
}

func writeEnvironmentDiagnostic(out *bytes.Buffer) {
	container := false
	for _, path := range []string{"/run/.containerenv", "/.dockerenv", "/run/host/container-manager"} {
		if _, err := os.Stat(path); err == nil {
			container = true
		}
	}
	fmt.Fprintf(out, "Container markers present: %t (a context clue, not a fault verdict)\n", container)
	if container {
		fmt.Fprintln(out, "ACTION: collect another report natively on the host to compare device access and the user-service session.")
	}
	fmt.Fprintln(out, "Connections: native control supports direct USB and headsets routed through Jabra Link. Direct system-Bluetooth/BlueZ control is NOT IMPLEMENTED.")
}

func inputCapabilityNames(data string) []string {
	// Linux sysfs prints native-word bitmaps most-significant word first.
	words := strings.Fields(data)
	var names []string
	for code, name := range mediaKeyNames {
		word := len(words) - 1 - int(code)/nativeWordBits
		if word < 0 {
			continue
		}
		value, err := parseCapabilityWord(words[word])
		if err == nil && value&(uint64(1)<<uint(code%nativeWordBits)) != 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func writeButtonCapabilities(out *bytes.Buffer) {
	fmt.Fprintln(out, "\nLinux button capabilities:")
	paths := jabraInputPaths()
	if len(paths) == 0 {
		fmt.Fprintln(out, "UNAVAILABLE: no Jabra Linux input nodes. Vendor-only controls may need HID event support.")
	}
	for _, path := range paths {
		key, err := os.ReadFile(filepath.Join("/sys/class/input", filepath.Base(path), "device/capabilities/key"))
		if err != nil {
			continue
		}
		fmt.Fprintf(out, "  %s advertised media/call controls: %s\n", filepath.Base(path), strings.Join(inputCapabilityNames(string(key)), ", "))
	}
	fmt.Fprintln(out, "Capability bits advertise events; use debug --buttons to record what the device actually emits.")
}
