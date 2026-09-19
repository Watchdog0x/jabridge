package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type usbAudioTopology struct {
	PID        uint16
	Port       string
	Hubs       []string
	Controller string
}

var usbAudioPortPattern = regexp.MustCompile(`^[0-9]+-[0-9]+(?:\.[0-9]+)*$`)
var stickyMixerPattern = regexp.MustCompile(`(?:^|\s)usb ([0-9]+-[0-9]+(?:\.[0-9]+)*):[^\n]*sticky mixer values[^\n]*, disabling`)

func readTopologyID(path string, bits int) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 32 {
		return 0, false
	}
	value, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(string(data)), "0x"), 16, bits)
	return value, err == nil
}

// Export only numeric USB/PCI IDs and port topology. USB serials, manufacturer
// text, device names and full sysfs paths are not included in the report.
func readUSBAudioTopology(sysRoot string) ([]usbAudioTopology, error) {
	entries, err := os.ReadDir(filepath.Join(sysRoot, "bus/usb/devices"))
	if err != nil {
		return nil, err
	}
	var rows []usbAudioTopology
	deviceRoot := filepath.Join(sysRoot, "devices") + string(filepath.Separator)
	for _, entry := range entries {
		if !usbAudioPortPattern.MatchString(entry.Name()) {
			continue
		}
		path := filepath.Join(sysRoot, "bus/usb/devices", entry.Name())
		vendor, ok := readTopologyID(filepath.Join(path, "idVendor"), 16)
		if !ok || vendor != 0x0b0e {
			continue
		}
		pid, ok := readTopologyID(filepath.Join(path, "idProduct"), 16)
		if !ok {
			continue
		}
		row := usbAudioTopology{PID: uint16(pid), Port: entry.Name(), Controller: "unavailable"}
		resolved, e := filepath.EvalSymlinks(path)
		if e == nil && strings.HasPrefix(resolved, deviceRoot) {
			parent := filepath.Dir(resolved)
			for depth := 0; depth < 24 && strings.HasPrefix(parent, deviceRoot); depth++ {
				if class, ok := readTopologyID(filepath.Join(parent, "bDeviceClass"), 8); ok && class == 9 && usbAudioPortPattern.MatchString(filepath.Base(parent)) {
					v, vOK := readTopologyID(filepath.Join(parent, "idVendor"), 16)
					p, pOK := readTopologyID(filepath.Join(parent, "idProduct"), 16)
					if vOK && pOK {
						row.Hubs = append(row.Hubs, fmt.Sprintf("%04x:%04x", v, p))
					}
				}
				if class, ok := readTopologyID(filepath.Join(parent, "class"), 24); ok && class>>8 == 0x0c03 {
					v, vOK := readTopologyID(filepath.Join(parent, "vendor"), 16)
					p, pOK := readTopologyID(filepath.Join(parent, "device"), 16)
					if vOK && pOK {
						row.Controller = fmt.Sprintf("PCI %04x:%04x", v, p)
					}
					if class&0xff == 0x30 {
						row.Controller += " (xHCI)"
					}
					break
				}
				parent = filepath.Dir(parent)
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func writeUSBAudioTopology(out *bytes.Buffer, rows []usbAudioTopology, err error) {
	fmt.Fprintln(out, "\nUSB audio connection path:")
	if err != nil {
		fmt.Fprintln(out, "  USB topology unavailable.")
		return
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "  No attached Jabra USB device found.")
		return
	}
	for _, row := range rows {
		hubs := "no intermediate USB hub found"
		if len(row.Hubs) > 0 {
			hubs = "hubs from headset toward computer: " + strings.Join(row.Hubs, " -> ")
		}
		fmt.Fprintf(out, "  Jabra %04x on USB port %s; %s; controller %s\n", row.PID, row.Port, hubs, row.Controller)
	}
	fmt.Fprintln(out, "  Connection layout is diagnostic information, not proof that a hub or controller caused a fault.")
}

func writeStickyMixerDiagnostic(out *bytes.Buffer, rows []usbAudioTopology, data []byte, err error) {
	if err != nil {
		fmt.Fprintln(out, "Kernel USB mixer warnings: unavailable (kernel journal access may be restricted).")
		return
	}
	counts := map[string]int{}
	for _, match := range stickyMixerPattern.FindAllSubmatch(data, -1) {
		counts[string(match[1])]++
	}
	found := false
	for _, row := range rows {
		if count := counts[row.Port]; count > 0 {
			found = true
			fmt.Fprintf(out, "Kernel USB mixer warning on port %s: %d sticky-mixer disable message(s) in this boot's recent kernel log.\n", row.Port, count)
		}
	}
	if found {
		fmt.Fprintln(out, "  These messages may predate the current USB attachment. A stale volume read can make the kernel disable a working mixer; this does not identify every cause of silent playback.")
	} else {
		fmt.Fprintln(out, "Kernel USB mixer warnings: none found for these ports in the last 3000 kernel log entries; older warnings may be absent.")
	}
}

type boundedKernelLog struct {
	data      []byte
	truncated bool
}

func (b *boundedKernelLog) Write(p []byte) (int, error) {
	const limit = 2 << 20
	if len(p) > limit-len(b.data) {
		b.truncated = true
	}
	if remaining := limit - len(b.data); remaining > 0 {
		b.data = append(b.data, p[:min(len(p), remaining)]...)
	}
	return len(p), nil
}

func writeUSBAudioDiagnostic(out *bytes.Buffer) {
	rows, err := readUSBAudioTopology("/sys")
	writeUSBAudioTopology(out, rows, err)
	if err != nil || len(rows) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "journalctl", "-k", "-b", "-n", "3000", "-o", "cat", "--no-pager")
	var data boundedKernelLog
	command.Stdout = &data
	err = command.Run()
	writeStickyMixerDiagnostic(out, rows, data.data, err)
	if data.truncated {
		fmt.Fprintln(out, "  Kernel log scan reached its size limit; some messages were omitted.")
	}
}
