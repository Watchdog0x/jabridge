package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/firmware"
)

func TestWirelessFirmwareIdentitySurvivesIPCHandoff(t *testing.T) {
	parent := &jabra_DeviceInfo{deviceID: 0, productID: 0x1131, isDongle: true, hidrawPath: "/isolated/hidraw"}
	withDeviceState(t, devices{0: parent}, -1, 0)
	if !upsertDongleChildIdentity(0, 0x1116, "Engage 55", "private-fixture-serial") {
		t.Fatal("child was not registered")
	}
	child, ok := selectedHeadsetSnapshot()
	if !ok {
		t.Fatal("child missing")
	}
	updateDeviceByID(child.deviceID, func(stored *jabra_DeviceInfo) { stored.variantType = "01-72"; stored.firmwareVersion = "5.17.0" })
	infos := (&jabraAPIBridge{}).ListDevices()
	encoded, err := json.Marshal(infos)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-fixture-serial") {
		t.Fatal("raw headset serial leaked through IPC")
	}
	wanted := firmware.WirelessFirmwareIdentity(0x1116, "private-fixture-serial", "0172")
	replaceTUIDeviceState(infos, nil, ipc.FeatureInfo{})
	child, ok = selectedHeadsetSnapshot()
	if !ok || child.firmwareIdentity != wanted || child.parentDeviceID != 0 || child.deviceConnection != deviceConnectionType_BT {
		t.Fatal("wireless firmware selection lost identity or parent")
	}
}

func TestWirelessSamePIDReplacementInvalidatesMenuSelection(t *testing.T) {
	parent := &jabra_DeviceInfo{deviceID: 0, productID: 0x1131, isDongle: true, hidrawPath: "/isolated/hidraw"}
	withDeviceState(t, devices{0: parent}, -1, 0)
	upsertDongleChildIdentity(0, 0x1116, "Engage 55", "old-fixture")
	child, ok := selectedHeadsetSnapshot()
	if !ok {
		t.Fatal("child missing")
	}
	updateDeviceByID(child.deviceID, func(stored *jabra_DeviceInfo) { stored.variantType = "01-72"; stored.firmwareVersion = "5.17.0" })
	if !upsertDongleChildIdentity(0, 0x1116, "Engage 55", "replacement-fixture") {
		t.Fatal("same-PID replacement was not detected")
	}
	replacement, ok := selectedHeadsetSnapshot()
	if !ok || replacement.instance == child.instance || replacement.variantType != "" || replacement.firmwareVersion != "" || replacement.serialNumber != "replacement-fixture" {
		t.Fatal("replacement inherited stale identity or firmware metadata")
	}
}
