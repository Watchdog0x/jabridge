package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"golang.org/x/sys/unix"
)

func engageFixture(pid uint16) *jabra_DeviceInfo {
	d := &jabra_DeviceInfo{deviceID: 7, instance: strings.Repeat("a", 32), productID: pid, deviceName: "Engage 50 II", featureFlags: &featureFlags{}}
	parts := []controlPart{{Role: "headset", Address: 1, Path: "/dev/hidraw-test", Variant: "01-72", Firmware: "4.1.3", Identity: "PRIVATE_HEADSET", Ready: true}}
	if pid == 0x4052 {
		parts = append(parts, controlPart{Role: "controller", Address: 3, Path: "/dev/hidraw-test", Variant: "03-05", Firmware: "4.1.3", Identity: "PRIVATE_CONTROLLER", Ready: true})
	}
	applyControlParts(d, parts)
	return d
}

func TestEngageDiscoveryDoesNotStopAtController(t *testing.T) {
	device := engageFixture(0x4052)
	var addresses []byte
	plans := []controlPartPlan{{"controller", 3}, {"headset", 1}}
	parts := discoverControlParts([]string{"/dev/hidraw-test"}, plans, func(path string, plan controlPartPlan) controlPart {
		addresses = append(addresses, plan.Address)
		part, _ := findControlPart(device, plan.Role)
		return part
	})
	applyControlParts(device, parts)
	if len(addresses) != 2 || addresses[0] != 3 || addresses[1] != 1 || device.variantType != "01-72" || device.gnpDestination != 1 || !hasController(device) {
		t.Fatalf("discovery stopped at the wrong identity: %v %#v", addresses, device)
	}
	if parts[0].Variant != "03-05" {
		t.Fatal("controller identity overwritten")
	}
}

func TestEngageRoutesKeepMainSettingsSeparateFromControllerOverrides(t *testing.T) {
	d := engageFixture(0x4052)
	for _, test := range []struct {
		key            string
		override, want byte
	}{
		{"sidetone", 0, 1}, {"music-mode", 0, 1}, {"device-name", 0, 1},
		{"controller-name", 3, 3}, {"smart-ringer", 3, 3}, {"controller-ringtone", 3, 3},
		{"call-button", 0, 1}, {"speed-dial", 0, 1},
	} {
		view, address, err := settingPartRoute(d, test.key, test.override)
		if err != nil || address != test.want || view.gnpDestination != test.want {
			t.Fatalf("%s: address=%d error=%v", test.key, address, err)
		}
	}
	button := deviceSettingValue{Choice: &choiceSettingValue{Definition: buttonFunctionDefinition("call-button", "Call button", "buttonFunctionMfb", 0)}}
	if settingValuePart(d, button) != "controller" {
		t.Fatal("button belongs in controller menu even though it uses the main route")
	}
}

func TestMissingPartNeverFallsBackToOtherPart(t *testing.T) {
	d := engageFixture(0x4052)
	parts := append([]controlPart(nil), d.controlParts...)
	parts[0].Ready = false
	applyControlParts(d, parts)
	if d.variantType != "" || d.gnpDestinationKnown {
		t.Fatal("controller identity substituted for missing headset")
	}
	if _, _, err := settingPartRoute(d, "sidetone", 0); err == nil {
		t.Fatal("missing headset routed to controller")
	}
	if _, address, err := settingPartRoute(d, "controller-name", 3); err != nil || address != 3 {
		t.Fatal("independent controller route lost", err)
	}
	if _, err := lookupDeviceModel(d); err == nil {
		t.Fatal("profile selected without headset identity")
	}
	d = engageFixture(0x4056)
	if _, _, err := settingPartRoute(d, "controller-name", 3); err == nil {
		t.Fatal("missing controller routed to headset")
	}
	if hasController(d) {
		t.Fatal("invented controller for direct USB")
	}
}

