package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

// This identifies one observed attachment, not a serial number. A fresh
// value also prevents an editor surviving a service restart from matching a
// recycled numeric device ID.
func newDeviceInstance() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic("cannot create device attachment identity")
	}
	return hex.EncodeToString(data[:])
}

func settingTarget(device *jabra_DeviceInfo) *ipc.SettingTarget {
	if device == nil || device.instance == "" {
		return nil
	}
	return &ipc.SettingTarget{ID: device.deviceID, Instance: device.instance, Topology: device.controlTopology}
}

func validateSettingTarget(device *jabra_DeviceInfo, target *ipc.SettingTarget) error {
	if target == nil || device == nil || target.Instance == "" || target.ID != device.deviceID || target.Instance != device.instance || target.Topology != device.controlTopology {
		return errors.New("device changed while editing; reopen its settings")
	}
	return nil
}

func currentSettingDevice(device *jabra_DeviceInfo) error {
	if device == nil || device.instance == "" {
		return errors.New("device attachment identity is unavailable; reconnect and select it again")
	}
	return validateSettingTarget(deviceForID(device.deviceID), settingTarget(device))
}
