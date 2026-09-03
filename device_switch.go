package main

import (
	"fmt"
	"sort"
)

type switchDeviceItem struct {
	RegistryID int
	Device     *jabra_DeviceInfo
	Active     bool
}

func switchableDevices() []switchDeviceItem {
	snapshots := deviceSnapshots()
	ids := make([]int, 0, len(snapshots))
	for id := range snapshots {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	deviceStateMu.RLock()
	activeDongle := selectedDongle
	activeHeadset := selectedHeadset
	deviceStateMu.RUnlock()

	items := make([]switchDeviceItem, 0, len(ids))
	for _, id := range ids {
		device := snapshots[id]
		if device == nil {
			continue
		}
		active := id == activeHeadset
		if device.isDongle {
			active = id == activeDongle
		}
		items = append(items, switchDeviceItem{RegistryID: id, Device: device, Active: active})
	}
	return items
}

func selectRegistryDevice(registryID int) (string, error) {
	deviceStateMu.Lock()
	device, exists := deviceManager[registryID]
	if !exists || device == nil {
		deviceStateMu.Unlock()
		return "", fmt.Errorf("device %d is no longer connected", registryID)
	}
	name := device.deviceName
	if device.isDongle {
		selectedDongle = registryID
		if current, ok := deviceManager[selectedHeadset]; ok && current != nil &&
			current.deviceConnection == deviceConnectionType_BT && current.parentDeviceID != device.deviceID {
			selectedHeadset = firstHeadsetForDongleLocked(device.deviceID)
		}
	} else {
		selectedHeadset = registryID
		if device.deviceConnection == deviceConnectionType_BT {
			for id, candidate := range deviceManager {
				if candidate != nil && candidate.isDongle && candidate.deviceID == device.parentDeviceID {
					selectedDongle = id
					break
				}
			}
		}
	}
	deviceStateMu.Unlock()
	requestUIRedraw()
	return name, nil
}

func firstHeadsetForDongleLocked(parentID uint16) int {
	selected := -1
	for id, device := range deviceManager {
		if device == nil || device.isDongle {
			continue
		}
		if device.deviceConnection == deviceConnectionType_USB || device.parentDeviceID == parentID {
			if selected == -1 || id < selected {
				selected = id
			}
		}
	}
	return selected
}

func switchDeviceLabel(item switchDeviceItem) string {
	kind := "Headset"
	connection := "USB"
	if item.Device.isDongle {
		kind = "Dongle"
	} else if item.Device.deviceConnection == deviceConnectionType_BT {
		connection = "through dongle"
	}
	marker := " "
	if item.Active {
		marker = "*"
	}
	return fmt.Sprintf("%s %s: %s (%s, 0b0e:%04x)", marker, kind, item.Device.deviceName, connection, item.Device.productID)
}
