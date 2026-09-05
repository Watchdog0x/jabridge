package main

import "strings"

func deviceKindLabel(device *jabra_DeviceInfo) string {
	if device == nil {
		return "Device"
	}
	if device.isDongle {
		return "Dongle"
	}
	name := strings.ToLower(device.deviceName)
	switch {
	case strings.Contains(name, "panacast"):
		return "Camera/room device"
	case strings.Contains(name, "scheduler"):
		return "Room scheduler"
	case strings.Contains(name, "control ip"):
		return "Room controller"
	case strings.Contains(name, "speak"), strings.Contains(name, "connect 4s"):
		return "Speakerphone"
	case strings.Contains(name, "link"):
		return "Controller/adapter"
	case strings.Contains(name, "evolve"), strings.Contains(name, "engage"),
		strings.Contains(name, "biz"), strings.Contains(name, "pro "),
		strings.Contains(name, "perform"), strings.Contains(name, "uc voice"):
		return "Headset"
	default:
		return "Device"
	}
}
