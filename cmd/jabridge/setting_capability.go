package main

import "github.com/Watchdog0x/jabridge/daemon/ipc"

type settingSource uint8

const (
	settingSourceCatalog settingSource = 1 << iota
	settingSourceExactProfile
	settingSourceDeviceRead
	settingSourceHardwareReport
)

// A successful read proves that a control answered, not that every possible
// write works. Keep its evidence separate from the existing write gates.
type settingProvenance struct {
	Sources settingSource
	Reason  string
}

func observedSetting(catalogued, exact, qualified bool, reason string) settingProvenance {
	p := settingProvenance{Sources: settingSourceDeviceRead, Reason: reason}
	if catalogued {
		p.Sources |= settingSourceCatalog
		if exact {
			p.Sources |= settingSourceExactProfile
		}
	}
	if qualified {
		p.Sources |= settingSourceHardwareReport
	}
	return p
}

func (p settingProvenance) info(editable bool) *ipc.SettingSupport {
	if p.Sources == 0 && p.Reason == "" {
		return nil
	}
	info := &ipc.SettingSupport{State: "unavailable", Reason: p.Reason}
	if p.Sources&settingSourceDeviceRead != 0 {
		info.State = "read-only"
		if editable {
			info.State = "available"
		}
	}
	for _, source := range []struct {
		flag settingSource
		name string
	}{
		{settingSourceCatalog, "model-catalog"},
		{settingSourceExactProfile, "exact-firmware-profile"},
		{settingSourceDeviceRead, "device-read"},
		{settingSourceHardwareReport, "hardware-report"},
	} {
		if p.Sources&source.flag != 0 {
			info.Sources = append(info.Sources, source.name)
		}
	}
	return info
}

func (setting deviceSettingValue) support() *ipc.SettingSupport {
	if setting.Boolean != nil {
		return setting.Boolean.Provenance.info(setting.editable())
	}
	if setting.Choice != nil {
		return setting.Choice.Provenance.info(setting.editable())
	}
	if setting.Text != nil {
		return setting.Text.Provenance.info(setting.editable())
	}
	if setting.Remote != nil {
		return setting.Remote.Support
	}
	return nil
}
