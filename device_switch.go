package main

import (
	"fmt"
	"github.com/Watchdog0x/jabridge/internal/history"
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

func selectRegistryDevice(registryID int) (result string, actionErr error) {
	device, _ := deviceAt(registryID)
	finish := history.Begin(historyDeviceEvent(device, "select"))
	defer history.EndDeferred(finish, &actionErr)
	name, audioTarget, switchAudio, err := selectRegistryDeviceState(registryID)
	if err != nil {
		return "", err
	}
	if switchAudio {
		if err := followSelectedDeviceAudio(audioTarget); err != nil {
			return "", err
		}
	}
	return name, nil
}

func selectRegistryDeviceState(registryID int) (name, audioTarget string, switchAudio bool, err error) {
	deviceStateMu.Lock()
	device, exists := deviceManager[registryID]
	if !exists || device == nil {
		deviceStateMu.Unlock()
		return "", "", false, fmt.Errorf("device %d is no longer connected", registryID)
	}
	name = device.deviceName
	if device.isDongle {
		selectedDongle = registryID
		if current, ok := deviceManager[selectedHeadset]; ok && current != nil &&
			current.deviceConnection == deviceConnectionType_BT && current.parentDeviceID != device.deviceID {
			selectedHeadset = firstHeadsetForDongleLocked(device.deviceID)
		}
	} else {
		switchAudio = true
		audioTarget = device.deviceName
		selectedHeadset = registryID
		if device.deviceConnection == deviceConnectionType_BT {
			for id, candidate := range deviceManager {
				if candidate != nil && candidate.isDongle && candidate.deviceID == device.parentDeviceID {
					selectedDongle = id
					audioTarget = candidate.deviceName
					break
				}
			}
		}
	}
	deviceStateMu.Unlock()
	requestUIRedraw()
	return name, audioTarget, switchAudio, nil
}

func firstHeadsetForDongleLocked(parentID uint16) int {
	wireless, direct := -1, -1
	for id, device := range deviceManager {
		if device == nil || device.isDongle {
			continue
		}
		if device.deviceConnection == deviceConnectionType_BT && device.parentDeviceID == parentID {
			if wireless == -1 || id < wireless {
				wireless = id
			}
		} else if device.deviceConnection == deviceConnectionType_USB && (direct == -1 || id < direct) {
			direct = id
		}
	}
	if wireless >= 0 {
		return wireless
	}
	return direct
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