func TestControlPartPlansDoNotChangeOtherModelsOrWirelessRoutes(t *testing.T) {
	for _, d := range []*jabra_DeviceInfo{{productID: 0x24c7, isDongle: true}, {productID: 0x24b7}, {productID: 0x0422}, {productID: 0x4052, deviceConnection: deviceConnectionType_BT}} {
		if len(controlPartPlans(d)) != 0 {
			t.Fatal("unrelated model given Engage topology")
		}
		view, address, err := settingPartRoute(d, "sidetone", 0)
		if err != nil || view != d || address != 0 {
			t.Fatal("legacy routing changed")
		}
	}
	for _, pid := range []uint16{0x4051, 0x4052, 0x4053, 0x4054} {
		if len(controlPartPlans(&jabra_DeviceInfo{productID: pid})) != 2 {
			t.Fatal(pid)
		}
	}
	for _, pid := range []uint16{0x4055, 0x4056} {
		if len(controlPartPlans(&jabra_DeviceInfo{productID: pid})) != 1 {
			t.Fatal(pid)
		}
	}
}

func TestPartBindingChangesWithoutUSBDetach(t *testing.T) {
	d := engageFixture(0x4052)
	old := settingTarget(d)
	applyControlParts(d, append([]controlPart(nil), d.controlParts...))
	if err := validateSettingTarget(d, old); err != nil {
		t.Fatal("stable topology invalidated editor")
	}
	parts := append([]controlPart(nil), d.controlParts...)
	parts[1].Identity = "REPLACEMENT_CONTROLLER"
	applyControlParts(d, parts)
	if err := validateSettingTarget(d, old); err == nil {
		t.Fatal("same USB setup accepted replaced part")
	}
	if err := validateSettingTarget(d, &ipc.SettingTarget{ID: d.deviceID, Instance: d.instance}); err == nil {
		t.Fatal("missing topology binding accepted")
	}
	withDeviceState(t, devices{7: d}, 7, -1)
	original := cloneDeviceInfo(d)
	updateDeviceByID(7, func(stored *jabra_DeviceInfo) { stored.controlTopology = newDeviceInstance() })
	if refreshedSettingsDevice(original) != nil {
		t.Fatal("readback followed a changed setup")
	}
}

func TestPartsCopyAndDebugExcludePrivateIdentity(t *testing.T) {
	d := engageFixture(0x4052)
	copy := cloneDeviceInfo(d)
	copy.controlParts[0].Variant = "changed"
	if d.controlParts[0].Variant != "01-72" {
		t.Fatal("part snapshots share storage")
	}
	data, err := json.Marshal(ipcControlParts(d))
	if err != nil || strings.Contains(string(data), "PRIVATE") || strings.Contains(string(data), "hidraw") {
		t.Fatal("private part identity exposed", string(data), err)
	}
	checks := controlPartDiagnostics(d)
	text := fmt.Sprint(checks)
	if len(checks) != 2 || !strings.Contains(text, "address=3") || strings.Contains(text, "PRIVATE") || strings.Contains(text, "hidraw-test") {
		t.Fatal(text)
	}
}

func TestPhysicalUSBPathDoesNotMatchAnotherUnit(t *testing.T) {
	if !pathBelongsToUSB("/sys/devices/usb1/1-2/1-2:1.0/hid", "/sys/devices/usb1/1-2") {
		t.Fatal("correct parent rejected")
	}
	for _, node := range []string{"/sys/devices/usb1/1-20/1-20:1.0/hid", "/sys/devices/usb1/1-3/hid"} {
		if pathBelongsToUSB(node, "/sys/devices/usb1/1-2") {
			t.Fatal("other physical unit accepted")
		}
	}
}

