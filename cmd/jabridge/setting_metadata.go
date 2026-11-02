package main

import (
	"strings"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

// Descriptions inform the UI. They never grant writes or configure transport.
type settingCatalogMetadata struct {
	Help, Group string
	Restart     *bool
}

func bindSettingCatalogMetadata(device *jabra_DeviceInfo, key string, destination byte, property modelcatalog.Property, capabilities *modelcatalog.Capabilities) settingCatalogMetadata {
	if capabilities == nil || settingPartRole(device, key, destination) == "controller" {
		return settingCatalogMetadata{}
	}
	metadata := settingCatalogMetadata{Group: property.Group}
	// Metadata does not authorize future structured write implementations.
	if len(property.CompositeSettings) == 0 || key == "noise-cycle" || isEqualizerBand(key) {
		metadata.Help = property.Help
	}
	if property.RestartKnown && (property.RequiresRestart || capabilities.ExactFirmwareProfile && capabilities.DeviceFirmware != "") {
		restart := property.RequiresRestart
		metadata.Restart = &restart
	}
	return metadata
}

func (setting deviceSettingValue) catalogMetadata() settingCatalogMetadata {
	if setting.Boolean != nil {
		return setting.Boolean.Catalog
	}
	if setting.Choice != nil {
		return setting.Choice.Catalog
	}
	if setting.Text != nil {
		return setting.Text.Catalog
	}
	if setting.Remote != nil {
		return settingCatalogMetadata{Group: setting.Remote.Group, Restart: setting.Remote.RequiresRestart}
	}
	return settingCatalogMetadata{}
}

func (setting deviceSettingValue) group() string {
	return setting.catalogMetadata().Group
}

func (setting deviceSettingValue) requiresRestart() *bool {
	return setting.catalogMetadata().Restart
}

func (setting deviceSettingValue) restartNotice() string {
	if setting.needsConfigMode() {
		return "Saving may restart this device. Do not edit during a call."
	}
	if restart := setting.requiresRestart(); restart != nil && *restart {
		return "Jabra's model data says this change restarts the device."
	}
	return ""
}

func (setting deviceSettingValue) usesEditor() bool {
	restart := setting.requiresRestart()
	return settingIsText(setting) || len(settingChoices(setting)) > 2 || setting.needsConfigMode() || restart != nil && *restart
}

func (setting deviceSettingValue) detailLine() string {
	help := setting.help()
	if group := setting.group(); group != "" {
		if support := setting.support(); support != nil && support.Reason != "" && strings.HasPrefix(help, support.Reason) {
			return help + " (" + group + ")"
		}
		return strings.TrimSpace(group + ": " + help)
	}
	return help
}
