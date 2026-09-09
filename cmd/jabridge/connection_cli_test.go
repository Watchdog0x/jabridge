package main

import (
	"testing"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

func TestChooseConnectionDeviceIgnoresDongleItself(t *testing.T) {
	devices := []ipc.DeviceInfo{
		{ID: 1, Name: "Link 380", IsDongle: true, Connection: "usb"},
		{ID: 2, Name: "Evolve2 65", Connection: "usb"},
		{ID: 3, Name: "Evolve2 65", Connection: "dongle"},
	}
	if device, err := chooseConnectionDevice(devices, "usb"); err != nil || device.ID != 2 {
		t.Fatalf("USB device = %#v, %v", device, err)
	}
	if device, err := chooseConnectionDevice(devices, "dongle"); err != nil || device.ID != 3 {
		t.Fatalf("dongle device = %#v, %v", device, err)
	}
}

func TestChooseConnectionDeviceRejectsAmbiguousRoute(t *testing.T) {
	devices := []ipc.DeviceInfo{
		{ID: 1, Name: "First", Connection: "usb"},
		{ID: 2, Name: "Second", Connection: "usb"},
	}
	if _, err := chooseConnectionDevice(devices, "usb"); err == nil {
		t.Fatal("ambiguous USB route was accepted")
	}
}
