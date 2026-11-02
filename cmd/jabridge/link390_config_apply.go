package main

import (
	"fmt"

	"github.com/Watchdog0x/jabridge/internal/history"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

type link390AppliedSetting struct {
	Setting deviceSettingValue
	Device  *jabra_DeviceInfo
	Changed bool
}

func bindLink390ConfigBool(device *jabra_DeviceInfo, d boolSettingDefinition, profile *modelcatalog.Capabilities, catalogErr error) boolSettingDefinition {
	if d.Scope != settingScopeDongle || d.Key != "softphone-integration" || catalogErr != nil || !matchingDongleSettingModel(device, profile) {
		return d
	}
	property, ok := firstCatalogProperty(profile, d.CatalogProperties)
	if !ok {
		return d
	}
	d.Link390Config, d.RequireExactProfile, d.RequireBooleanChoices = true, true, true
	d.ValidatedLink380, d.ValidatedLink390 = false, false
	d.ResponseBytes, d.ExpectedFirmware = 1, profile.Firmware
	d.Writable = d.Writable && catalogAllowsBoolSettingWrite(d, profile, property)
	d.Help = "Changing this may restart the dongle. Based on Jabra's model data; not tested on hardware."
	return d
}

func isLink390ConfigBool(device *jabra_DeviceInfo, d boolSettingDefinition) bool {
	canonical, ok := link390ConfigDefinitionForKey(d.Key)
	return ok && d.Key == "softphone-integration" && d.Link390Config && d.Writable &&
		exactDongleSettingProfile(device) && d.ExpectedFirmware == device.firmwareVersion &&
		d.Scope == settingScopeDongle && d.NeedsConfigMode && d.Class == canonical.Class && d.Op == canonical.Op &&
		d.Destination == 0 && len(d.Request) == 0 && len(d.WritePrefix) == 0 && d.ResponseIndex == 0 && d.ResponseBytes == 1 &&
		!d.Invert && !d.HasRawValues && !d.PreservePayload && d.BitMask == 0
}

func isLink390ConfigChoice(device *jabra_DeviceInfo, d choiceSettingDefinition) bool {
	canonical, ok := link390ConfigDefinitionForKey(d.Key)
	return ok && d.Key == "wireless-range" && d.Link390Config && d.Writable && d.DongleExactProfile &&
		exactDongleSettingProfile(device) && d.ExpectedFirmware == device.firmwareVersion &&
		d.Scope == settingScopeDongle && d.NeedsConfigMode && d.Class == canonical.Class && d.Op == canonical.Op &&
		d.Destination == 0 && len(d.Request) == 0 && len(d.WritePrefix) == 0 && d.ResponseIndex == 0 && d.ResponseBytes == 1 &&
		len(d.ResponsePrefix) == 0 && d.Codec == nil && !d.PreservePayload && d.BitMask == 0
}

func settingUsesLink390Config(setting deviceSettingValue) bool {
	return setting.Boolean != nil && setting.Boolean.Definition.Link390Config ||
		setting.Choice != nil && setting.Choice.Definition.Link390Config
}

// All public setters and low-level compatibility calls reach this one verified
// flow. Return its readback directly instead of doing a PID-based refresh.
func applyLink390ConfigFromText(device *jabra_DeviceInfo, setting deviceSettingValue, text string) (applied link390AppliedSetting, actionErr error) {
	if !setting.editable() {
		return applied, ErrNotSupported
	}
	definition, known := link390ConfigDefinitionForKey(setting.key())
	if !known {
		return applied, ErrNotSupported
	}
	var wanted, previous byte
	if setting.Boolean != nil {
		if !isLink390ConfigBool(device, setting.Boolean.Definition) {
			return applied, ErrNotSupported
		}
		value, err := parseOnOff(text)
		if err != nil {
			return applied, err
		}
		if value {
			wanted = 1
		}
		if setting.Boolean.Value {
			previous = 1
		}
	} else if setting.Choice != nil {
		if !isLink390ConfigChoice(device, setting.Choice.Definition) {
			return applied, ErrNotSupported
		}
		index, ok := findChoiceIndex(setting.Choice.Definition, text)
		if !ok {
			return applied, fmt.Errorf("choose one of: %s", choiceNames(setting.Choice.Definition.Choices))
		}
		wanted, previous = setting.Choice.Definition.Choices[index].Raw, setting.Choice.Raw
	} else {
		return applied, ErrNotSupported
	}
	if !definition.allows(wanted) {
		return applied, ErrNotSupported
	}
	entry := historyDeviceEvent(device, "settings")
	entry.Setting = setting.key()
	finish := history.Begin(entry)
	defer history.EndDeferred(finish, &actionErr)
	verified, err := changeLink390Config(device, definition, wanted, &previous)
	if err != nil {
		return applied, err
	}
	if verified.Device == nil || verified.Result.Status != link390Applied || verified.Result.ReadBack == nil {
		return applied, fmt.Errorf("configuration outcome was not verified")
	}
	if setting.Boolean != nil {
		copy := *setting.Boolean
		copy.Value = *verified.Result.ReadBack == 1
		setting.Boolean = &copy
	} else {
		value, err := decodeChoiceSettingPayload(setting.Choice.Definition, []byte{*verified.Result.ReadBack})
		if err != nil {
			return applied, err
		}
		value.Catalog, value.Provenance = setting.Choice.Catalog, setting.Choice.Provenance
		setting.Choice = &value
	}
	return link390AppliedSetting{Setting: setting, Device: verified.Device, Changed: verified.Result.Attempted}, nil
}
