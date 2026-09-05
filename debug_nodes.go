package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type diagnosticHIDNode struct {
	Path     string
	Bus, PID uint16
}

func diagnosticHIDNodes() []diagnosticHIDNode {
	entries, _ := os.ReadDir("/sys/class/hidraw")
	var nodes []diagnosticHIDNode
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join("/sys/class/hidraw", entry.Name(), "device", "uevent"))
		if err != nil {
			continue
		}
		bus, pid, ok := diagnosticHIDIdentity(string(data))
		if ok {
			nodes = append(nodes, diagnosticHIDNode{Path: filepath.Join("/dev", entry.Name()), Bus: bus, PID: pid})
		}
	}
	return nodes
}

func diagnosticHIDIdentity(data string) (bus, pid uint16, ok bool) {
	for _, line := range strings.Split(data, "\n") {
		id, exists := strings.CutPrefix(line, "HID_ID=")
		if !exists {
			continue
		}
		parts := strings.Split(id, ":")
		if len(parts) != 3 {
			return 0, 0, false
		}
		var values [3]uint16
		for index, part := range parts {
			value, err := strconv.ParseUint(part, 16, 16)
			if err != nil {
				return 0, 0, false
			}
			values[index] = uint16(value)
		}
		return values[0], values[2], values[1] == 0x0b0e
	}
	return 0, 0, false
}

func diagnosticBus(bus uint16) string {
	switch bus {
	case 3:
		return "usb"
	case 5:
		return "system-bluetooth"
	default:
		return fmt.Sprintf("hid-bus-%04x", bus)
	}
}

func (node diagnosticHIDNode) label() string {
	return fmt.Sprintf("%s model=0b0e:%04x connection=%s", filepath.Base(node.Path), node.PID, diagnosticBus(node.Bus))
}

func diagnosticInputLabel(path string) string {
	name := filepath.Base(path)
	values := map[string]uint64{}
	for _, key := range []string{"bustype", "vendor", "product"} {
		data, err := os.ReadFile(filepath.Join("/sys/class/input", name, "device/id", key))
		if err != nil {
			return name
		}
		value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 16, 16)
		if err != nil {
			return name
		}
		values[key] = value
	}
	return fmt.Sprintf("%s model=%04x:%04x connection=%s", name, values["vendor"], values["product"], diagnosticBus(uint16(values["bustype"])))
}
