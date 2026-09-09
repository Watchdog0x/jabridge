package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

func runUse(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		printUseUsage()
		return nil
	}
	if len(args) != 1 {
		return errors.New("usage: jabridge use usb|dongle")
	}
	connection := strings.ToLower(args[0])
	if connection != "usb" && connection != "dongle" {
		return errors.New("connection must be usb or dongle")
	}

	backend, err := connectTUIService()
	if err != nil {
		return err
	}
	defer backend.close()
	client := backend.clientSnapshot()
	var devices []ipc.DeviceInfo
	if err := ipcCall(client, "devices.list", nil, &devices); err != nil {
		return err
	}
	device, err := chooseConnectionDevice(devices, connection)
	if err != nil {
		return err
	}
	var response map[string]bool
	if err := ipcCall(client, "device.select", map[string]uint16{"id": device.ID}, &response); err != nil {
		return err
	}
	label := "direct USB"
	if connection == "dongle" {
		label = "the wireless dongle"
	}
	fmt.Printf("Using %s through %s. Matching PipeWire output and microphone are preferred when present.\n", device.Name, label)
	return nil
}

func chooseConnectionDevice(devices []ipc.DeviceInfo, connection string) (ipc.DeviceInfo, error) {
	var matches []ipc.DeviceInfo
	for _, device := range devices {
		if !device.IsDongle && strings.EqualFold(device.Connection, connection) {
			matches = append(matches, device)
		}
	}
	if len(matches) == 0 {
		return ipc.DeviceInfo{}, fmt.Errorf("no headset connected through %s", connection)
	}
	if len(matches) > 1 {
		return ipc.DeviceInfo{}, fmt.Errorf("more than one %s headset is connected; choose one in the TUI Switch device screen", connection)
	}
	return matches[0], nil
}

func printUseUsage() {
	fmt.Println(`Usage:
  jabridge use usb       use the headset's direct USB connection
  jabridge use dongle    use the headset through its wireless dongle

The selected control connection also becomes the preferred matching PipeWire
output and microphone.`)
}
