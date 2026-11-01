package main

import (
	"errors"
	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"testing"
)

func TestStablePairingDefaultsRejectCachedAndRawReadsBeforeIO(t *testing.T) {
	preservePairingOption(t)
	link380CSearchConnectEnabled.Store(false)
	t.Setenv(experimentalWritesEnv, experimentalWritesAck)
	for _, pid := range []uint16{0x24c9, 0x24ca, 0x24e9, 0x2e56} {
		d := &jabra_DeviceInfo{deviceID: 0, instance: "default-off", productID: pid, isDongle: true, hidrawPath: "synthetic-no-device", searchConnect: true, pairingList: &pairingList{pairedDevices: []pairedDevice{{deviceName: "Cached", isConnected: true}}}}
		withDeviceState(t, devices{0: d}, -1, 0)
		if _, err := getPairingList(0); !errors.Is(err, ErrNotSupported) {
			t.Fatal("disabled list reached transport", pid, err)
		}
		if list := (&jabraAPIBridge{}).GetPairingList(); len(list) != 0 {
			t.Fatal("disabled list exposed cached entries", pid, list)
		}
		if err := setDongleInBTPairing(true); !errors.Is(err, ErrNotSupported) {
			t.Fatal("global write flag bypassed pairing policy", pid, err)
		}
	}
}

func TestOptedInServiceKeepsFindVisibleToClient(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, make(devices), -1, -1)
	preservePairingOption(t)
	link380CSearchConnectEnabled.Store(false) // The client never captures the option.
	old := currentTUIBackend()
	setTUIBackend(&tuiIPCBackend{})
	t.Cleanup(func() { setTUIBackend(old) })
	for _, granted := range []bool{false, true} {
		replaceTUIDeviceState([]ipc.DeviceInfo{{ID: 0, Instance: "service", Name: "Link 380c", IsDongle: true, PID: 0x24c9, Selected: true, SearchConnect: granted}}, nil, ipc.FeatureInfo{})
		updateStartMenu()
		found := false
		for _, item := range startMenu {
			found = found || item.id == 0
		}
		if found != granted || !rememberedPreviewIsReadOnly() {
			t.Fatal("client lost daemon permission or unlocked remembered actions", granted, found)
		}
	}
}
