package main

import (
	"strings"
	"testing"
	"time"
)

func metadataTestDongle() *jabra_DeviceInfo {
	return &jabra_DeviceInfo{deviceID: 0, instance: strings.Repeat("a", 32), productID: 0x24c7, vendorID: jabraVendorID, usbDevicePath: "/sys/test-device", deviceConnection: deviceConnectionType_USB, isDongle: true, featureFlags: &featureFlags{}}
}

func TestUSBMetadataRecoversAfterInitialReadFailure(t *testing.T) {
	device := metadataTestDongle()
	now := time.Now()
	device.metadataProbeAt = now
	withDeviceState(t, devices{0: device}, -1, 0)
	calls := 0
	read := func(snapshot *jabra_DeviceInfo) {
		calls++
		updateDeviceSnapshot(snapshot, func(current *jabra_DeviceInfo) {
			current.firmwareVersion = "1.16.0"
			current.variantType = "04-0B"
			current.gnpDestinationKnown = true
		})
	}
	retryUSBMetadata(deviceForID(0), now.Add(time.Second), read)
	if calls != 0 {
		t.Fatal("early retry was not throttled")
	}
	retryUSBMetadata(deviceForID(0), now.Add(usbMetadataRetryInterval), read)
	listed := (&jabraAPIBridge{}).ListDevices()
	if calls != 1 || len(listed) != 1 || listed[0].Firmware != "1.16.0" || listed[0].Variant != "04-0B" {
		t.Fatalf("recovered metadata was not published through IPC: calls=%d devices=%+v", calls, listed)
	}
	retryUSBMetadata(deviceForID(0), now.Add(2*usbMetadataRetryInterval), read)
	if calls != 1 {
		t.Fatal("complete metadata was polled again")
	}
}

func TestUSBMetadataFailedRetriesAreBounded(t *testing.T) {
	device := metadataTestDongle()
	withDeviceState(t, devices{0: device}, -1, 0)
	now := time.Now()
	calls := 0
	read := func(*jabra_DeviceInfo) { calls++ }
	for i := range 60 {
		retryUSBMetadata(deviceForID(0), now.Add(time.Duration(i)*time.Second), read)
	}
	if calls != 2 || deviceForID(0).firmwareVersion != "" {
		t.Fatalf("calls=%d; failed reads must not invent firmware", calls)
	}
}

func TestUSBMetadataRejectsReusedDeviceID(t *testing.T) {
	device := metadataTestDongle()
	withDeviceState(t, devices{0: device}, -1, 0)
	stale := deviceForID(0)
	updateDeviceByID(0, func(current *jabra_DeviceInfo) { current.instance = strings.Repeat("b", 32) })
	if updateDeviceSnapshot(stale, func(current *jabra_DeviceInfo) { current.firmwareVersion = "wrong" }) {
		t.Fatal("late metadata was applied to a replacement")
	}
	retryUSBMetadata(stale, time.Now(), func(*jabra_DeviceInfo) { t.Fatal("stale device was probed") })
	if deviceForID(0).firmwareVersion != "" {
		t.Fatal("replacement inherited old firmware")
	}
}
