package main

import (
	"strings"
	"testing"
)

func TestSwitchableDevicesAreSortedAndMarkActive(t *testing.T) {
	withDeviceState(t, devices{
		4: {deviceID: 4, deviceName: "Second headset", productID: 0x2222},
		1: {deviceID: 1, deviceName: "Link 380", productID: 0x24c7, isDongle: true},
		2: {deviceID: 2, deviceName: "First headset", productID: 0x1111},
	}, 2, 1)
	items := switchableDevices()
	if len(items) != 3 || items[0].RegistryID != 1 || items[1].RegistryID != 2 || items[2].RegistryID != 4 {
		t.Fatalf("switch items = %#v", items)
	}
	if !items[0].Active || !items[1].Active || items[2].Active {
		t.Fatalf("active markers = %#v", items)
	}
}

func TestSelectHeadsetAndDongle(t *testing.T) {
	withDeviceState(t, devices{
		0: {deviceID: 0, deviceName: "Link A", isDongle: true},
		1: {deviceID: 1, deviceName: "USB headset"},
		2: {deviceID: 2, deviceName: "Link B", isDongle: true},
		3: {deviceID: 3, deviceName: "Wireless headset", deviceConnection: deviceConnectionType_BT, parentDeviceID: 2},
	}, 1, 0)

	if _, err := selectRegistryDevice(3); err != nil {
		t.Fatal(err)
	}
	if selectedHeadset != 3 || selectedDongle != 2 {
		t.Fatalf("wireless selection = headset %d dongle %d", selectedHeadset, selectedDongle)
	}
	if _, err := selectRegistryDevice(0); err != nil {
		t.Fatal(err)
	}
	if selectedDongle != 0 || selectedHeadset != 1 {
		t.Fatalf("dongle switch = headset %d dongle %d", selectedHeadset, selectedDongle)
	}
}

func TestSelectedConnectionChoosesMatchingPipeWireName(t *testing.T) {
	withDeviceState(t, devices{
		0: {deviceID: 0, deviceName: "Jabra Link 380", isDongle: true},
		1: {deviceID: 1, deviceName: "Jabra Evolve2 65", deviceConnection: deviceConnectionType_USB},
		2: {deviceID: 2, deviceName: "Jabra Evolve2 65", deviceConnection: deviceConnectionType_BT, parentDeviceID: 0},
	}, 1, 0)
	_, target, follow, err := selectRegistryDeviceState(2)
	if err != nil || !follow || target != "Jabra Link 380" {
		t.Fatalf("dongle route = %q, %v, %v", target, follow, err)
	}
	_, target, follow, err = selectRegistryDeviceState(1)
	if err != nil || !follow || target != "Jabra Evolve2 65" {
		t.Fatalf("USB route = %q, %v, %v", target, follow, err)
	}
}

func TestSelectingDonglePrefersItsWirelessHeadsetOverDirectUSB(t *testing.T) {
	withDeviceState(t, devices{
		0: {deviceID: 0, deviceName: "USB headset", deviceConnection: deviceConnectionType_USB},
		1: {deviceID: 1, deviceName: "Link 380", isDongle: true},
		2: {deviceID: 2, deviceName: "Wireless headset", deviceConnection: deviceConnectionType_BT, parentDeviceID: 1},
	}, 0, 1)
	if got := firstHeadsetForDongleLocked(1); got != 2 {
		t.Fatalf("headset for dongle = %d, want wireless child 2", got)
	}
}

func TestIPCDeviceListMarksServiceSelections(t *testing.T) {
	withDeviceState(t, devices{
		4: {deviceID: 4, deviceName: "Link 380", isDongle: true},
		7: {deviceID: 7, deviceName: "USB headset", deviceConnection: deviceConnectionType_USB},
		9: {deviceID: 9, deviceName: "Wireless headset", deviceConnection: deviceConnectionType_BT, parentDeviceID: 4},
	}, 9, 4)
	selected := make(map[uint16]bool)
	for _, device := range (&jabraAPIBridge{}).ListDevices() {
		selected[device.ID] = device.Selected
	}
	if !selected[4] || selected[7] || !selected[9] {
		t.Fatalf("IPC selected flags = %#v", selected)
	}
}

func TestSwitchDeviceLabelIsSimpleAndShowsConnection(t *testing.T) {
	item := switchDeviceItem{Active: true, Device: &jabra_DeviceInfo{
		deviceName: "Evolve2 65", productID: 0x24b7, deviceConnection: deviceConnectionType_BT,
	}}
	label := switchDeviceLabel(item)
	for _, want := range []string{"* Headset", "Evolve2 65", "through dongle", "0b0e:24b7"} {
		if !strings.Contains(label, want) {
			t.Fatalf("label %q does not contain %q", label, want)
		}
	}
}
