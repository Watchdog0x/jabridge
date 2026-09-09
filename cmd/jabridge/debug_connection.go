package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

// Inventory uses sysfs identities, not an allowlist of supported products. A
// device can be visible to Linux even when the service cannot manage it yet.
// Keep failed scans distinct from successful scans that found nothing.
type diagnosticInventory struct {
	USBPIDs                        []uint16
	HID                            []diagnosticHIDNode
	Input                          []string
	USBError, HIDError, InputError error
}

const (
	connectionNone       = "Connection check: no Jabra USB, HID or input device is visible in this environment."
	connectionIncomplete = "Connection check: device scan incomplete; missing entries do not prove a missing device."
	connectionNoHID      = "Connection check: USB device found, but no Jabra HID interface is visible."
)

func readDiagnosticInventory(root fs.FS) diagnosticInventory {
	var result diagnosticInventory
	readEntries := func(dir string, failure *error) []fs.DirEntry {
		entries, err := fs.ReadDir(root, dir)
		if err != nil {
			*failure = err
		}
		return entries
	}
	read := func(name string, failure *error) string {
		data, err := fs.ReadFile(root, name)
		if err != nil && *failure == nil {
			*failure = err
		}
		return strings.TrimSpace(string(data))
	}
	for _, entry := range readEntries("sys/bus/usb/devices", &result.USBError) {
		// Interface entries do not have idVendor/idProduct; their parent does.
		if strings.Contains(entry.Name(), ":") {
			continue
		}
		dir := path.Join("sys/bus/usb/devices", entry.Name())
		if !strings.EqualFold(read(path.Join(dir, "idVendor"), &result.USBError), "0b0e") {
			continue
		}
		pid, err := strconv.ParseUint(read(path.Join(dir, "idProduct"), &result.USBError), 16, 16)
		if err != nil {
			pid = 0 // Preserve the device count without looking up an invalid PID.
			if result.USBError == nil {
				result.USBError = err
			}
		}
		result.USBPIDs = append(result.USBPIDs, uint16(pid))
	}
	for _, entry := range readEntries("sys/class/hidraw", &result.HIDError) {
		data := read(path.Join("sys/class/hidraw", entry.Name(), "device/uevent"), &result.HIDError)
		if bus, pid, ok := diagnosticHIDIdentity(data); ok {
			result.HID = append(result.HID, diagnosticHIDNode{Path: path.Join("/dev", entry.Name()), Bus: bus, PID: pid})
		}
	}
	for _, entry := range readEntries("sys/class/input", &result.InputError) {
		if !strings.HasPrefix(entry.Name(), "event") {
			continue
		}
		vendor := read(path.Join("sys/class/input", entry.Name(), "device/id/vendor"), &result.InputError)
		if strings.EqualFold(vendor, "0b0e") {
			result.Input = append(result.Input, path.Join("/dev/input", entry.Name()))
		}
	}
	return result
}

func writeConnectionDiagnostic(out *bytes.Buffer, inventory diagnosticInventory) {
	fmt.Fprintf(out, "\nDetected Jabra USB devices: %d; Jabra HID nodes: %d\n", len(inventory.USBPIDs), len(inventory.HID))
	fmt.Fprintf(out, "Detected Jabra Linux input nodes: %d\n", len(inventory.Input))
	for _, scan := range []struct {
		name string
		err  error
	}{{"USB", inventory.USBError}, {"HID", inventory.HIDError}, {"input", inventory.InputError}} {
		if scan.err != nil {
			// A sysfs scan error is not a failed /dev open. Do not prescribe udev
			// setup or export arbitrary paths from the underlying error.
			category := diagnosticError(scan.err)
			category, _, _ = strings.Cut(category, " (")
			fmt.Fprintf(out, "%s inventory scan: incomplete (%s)\n", scan.name, category)
		}
	}
	switch {
	case inventory.USBError != nil || inventory.HIDError != nil || inventory.InputError != nil:
		fmt.Fprintln(out, connectionIncomplete)
	case len(inventory.USBPIDs)+len(inventory.HID)+len(inventory.Input) == 0:
		fmt.Fprintln(out, connectionNone)
		fmt.Fprintln(out, "This does not show a device permission error. Check the USB connection before changing permissions.")
	case len(inventory.USBPIDs) > 0 && len(inventory.HID) == 0:
		fmt.Fprintln(out, connectionNoHID)
	default:
		fmt.Fprintln(out, "Connection check: Jabra device entries found; actual access and service reads are checked separately below.")
	}
}
