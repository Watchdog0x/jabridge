package main

import (
	"context"
	"errors"
	"testing"
)

func controlCacheFixture(t *testing.T) (string, *jabra_DeviceInfo, *jabra_DeviceInfo) {
	t.Helper()
	port := fakeControlPort(t, "2e57")
	dongle := &jabra_DeviceInfo{deviceID: 7, instance: "d", productID: 0x2e57, vendorID: jabraVendorID, isDongle: true, deviceConnection: deviceConnectionType_USB, usbDevicePath: port, hidrawPath: "/dev/hidraw-review-absent", gnpDestinationKnown: true}
	child := &jabra_DeviceInfo{deviceID: 8, instance: "c", productID: 0x4100, vendorID: jabraVendorID, deviceConnection: deviceConnectionType_BT, parentDeviceID: 7, hidrawPath: dongle.hidrawPath, batteryStatus: &batteryStatus{levelInPercent: 61, component: batteryHeadband}}
	withDeviceState(t, devices{7: dongle, 8: child}, 8, 7)
	return port, dongle, child
}

// An owned port must not turn a temporary absence into a lost headset.
func TestControlCacheBatteryAndChildSurviveOwnedAbsence(t *testing.T) {
	port, _, _ := controlCacheFixture(t)
	owner, err := link390ControlGate.exclusive(context.Background(), port, port)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if _, err := getBatteryStatus(8); !errors.Is(err, errControlBusy) {
		t.Fatalf("typed busy lost: %v", err)
	}
	deviceStateMu.Lock()
	dongleChildMisses = 0
	deviceStateMu.Unlock()
	for i := 0; i < 5; i++ {
		refreshSelectedDeviceData()
	}
	deviceStateMu.RLock()
	misses := dongleChildMisses
	deviceStateMu.RUnlock()
	child := deviceForID(8)
	if child == nil || child.batteryStatus == nil || child.batteryStatus.levelInPercent != 61 {
		t.Fatalf("cached child or battery lost: %+v", child)
	}
	if misses != 0 {
		t.Fatalf("busy counted as a child miss: %d", misses)
	}
	if selected, ok := selectedHeadsetSnapshot(); !ok || selected.deviceID != 8 {
		t.Fatal("selected headset lost")
	}
	if selected, ok := selectedDongleSnapshot(); !ok || selected.deviceID != 7 {
		t.Fatal("selected dongle lost")
	}
}

func TestControlCacheNoOwnerMissClearsAsBefore(t *testing.T) {
	controlCacheFixture(t)
	refreshSelectedDeviceData()
	child := deviceForID(8)
	if child != nil && child.batteryStatus != nil {
		t.Fatal("idle failure must still clear battery as before")
	}
}

func TestControlCacheSettingTransportBusyIsTypedForDongleAndChild(t *testing.T) {
	port, dongle, child := controlCacheFixture(t)
	owner, err := link390ControlGate.exclusive(context.Background(), port, port)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	for _, device := range []*jabra_DeviceInfo{dongle, child} {
		if _, _, err := settingTransport(device); !errors.Is(err, errControlBusy) {
			t.Fatalf("%d: %v", device.deviceID, err)
		}
	}
	if !controlDeviceBusy(dongle) || !controlDeviceBusy(child) {
		t.Fatal("busy lookup")
	}
}

func TestControlCacheSearchRefusesMissingScopedParent(t *testing.T) {
	_, dongle, _ := controlCacheFixture(t)
	dongle.usbDevicePath = "/missing/control-test-port"
	if link, err := openSearchLink(dongle); !errors.Is(err, errLink390Identity) {
		if link != nil {
			_ = link.Close()
		}
		t.Fatalf("search did not refuse an unresolved scoped dongle: %v", err)
	}
}
