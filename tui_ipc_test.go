package main

import (
	"testing"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

func TestReplaceTUIDeviceStateMirrorsService(t *testing.T) {
	withDeviceState(t, devices{}, -1, -1)
	replaceTUIDeviceState(
		[]ipc.DeviceInfo{
			{ID: 0, Name: "Link 380", PID: 0x24c7, IsDongle: true, Connection: "usb", Firmware: "1.16.0"},
			{ID: 1, Name: "Evolve2 85", PID: 0x24b9, Connection: "dongle", ParentID: 0,
				Battery: &ipc.BatteryInfo{Level: 48, Component: int(batteryHeadband)}},
		},
		[]ipc.PairedDeviceInfo{{ID: 7, Name: "Evolve2 85", Connected: true}},
		ipc.FeatureInfo{PairingList: true, BusyLight: true},
	)

	dongle, exists := selectedDongleSnapshot()
	if !exists || dongle.deviceName != "Link 380" || dongle.firmwareVersion != "1.16.0" {
		t.Fatalf("mirrored dongle = %#v, exists=%v", dongle, exists)
	}
	if dongle.hidrawPath != "" {
		t.Fatalf("TUI mirror retained a direct hidraw path: %q", dongle.hidrawPath)
	}
	if dongle.pairingList == nil || len(dongle.pairingList.pairedDevices) != 1 {
		t.Fatalf("mirrored pairing list = %#v", dongle.pairingList)
	}
	headset, exists := selectedHeadsetSnapshot()
	if !exists || headset.deviceConnection != deviceConnectionType_BT || headset.parentDeviceID != 0 {
		t.Fatalf("mirrored headset = %#v, exists=%v", headset, exists)
	}
	if headset.batteryStatus == nil || headset.batteryStatus.levelInPercent != 48 {
		t.Fatalf("mirrored battery = %#v", headset.batteryStatus)
	}
}

func TestRemoteSettingCyclesOnlyServiceChoices(t *testing.T) {
	setting := deviceSettingValue{Remote: &remoteSettingValue{
		Key: "voice-prompts", Value: "Voice", Editable: true,
		Choices: []string{"Tones", "Voice", "Off"},
	}}
	if got, err := setting.nextValueName(); err != nil || got != "Off" {
		t.Fatalf("next remote setting = %q, %v", got, err)
	}
	setting.Remote.Value = "Off"
	if got, err := setting.nextValueName(); err != nil || got != "Tones" {
		t.Fatalf("wrapped remote setting = %q, %v", got, err)
	}
}

func TestBatteryStatusFromIPCRejectsInvalidValue(t *testing.T) {
	if got := batteryStatusFromIPC(&ipc.BatteryInfo{Level: 230}); got != nil {
		t.Fatalf("invalid IPC battery was accepted: %#v", got)
	}
}
