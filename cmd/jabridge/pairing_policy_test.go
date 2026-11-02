package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/btsearch"
)

func TestPairingPolicyKeepsPermissionsSeparate(t *testing.T) {
	tests := []struct {
		pid  uint16
		want pairingPolicy
	}{
		{0x24c7, pairingPolicy{Reads: true, SearchConnect: true, RememberedWrites: true}},
		{0x24c8, pairingPolicy{Reads: true, SearchConnect: true, RememberedWrites: true}},
		{0x24c9, pairingPolicy{Reads: true, SearchConnect: true, BoundSearch: true}},
		{0x2e57, pairingPolicy{Reads: true, SearchConnect: true, RememberedWrites: true, BoundSearch: true, Wire: searchWireDualMode}},
		{0x2e56, pairingPolicy{BoundSearch: true, Wire: searchWireDualMode}},
		{0x24ca, pairingPolicy{}}, {0x24e9, pairingPolicy{}},
		{0x2e50, pairingPolicy{}}, {0x2e51, pairingPolicy{}}, {0xffff, pairingPolicy{}},
	}
	for _, value := range []string{"", "yes", "true", "I_ACCEPT_UNTESTED_PAIRING"} {
		t.Setenv("JABRIDGE_ENABLE_LINK380C_SEARCH_CONNECT", value)
		for _, test := range tests {
			got := pairingPolicyFor(&jabra_DeviceInfo{isDongle: true, productID: test.pid})
			if got != test.want {
				t.Fatalf("wrong policy for %04x and retired option %q: %+v, want %+v", test.pid, value, got, test.want)
			}
			if supportsExperimentalDongleWrites(test.pid) != test.want.RememberedWrites || supportsValidatedFactoryReset(test.pid) != (test.pid == 0x24c7 || test.pid == 0x24c8) {
				t.Fatal("pairing enabled unrelated writes", test.pid)
			}
		}
	}
	if pairingPolicyFor(nil) != (pairingPolicy{}) || pairingPolicyFor(&jabra_DeviceInfo{productID: 0x24c9}) != (pairingPolicy{}) {
		t.Fatal("a non-dongle was granted dongle permissions")
	}
}

func TestLink380CUIUsesDaemonPermission(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, make(devices), -1, -1)
	old := currentTUIBackend()
	setTUIBackend(&tuiIPCBackend{})
	t.Cleanup(func() { setTUIBackend(old) })
	for _, allowed := range []bool{false, true} {
		info := ipc.DeviceInfo{ID: 7, Instance: "candidate", IsDongle: true, PID: 0x24c9, Name: "Link 380c", Selected: true, SearchConnect: allowed}
		replaceTUIDeviceState([]ipc.DeviceInfo{info}, nil, ipc.FeatureInfo{})
		if searchPreviewIsReadOnly() == allowed || !rememberedPreviewIsReadOnly() {
			t.Fatal("client option or fresh pairing unlocked the remembered list", allowed)
		}
	}
	info := ipc.DeviceInfo{ID: 7, Instance: "candidate", IsDongle: true, PID: 0x24c9, Selected: true, SearchConnect: true}
	replaceTUIDeviceState([]ipc.DeviceInfo{info}, nil, ipc.FeatureInfo{})
	if searchPreviewIsReadOnly() {
		t.Fatal("client rejected the advertised daemon permission")
	}
}

func TestLink380CServerAdvertisesEffectivePairingPolicy(t *testing.T) {
	withDeviceState(t, devices{0: {deviceID: 0, instance: "candidate", isDongle: true, productID: 0x24c9}}, -1, 0)
	list := (&jabraAPIBridge{}).ListDevices()
	if len(list) != 1 || !list[0].SearchConnect {
		t.Fatal("default service pairing permission missing", list)
	}
}

