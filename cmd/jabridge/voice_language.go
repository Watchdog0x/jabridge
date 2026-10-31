package main

import (
	"encoding/binary"
	"errors"
	"time"

	"github.com/Watchdog0x/jabridge/internal/firmware"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func readVoiceLanguageSetting(device *jabra_DeviceInfo) (deviceSettingValue, error) {
	capabilities, err := lookupDeviceModel(device)
	if err != nil {
		return deviceSettingValue{}, err
	}
	if !capabilities.ExactFirmwareProfile {
		return deviceSettingValue{}, ErrNotSupported
	}
	if _, exists := capabilities.Properties["currentVoiceLanguage"]; !exists {
		return deviceSettingValue{}, ErrNotSupported
	}
	h, destination, err := settingPartTransport(device, "voice-language", 0)
	if err != nil {
		return deviceSettingValue{}, err
	}
	defer h.close()
	payload, err := gnpQueryPayloadWithDataTimeout(h, destination, nextSeq(), gnpClassConfig, 0x74, nil, 900*time.Millisecond)
	if err != nil {
		return deviceSettingValue{}, err
	}
	return voiceLanguageSettingFromProfile(device.productID, payload, capabilities)
}

func voiceLanguageSettingFromProfile(pid uint16, payload []byte, capabilities *modelcatalog.Capabilities) (deviceSettingValue, error) {
	value, err := voiceLanguageSettingFromPayload(pid, payload)
	if err == nil {
		value.Text.Provenance = observedSetting(true, capabilities.ExactFirmwareProfile && capabilities.DeviceFirmware != "", false, "")
	}
	return value, err
}

func voiceLanguageSettingFromPayload(pid uint16, payload []byte) (deviceSettingValue, error) {
	if len(payload) != 2 {
		return deviceSettingValue{}, errors.New("voice language returned an invalid value")
	}
	help := "Voice language changes are not enabled for this model."
	if pid == 0x2549 {
		help = "Use Firmware > Voice language with the headset connected directly by USB."
	}
	definition := textSettingDefinition{Key: "voice-language", Label: "Voice prompt language", Class: gnpClassConfig, Op: 0x74, CatalogProperties: []string{"currentVoiceLanguage"}, Writable: false, Help: help}
	return deviceSettingValue{Text: &textSettingValue{Definition: definition, Value: firmware.VoiceLanguageName(binary.LittleEndian.Uint16(payload)), Editable: false}}, nil
}
