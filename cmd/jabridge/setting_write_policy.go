package main

import "github.com/Watchdog0x/jabridge/internal/modelcatalog"

const settingWriteNotImplemented = "Editing this control is not implemented yet."

// A new structured codec needs an explicit policy decision before it can write.
func implementedSettingCodec(codec choicePayloadCodec) bool {
	switch codec.(type) {
	case noiseCycleCodec, equalizerBandCodec:
		return true
	default:
		return false
	}
}

func choiceWriteImplemented(definition choiceSettingDefinition) bool {
	return definition.Codec == nil || implementedSettingCodec(definition.Codec)
}

func applyChoiceWritePolicy(definition choiceSettingDefinition) choiceSettingDefinition {
	if !choiceWriteImplemented(definition) {
		definition.Writable = false
		definition.Help = settingWriteNotImplemented
	}
	return definition
}

func exactSettingProfile(capabilities *modelcatalog.Capabilities) bool {
	return capabilities != nil && capabilities.ExactFirmwareProfile && capabilities.DeviceFirmware != ""
}

var link390SettingProfiles = []struct {
	pid               uint16
	variant, firmware string
}{
	{0x2e56, "04-10", "3.2.0"},
	{0x2e57, "04-10", "3.2.0"},
}

func exactDongleSettingProfile(device *jabra_DeviceInfo) bool {
	if device == nil || !device.isDongle || device.instance == "" || device.serialNumber == "" {
		return false
	}
	for _, profile := range link390SettingProfiles {
		if device.productID == profile.pid && device.variantType == profile.variant && device.firmwareVersion == profile.firmware {
			return true
		}
	}
	return false
}

func matchingDongleSettingModel(device *jabra_DeviceInfo, capabilities *modelcatalog.Capabilities) bool {
	return exactDongleSettingProfile(device) && exactSettingProfile(capabilities) &&
		capabilities.PID == device.productID && capabilities.Variant == device.variantType &&
		capabilities.Firmware == device.firmwareVersion && capabilities.DeviceFirmware == device.firmwareVersion
}

func choiceDeviceWriteAllowed(device *jabra_DeviceInfo, definition choiceSettingDefinition) bool {
	if definition.DongleExactProfile || device != nil && device.isDongle {
		return definition.DongleExactProfile && exactDongleSettingProfile(device)
	}
	return true
}

func modelSettingWriteReason(property modelcatalog.Property, capabilities *modelcatalog.Capabilities, requiresExact bool) string {
	if !catalogAllowsSettingWrite(property) {
		return "Jabra's model data marks this control read-only."
	}
	if requiresExact && !exactSettingProfile(capabilities) {
		return "Editing needs Jabra's model data for this exact firmware."
	}
	return ""
}

func boolSettingWriteReason(device *jabra_DeviceInfo, definition boolSettingDefinition, wanted bool, capabilities *modelcatalog.Capabilities, property modelcatalog.Property) string {
	if reason := modelSettingWriteReason(property, capabilities, definition.RequireExactProfile); reason != "" {
		return reason
	}
	if definition.RequireBooleanChoices && !catalogAllowsBoolSettingWrite(definition, capabilities, property) {
		return "Jabra's model data does not describe this control as on/off."
	}
	if !wanted {
		return "Jabridge can read this control but cannot change it yet."
	}
	if device != nil && device.isDongle && !canEditBoolSetting(device, definition) {
		return "Changing this is not enabled on this dongle."
	}
	return ""
}

func choiceSettingWriteReason(definition choiceSettingDefinition, wanted bool, capabilities *modelcatalog.Capabilities, property modelcatalog.Property) string {
	if reason := modelSettingWriteReason(property, capabilities, definition.Codec != nil || definition.DongleExactProfile); reason != "" {
		return reason
	}
	if !choiceWriteImplemented(definition) {
		return settingWriteNotImplemented
	}
	if !wanted {
		return "Jabridge can read this control but cannot change it yet."
	}
	return ""
}

func choiceHasChange(value choiceSettingValue) bool {
	for _, choice := range value.Definition.Choices {
		if choice.Raw != value.Raw {
			return true
		}
	}
	return false
}
