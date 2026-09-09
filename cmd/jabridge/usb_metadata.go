package main

import "time"

const usbMetadataRetryInterval = 30 * time.Second

// Metadata can be temporarily unreadable immediately after USB attachment.
// Retry within the service, never in an IPC list/render request. The device
// instance prevents a late read from updating a replacement that reuses its ID.
func refreshKnownUSBMetadata(usbDevice usbDev, now time.Time) {
	for _, device := range deviceSnapshots() {
		if device.deviceConnection == deviceConnectionType_USB && device.usbDevicePath == usbDevice.sysPath && device.productID == usbDevice.productID && device.vendorID == usbDevice.vendorID {
			retryUSBMetadata(device, now, enrichUSBDevice)
			return
		}
	}
}

func retryUSBMetadata(device *jabra_DeviceInfo, now time.Time, read func(*jabra_DeviceInfo)) {
	if device == nil || device.deviceConnection != deviceConnectionType_USB || len(controlPartPlans(device)) != 0 {
		return // Composite controllers have their own refresh path.
	}
	claimed := false
	updateDeviceSnapshot(device, func(current *jabra_DeviceInfo) {
		if current.gnpDestinationKnown && current.firmwareVersion != "" && current.variantType != "" {
			return
		}
		if !current.metadataProbeAt.IsZero() && now.Sub(current.metadataProbeAt) < usbMetadataRetryInterval {
			return
		}
		current.metadataProbeAt = now
		*device = *cloneDeviceInfo(current)
		claimed = true
	})
	if claimed {
		read(device)
	}
}

func updateDeviceSnapshot(expected *jabra_DeviceInfo, update func(*jabra_DeviceInfo)) bool {
	if expected == nil {
		return false
	}
	deviceStateMu.Lock()
	defer deviceStateMu.Unlock()
	for _, current := range deviceManager {
		if current != nil && current.deviceID == expected.deviceID && current.instance == expected.instance && current.productID == expected.productID && current.usbDevicePath == expected.usbDevicePath {
			update(current)
			return true
		}
	}
	return false
}