func TestControllerMenuIsAutomaticAndPartScoped(t *testing.T) {
	d := engageFixture(0x4052)
	withDeviceState(t, devices{7: d}, 7, -1)
	updateStartMenu()
	contains := func() bool {
		for _, item := range startMenu {
			if item.id == 7 && item.label == "Controller settings" {
				return true
			}
		}
		return false
	}
	if !contains() {
		t.Fatal("controller menu missing")
	}
	if _, ok := selectedSettingsDevice(settingScopeController); !ok {
		t.Fatal("controller selector missing")
	}
	updateDeviceByID(7, func(d *jabra_DeviceInfo) { applyControlParts(d, d.controlParts[:1]) })
	updateStartMenu()
	if contains() {
		t.Fatal("controller menu survived detach")
	}
	if _, ok := selectedSettingsDevice(settingScopeController); ok {
		t.Fatal("disconnected controller selected")
	}
	if scope, key, err := parseSettingSelector("controller.controller-name"); err != nil || scope != settingScopeController || key != "controller-name" {
		t.Fatal(scope, key, err)
	}
}

func TestControllerOnlyKeepsItsMenuWithoutInventingAHeadset(t *testing.T) {
	d := engageFixture(0x4052)
	parts := append([]controlPart(nil), d.controlParts...)
	parts[0] = controlPart{Role: "headset", Address: 1, Diagnostic: "device rejected query"}
	applyControlParts(d, parts)
	withDeviceState(t, devices{7: d}, 7, -1)
	if !controllerWithoutHeadset(d) || deviceKindLabel(d) != "Controller" {
		t.Fatal("controller labelled as a connected headset")
	}
	updateStartMenu()
	found := false
	for _, item := range startMenu {
		if item.id == 6 {
			t.Fatal("headset menu shown without a responsive headset")
		}
		found = found || item.id == 7
	}
	if !found {
		t.Fatal("controller menu missing")
	}
	if _, _, err := settingPartRoute(d, "call-button", 0); err == nil {
		t.Fatal("headset-owned controller setting routed to another part")
	}
	if _, address, err := settingPartRoute(d, "controller-name", 3); err != nil || address != 3 {
		t.Fatal(address, err)
	}
}

// Exercise the actual GNP query encoder/reply matcher, not just model metadata.
func partIdentitySocket(t *testing.T, expected byte, variant []byte) (*hidrawConn, <-chan error) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	client := &hidrawConn{f: os.NewFile(uintptr(fds[0]), "mock-hid"), path: "mock-hid"}
	peer := os.NewFile(uintptr(fds[1]), "mock-device")
	t.Cleanup(func() { client.close(); _ = peer.Close() })
	done := make(chan error, 1)
	go func() {
		for _, payload := range [][]byte{variant, {5, '4', '.', '1', '.', '3'}, {4, 'u', 'n', 'i', 't'}} {
			buf := make([]byte, 64)
			n, err := peer.Read(buf)
			if err != nil {
				done <- err
				return
			}
			if n < 7 || buf[1] != expected || buf[5] != gnpClassDevInfo || buf[4]&0xc0 != gnpFlagQuery {
				done <- fmt.Errorf("unexpected non-IDENT read packet: %x", buf[:n])
				return
			}
			reply, err := buildGNPReport(0, buf[3], 0xc0, buf[5], buf[6], payload)
			if err != nil {
				done <- err
				return
			}
			reply[2] = expected
			if _, err = peer.Write(reply); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	return client, done
}

func TestPartIdentityQueriesAreReadOnlyAndAddressBound(t *testing.T) {
	for _, test := range []struct {
		role    string
		address byte
		variant []byte
		want    string
	}{{"headset", 1, []byte{2, 1, 0x72}, "01-72"}, {"controller", 3, []byte{2, 3, 5}, "03-05"}} {
		h, done := partIdentitySocket(t, test.address, test.variant)
		part := readControlPart(h, "mock-hid", controlPartPlan{test.role, test.address})
		if !part.Ready || part.Variant != test.want || part.Firmware != "4.1.3" || part.Identity == "" {
			t.Fatalf("identity read failed: %#v", part)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("mock did not complete")
		}
	}
}
