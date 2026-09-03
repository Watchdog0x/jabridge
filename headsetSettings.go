package main

/*
#cgo CFLAGS: -Iheaders

#include "Common.h"
#include "JabraDeviceConfig.h"
#include <stdlib.h>
*/
import "C"
import (
	"fmt"
	"unsafe"
)

// controlType mirrors the C ControlType enum from JabraDeviceConfig.h.
type controlType int

const (
	cntrlRadio      controlType = iota // 0
	cntrlToggle                        // 1
	cntrlComboBox                      // 2
	cntrlDrpDown                       // 3
	cntrlLabel                         // 4
	cntrlTextBox                       // 5
	cntrlButton                        // 6
	cntrlEditButton                    // 7
	cntrlHorzRuler                     // 8
	cntrlPwdTextBox                    // 9
	cntrlUnknown                       // 10
)

// listKeyValue is the Go mirror of C.ListKeyValue (key → display label).
type listKeyValue struct {
	key   uint16
	value string
}

// settingInfo is the Go mirror of one C.SettingInfo entry.
type settingInfo struct {
	guid, name, helpText, groupName string
	cntrlType                       controlType
	dataType                        int // 0 = byte, 1 = string
	currByte                        uint8
	currString                      string
	listKeyValue                    []listKeyValue
	isDeviceRestart                 bool
	isProtected                     bool
	isProtectionEnabled             bool
}

// deviceSettings holds the parsed settings list and the live C pointer.
// raw is kept alive so we can mutate currValue in-place and call Jabra_SetSettings.
type deviceSettings struct {
	items []settingInfo
	raw   *C.DeviceSettings
}

// getDeviceSettings fetches all settings for deviceID from the Jabra SDK.
//
// The SDK downloads a device-model manifest from Jabra's servers and overlays
// the current EEPROM values. Every setting exposed in Jabra Direct (sidetone
// level, mute reminder, auto-accept on boom-arm, on-head auto-pause, …) is
// returned here, identified by a device-specific GUID.
//
// The caller MUST eventually call freeDeviceSettings to release C memory.
func getDeviceSettings(deviceID uint16) (*deviceSettings, error) {
	cSettings := C.Jabra_GetSettings(C.ushort(deviceID))
	if cSettings == nil {
		return nil, fmt.Errorf("Jabra_GetSettings returned nil (device %d)", deviceID)
	}

	if err := checkErrorStatus(errorStatusCode(cSettings.errStatus)); err != nil {
		C.Jabra_FreeDeviceSettings(cSettings)
		return nil, err
	}

	count := int(cSettings.settingCount)
	ds := &deviceSettings{
		items: make([]settingInfo, 0, count),
		raw:   cSettings,
	}
	if count == 0 {
		return ds, nil
	}

	cInfos := (*[1 << 14]C.SettingInfo)(unsafe.Pointer(cSettings.settingInfo))[:count:count]
	for _, ci := range cInfos {
		s := settingInfo{
			guid:                C.GoString(ci.guid),
			name:                C.GoString(ci.name),
			helpText:            C.GoString(ci.helpText),
			groupName:           C.GoString(ci.groupName),
			cntrlType:           controlType(ci.cntrlType),
			dataType:            int(ci.settingDataType),
			isDeviceRestart:     bool(ci.isDeviceRestart),
			isProtected:         bool(ci.isSettingProtected),
			isProtectionEnabled: bool(ci.isSettingProtectionEnabled),
		}

		if ci.currValue != nil {
			if s.dataType == 0 {
				s.currByte = uint8(*(*C.uint8_t)(ci.currValue))
			} else {
				s.currString = C.GoString((*C.char)(ci.currValue))
			}
		}

		if ci.listSize > 0 && ci.listKeyValue != nil {
			n := int(ci.listSize)
			opts := (*[1 << 16]C.ListKeyValue)(unsafe.Pointer(ci.listKeyValue))[:n:n]
			for _, o := range opts {
				s.listKeyValue = append(s.listKeyValue, listKeyValue{
					key:   uint16(o.key),
					value: C.GoString(o.value),
				})
			}
		}

		ds.items = append(ds.items, s)
	}
	return ds, nil
}

// freeDeviceSettings releases the C memory owned by ds.
func freeDeviceSettings(ds *deviceSettings) {
	if ds != nil && ds.raw != nil {
		C.Jabra_FreeDeviceSettings(ds.raw)
		ds.raw = nil
	}
}

// applySettingByte writes a single byte-valued setting (identified by GUID)
// to the headset's EEPROM.
//
// We use the single-setting form Jabra_GetSetting(guid) instead of writing the
// full settings blob back. The blob form (Jabra_SetSettings on the whole list)
// fails with ParameterFail (4) if *any* other setting in the blob is invalid
// or write-protected — which is common on real devices.
//
// After the write, *ds is refreshed by freeing + reloading the full settings
// list so that the displayed value reflects what the device actually accepted.
func applySettingByte(deviceID uint16, ds **deviceSettings, guid string, newByte uint8) error {
	cGuid := C.CString(guid)
	defer C.free(unsafe.Pointer(cGuid))

	one := C.Jabra_GetSetting(C.ushort(deviceID), cGuid)
	if one == nil {
		return fmt.Errorf("Jabra_GetSetting(%q) returned nil", guid)
	}
	defer C.Jabra_FreeDeviceSettings(one)

	if err := checkErrorStatus(errorStatusCode(one.errStatus)); err != nil {
		return err
	}
	if one.settingCount == 0 || one.settingInfo == nil {
		return fmt.Errorf("setting %q not found on device", guid)
	}

	// one.settingInfo points at exactly one SettingInfo.
	info := (*C.SettingInfo)(unsafe.Pointer(one.settingInfo))
	if info.currValue == nil {
		return fmt.Errorf("setting %q has no writable value slot", guid)
	}
	if info.settingDataType != 0 {
		return fmt.Errorf("setting %q is not a byte setting", guid)
	}

	*(*C.uint8_t)(info.currValue) = C.uint8_t(newByte)

	rc := returnCode(int(C.Jabra_SetSettings(C.ushort(deviceID), one)))

	// Reload the full settings blob so the UI reflects the device state.
	freeDeviceSettings(*ds)
	fresh, loadErr := getDeviceSettings(deviceID)
	*ds = fresh

	if rc != nil {
		return rc
	}
	return loadErr
}

// settingValueLabel returns a short human-readable string for the current
// value of a setting (the matching option label, or "ON"/"OFF" for toggles).
func settingValueLabel(s settingInfo) string {
	if len(s.listKeyValue) > 0 {
		for _, o := range s.listKeyValue {
			if o.key == uint16(s.currByte) {
				return o.value
			}
		}
		return fmt.Sprintf("(%d)", s.currByte)
	}
	if s.cntrlType == cntrlToggle {
		if s.currByte != 0 {
			return "ON"
		}
		return "OFF"
	}
	if s.dataType == 1 {
		return s.currString
	}
	return fmt.Sprintf("%d", s.currByte)
}
