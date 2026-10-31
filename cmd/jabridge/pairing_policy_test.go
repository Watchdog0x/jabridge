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

func preservePairingOption(t *testing.T) {
	t.Helper()
	old := link380CSearchConnectEnabled.Load()
	t.Cleanup(func() { link380CSearchConnectEnabled.Store(old) })
}

func TestLink380COptInNeverGrantsOtherWrites(t *testing.T) {
	preservePairingOption(t)
	for _, value := range []string{"", "yes", "true", link380CSearchConnectAck} {
		t.Setenv(link380CSearchConnectEnv, value)
		capturePairingPreviewOption()
		for _, pid := range []uint16{0x24c7, 0x24c8, 0x24c9, 0x24ca, 0x24e9, 0x2e50, 0x2e51, 0x2e56, 0x2e57} {
			d := &jabra_DeviceInfo{isDongle: true, productID: pid}
			p := pairingPolicyFor(d)
			known := pid == 0x24c7 || pid == 0x24c8 || pid == 0x2e57
			reads := known || pid == 0x24c9 && value == link380CSearchConnectAck
			pair := known || pid == 0x24c9 && value == link380CSearchConnectAck
			if p.Reads != reads || p.SearchConnect != pair || p.RememberedWrites != known {
				t.Fatalf("wrong policy for %04x and option %q: %+v", pid, value, p)
			}
			if p.BoundSearch != (pid == 0x24c9 || pid == 0x2e56 || pid == 0x2e57) || (p.Wire == searchWireDualMode) != (pid == 0x2e56 || pid == 0x2e57) {
				t.Fatal("transport identity and wire mode were coupled", pid, p)
			}
			if supportsExperimentalDongleWrites(pid) != known || supportsValidatedFactoryReset(pid) != (pid == 0x24c7 || pid == 0x24c8) {
				t.Fatal("preview enabled unrelated writes", pid)
			}
		}
	}
	t.Setenv(link380CSearchConnectEnv, "")
	t.Setenv(experimentalWritesEnv, experimentalWritesAck)
	capturePairingPreviewOption()
	if pairingPolicyFor(&jabra_DeviceInfo{isDongle: true, productID: 0x24c9}).SearchConnect {
		t.Fatal("global write option enabled the separate preview")
	}
	if pairingPolicyFor(&jabra_DeviceInfo{productID: 0x24c9}) != (pairingPolicy{}) {
		t.Fatal("a headset was granted dongle permissions")
	}
}

func TestLink380CUIUsesDaemonPermissionNotClientEnvironment(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, make(devices), -1, -1)
	preservePairingOption(t)
	link380CSearchConnectEnabled.Store(true)
	for _, allowed := range []bool{false, true} {
		info := ipc.DeviceInfo{ID: 7, Instance: "candidate", IsDongle: true, PID: 0x24c9, Name: "Link 380c", Selected: true, SearchConnect: allowed}
		replaceTUIDeviceState([]ipc.DeviceInfo{info}, nil, ipc.FeatureInfo{})
		if searchPreviewIsReadOnly() == allowed || !rememberedPreviewIsReadOnly() {
			t.Fatal("client option or fresh pairing unlocked the remembered list", allowed)
		}
	}
	link380CSearchConnectEnabled.Store(false)
	info := ipc.DeviceInfo{ID: 7, Instance: "candidate", IsDongle: true, PID: 0x24c9, Selected: true, SearchConnect: true}
	replaceTUIDeviceState([]ipc.DeviceInfo{info}, nil, ipc.FeatureInfo{})
	if searchPreviewIsReadOnly() {
		t.Fatal("client required its own copy of the daemon option")
	}
}

func TestLink380CServerAdvertisesEffectivePairingPolicy(t *testing.T) {
	withDeviceState(t, devices{0: {deviceID: 0, instance: "candidate", isDongle: true, productID: 0x24c9}}, -1, 0)
	preservePairingOption(t)
	for _, allowed := range []bool{false, true} {
		link380CSearchConnectEnabled.Store(allowed)
		list := (&jabraAPIBridge{}).ListDevices()
		if len(list) != 1 || list[0].SearchConnect != allowed {
			t.Fatal("effective daemon capability missing", list)
		}
	}
}

func TestLink380CConnectRefusedBeforeStopWithoutDaemonOptIn(t *testing.T) {
	preservePairingOption(t)
	link380CSearchConnectEnabled.Store(false)
	dongle := &jabra_DeviceInfo{deviceID: 0, instance: "candidate", productID: 0x24c9, isDongle: true, hidrawPath: "synthetic-no-device", searchConnect: true}
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

func TestLink380COptInDoesNotPermitRememberedOrSettingsWrites(t *testing.T) {
	withDeviceState(t, devices{0: {deviceID: 0, instance: "candidate", productID: 0x24c9, isDongle: true, hidrawPath: "synthetic-no-device"}}, -1, 0)
	preservePairingOption(t)
	link380CSearchConnectEnabled.Store(true)
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
			t.Fatal("opt-in passed an unrelated operation's policy gate", err)
		}
	}
	for _, definition := range settingDefinitions(settingScopeDongle) {
		if canEditBoolSetting(deviceForID(0), definition) {
			t.Fatal("opt-in unlocked a dongle setting", definition.Key)
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
			preservePairingOption(t)
			link380CSearchConnectEnabled.Store(true)
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