func TestUnsupportedPairingConnectRefusedBeforeStop(t *testing.T) {
	dongle := &jabra_DeviceInfo{deviceID: 0, instance: "candidate", productID: 0x2e56, isDongle: true, hidrawPath: "synthetic-no-device", searchConnect: true}
	withDeviceState(t, devices{0: dongle}, -1, 0)
	io := &previewSearchCounter{}
	session, err := btsearch.Start(context.Background(), io, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Stop() })
	nativeSearch.Lock()
	oldSession, oldTarget, oldToken := nativeSearch.session, nativeSearch.target, nativeSearch.token
	nativeSearch.session, nativeSearch.target, nativeSearch.token = session, dongle, strings.Repeat("c", 32)
	nativeSearch.Unlock()
	t.Cleanup(func() {
		nativeSearch.Lock()
		nativeSearch.session, nativeSearch.target, nativeSearch.token = oldSession, oldTarget, oldToken
		nativeSearch.Unlock()
	})
	if err := connectSearchDeviceBound(0, strings.Repeat("c", 32)); err == nil || !strings.Contains(err.Error(), "not enabled") || io.stops.Load() != 0 {
		t.Fatal("client-advertised permission bypassed daemon policy or stopped discovery", err, io.stops.Load())
	}
}

func TestLink380CDefaultPairingDoesNotPermitRememberedOrSettingsWrites(t *testing.T) {
	withDeviceState(t, devices{0: {deviceID: 0, instance: "candidate", productID: 0x24c9, isDongle: true, hidrawPath: "synthetic-no-device"}}, -1, 0)
	for _, test := range []struct {
		action  func() error
		message string
	}{
		{func() error { return connectRememberedDevice(0) }, "not enabled"},
		{func() error { return disconnectRememberedDevice(0) }, "not enabled"},
		{func() error { return forgetRememberedDevice(0) }, "not enabled"},
		{func() error { return setAutoPairing(true) }, "read-only"},
		{func() error { return factoryResetConfirmed(0) }, "not enabled"},
	} {
		if err := test.action(); err == nil || !strings.Contains(err.Error(), test.message) {
			t.Fatal("fresh pairing passed an unrelated operation's policy gate", err)
		}
	}
	for _, definition := range settingDefinitions(settingScopeDongle) {
		if canEditBoolSetting(deviceForID(0), definition) {
			t.Fatal("fresh pairing unlocked a dongle setting", definition.Key)
		}
	}
}

type previewSearchCounter struct{ stops atomic.Int32 }

func (*previewSearchCounter) Start(context.Context) error  { return nil }
func (p *previewSearchCounter) Stop(context.Context) error { p.stops.Add(1); return nil }
func (*previewSearchCounter) Close() error                 { return nil }
func (*previewSearchCounter) Read(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestLink380CBoundIdentityChecksRejectChangesBeforeOpeningUSB(t *testing.T) {
	for _, change := range []string{"instance", "path", "pid", "usb-path", "serial", "selection"} {
		t.Run(change, func(t *testing.T) {
			d := &jabra_DeviceInfo{deviceID: 0, instance: "first", productID: 0x24c9, vendorID: jabraVendorID, isDongle: true, hidrawPath: "synthetic", usbDevicePath: "/sys/synthetic", serialNumber: "first-serial"}
			other := &jabra_DeviceInfo{deviceID: 1, instance: "other", productID: 0x24c9, isDongle: true, hidrawPath: "other"}
			withDeviceState(t, devices{0: d, 1: other}, -1, 0)
			link := &searchLink{device: deviceForID(0), pairing: true}
			deviceStateMu.Lock()
			switch change {
			case "instance":
				deviceManager[0].instance = "replacement"
			case "path":
				deviceManager[0].hidrawPath = "replacement"
			case "pid":
				deviceManager[0].productID = 0x24ca
			case "usb-path":
				deviceManager[0].usbDevicePath = "/sys/other"
			case "serial":
				deviceManager[0].serialNumber = "replacement"
			case "selection":
				selectedDongle = 1
			}
			deviceStateMu.Unlock()
			if err := link.Valid(); err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatal("stale attachment reached USB binding", change, err)
			}
		})
	}
}
