package main

import (
	"errors"
	"strings"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

// A report narrows one exact setup; it never promotes a PID or firmware family.
// https://github.com/Watchdog0x/jabridge/pull/55#issuecomment-6436666392
func noiseCycleQualifiedMasks(profile *modelcatalog.Capabilities, route *jabra_DeviceInfo) ([]byte, bool) {
	if profile == nil || !profile.ExactFirmwareProfile || profile.PID != 0x2549 ||
		!strings.EqualFold(profile.Variant, "01-A3") || profile.Firmware != "1.0.1" || profile.DeviceFirmware != "1.0.1" ||
		route == nil || !route.isDongle || route.productID != 0x2e56 || route.firmwareVersion != "3.2.0" {
		return nil, false
	}
	return []byte{7, 6, 3}, true // HT + ANC + Off; HT + ANC; HT + Off.
}

func constrainNoiseCycle(device *jabra_DeviceInfo, profile *modelcatalog.Capabilities, definition choiceSettingDefinition) (choiceSettingDefinition, bool) {
	codec, ok := definition.Codec.(noiseCycleCodec)
	if !ok {
		return definition, false
	}
	var route *jabra_DeviceInfo
	if device != nil && device.deviceConnection == deviceConnectionType_BT {
		route = deviceForID(device.parentDeviceID)
	}
	return bindNoiseCycleProfile(definition, codec, profile, route)
}

func bindNoiseCycleProfile(definition choiceSettingDefinition, codec noiseCycleCodec, profile *modelcatalog.Capabilities, route *jabra_DeviceInfo) (choiceSettingDefinition, bool) {
	// A definition may be rebound after reconnecting. Never retain the old
	// route or its qualified choices when the new snapshot does not match.
	codec.selections, codec.observed = 1<<codec.modes, 0
	codec.route, codec.routeBound = noiseCycleRoute{}, false
	definition.Choices = []settingChoice{noiseCycleChoice(codec.modes)}
	masks, qualified := noiseCycleQualifiedMasks(profile, route)
	qualified = qualified && codec.modes == 7
	if qualified {
		codec.route, codec.routeBound = noiseCycleRouteFromDevice(route), true
		codec.selections = 0
		definition.Choices = nil
		for _, mask := range masks {
			if mask&codec.modes == mask {
				codec.selections |= 1 << mask
				definition.Choices = append(definition.Choices, noiseCycleChoice(mask))
			}
		}
		definition.Help = "Only the combinations confirmed on this headset and dongle firmware are offered."
	} else {
		definition.Help = "Other combinations are not known for this firmware."
	}
	definition.Codec = codec
	return definition, qualified
}

// Registry metadata is checked without adding device queries. The attachment
// identity and held HID path also catch an observed dongle re-enumeration.
type noiseCycleRoute struct {
	id, pid                  uint16
	instance, firmware, path string
}

func noiseCycleRouteFromDevice(parent *jabra_DeviceInfo) noiseCycleRoute {
	return noiseCycleRoute{id: parent.deviceID, pid: parent.productID, instance: parent.instance, firmware: parent.firmwareVersion, path: parent.hidrawPath}
}

func noiseCycleRouteCurrent(device *jabra_DeviceInfo, codec noiseCycleCodec) error {
	if !codec.routeBound {
		return nil
	}
	changed := errors.New("dongle connection or firmware changed; reopen Settings before saving")
	if device == nil {
		return changed
	}
	child := deviceForID(device.deviceID)
	if child == nil || child.deviceConnection != deviceConnectionType_BT || validateSettingTarget(child, settingTarget(device)) != nil {
		return changed
	}
	parent := deviceForID(child.parentDeviceID)
	if parent == nil || !parent.isDongle || parent.instance == "" || noiseCycleRouteFromDevice(parent) != codec.route {
		return changed
	}
	return nil
}

func choiceSettingRouteGuard(device *jabra_DeviceInfo, definition choiceSettingDefinition) func(*hidrawConn) error {
	codec, ok := definition.Codec.(noiseCycleCodec)
	if !ok || !codec.routeBound {
		return nil
	}
	return func(h *hidrawConn) error {
		if err := noiseCycleRouteCurrent(device, codec); err != nil {
			return err
		}
		if h == nil || h.path != codec.route.path {
			return errors.New("dongle connection or firmware changed; reopen Settings before saving")
		}
		return nil
	}
}
