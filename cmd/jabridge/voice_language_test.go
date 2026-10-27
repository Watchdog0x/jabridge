package main

import (
	"strings"
	"testing"
)

func TestVoiceLanguageDisplayIsReadOnlyAndUsesFullLCID(t *testing.T) {
	for _, test := range []struct {
		payload []byte
		name    string
	}{{[]byte{9, 4}, "English (US)"}, {[]byte{7, 4}, "German"}, {[]byte{4, 8}, "Chinese (Simplified)"}, {[]byte{0xff, 0xff}, "Unknown (65535)"}} {
		value, err := voiceLanguageSettingFromPayload(0x2549, test.payload)
		if err != nil || value.valueName() != test.name || value.editable() || !strings.Contains(value.help(), "Firmware") {
			t.Fatal(value, err)
		}
		info := ipcSettingInfo("headset", value)
		if info.Editable || info.Value != test.name || info.Help == "" {
			t.Fatal(info)
		}
		if _, _, err := applyDeviceSettingFromText(nil, value, "German"); err == nil {
			t.Fatal("language setting used a direct write instead of the firmware path")
		}
	}
	for _, payload := range [][]byte{nil, {9}, {9, 4, 0}} {
		if _, err := voiceLanguageSettingFromPayload(0x2549, payload); err == nil {
			t.Fatal("invalid LCID payload accepted")
		}
	}
}
