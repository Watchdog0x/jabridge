package main

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

type futureSettingCodec struct{ choicePayloadCodec }

func TestImplementedSettingsNoLongerNeedPreviewEnvironment(t *testing.T) {
	for _, value := range []string{"", "junk", "I_ACCEPT_UNTESTED_SETTINGS"} {
		t.Setenv("JABRIDGE_ENABLE_PREVIEW_SETTING_WRITES", value)
		t.Setenv(experimentalWritesEnv, experimentalWritesAck)
		var keys []string
		for _, definition := range choiceSettingDefinitions(settingScopeHeadset) {
			if definition.Codec != nil {
				if !choiceWriteImplemented(definition) || !applyChoiceWritePolicy(definition).Writable {
					t.Fatal("implemented control still needs an environment unlock", definition.Key)
				}
				keys = append(keys, definition.Key)
			}
		}
		power, ok := findBoolSettingDefinition(settingScopeHeadset, "power-saving")
		if !ok || !canEditBoolSetting(&jabra_DeviceInfo{}, power) {
			t.Fatal("implemented power saving still needs an environment unlock")
		}
		keys = append(keys, power.Key)
		slices.Sort(keys)
		want := []string{"equalizer-1khz", "equalizer-250hz", "equalizer-4khz", "equalizer-60hz", "equalizer-7.6khz", "noise-cycle", "power-saving"}
		if !slices.Equal(keys, want) {
			t.Fatal("implemented control inventory changed", keys)
		}
		if choiceWriteImplemented(choiceSettingDefinition{Codec: futureSettingCodec{}}) || implementedSettingCodec(&noiseCycleCodec{}) {
			t.Fatal("a flag or a new codec type granted write support")
		}
	}
}

func TestUnknownCodecCannotWriteOrNoOpBeforeLease(t *testing.T) {
	state := t.TempDir()
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", state)
	t.Setenv("JABRIDGE_HISTORY", "off")
	codec, choices, err := (noiseCycleCodec{}).BindProperty(noiseCyclePropertyForTest())
	if err != nil {
		t.Fatal(err)
	}
	definition := choiceSettingDefinition{Key: "future-control", Codec: futureSettingCodec{codec}, Writable: true, Choices: choices, ExpectedFirmware: "1.0.1", Help: "Adjust this control"}
	locked := applyChoiceWritePolicy(definition)
	read, err := decodeChoiceSettingPayload(locked, []byte{3, 2, 1, 0})
	if err != nil || read.Editable || !strings.Contains(locked.Help, "not implemented") {
		t.Fatal("snapshot exposed an unknown writer", read, err)
	}
	bound, ok := choiceSettingForCatalog(definition, noiseCyclePropertyForTest(), true)
	if !ok || bound.Writable {
		t.Fatal("binding an unknown wrapper promoted its returned codec")
	}
	profile := cycleProfileForTest()
	property := noiseCyclePropertyForTest()
	property.Help = "Adjust this control"
	profile.Properties = map[string]modelcatalog.Property{"ancSoundModeLoop": property}
	wrapped := headsetChoiceSettingDefinitions[0]
	wrapped.Codec = futureSettingCodec{codec}
	values := readChoiceSettingsSnapshot(nil, []choiceSettingDefinition{wrapped}, profile, nil, func(*jabra_DeviceInfo, choiceSettingDefinition) ([]byte, error) {
		return []byte{2, 2, 1}, nil
	})
	if len(values) != 1 || values[0].Editable || values[0].Catalog.Help != "" || !strings.Contains(values[0].Provenance.Reason, "not implemented") {
		t.Fatal("a bound snapshot promoted an unimplemented wrapper", values)
	}
	read.Definition, read.Editable = definition, true // Forged internal permission.
	if err := writeChoiceSetting(nil, read, 0); !errors.Is(err, ErrNotSupported) {
		t.Fatal("unknown writer reached a no-op or device access", err)
	}
	if _, _, err := applyDeviceSettingFromText(nil, deviceSettingValue{Choice: &read}, choices[0].CatalogValue); !errors.Is(err, ErrNotSupported) {
		t.Fatal("CLI accepted an unknown writer for an unchanged value", err)
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatal("unknown writer acquired a lease", entries, err)
	}
}

func TestReadOnlyReasonsAndExactFirmwareStayRequired(t *testing.T) {
	profile := cycleProfileForTest()
	property := modelcatalog.Property{Kind: "Enum", PossibleValues: []string{"true", "false"}}
	power, _ := findBoolSettingDefinition(settingScopeHeadset, "power-saving")
	for _, test := range []struct {
		name, reason string
		change       func(*modelcatalog.Capabilities, *modelcatalog.Property)
	}{
		{"fallback", "exact firmware", func(p *modelcatalog.Capabilities, _ *modelcatalog.Property) { p.ExactFirmwareProfile = false }},
		{"unread firmware", "exact firmware", func(p *modelcatalog.Capabilities, _ *modelcatalog.Property) { p.DeviceFirmware = "" }},
		{"vendor read only", "marks this control read-only", func(_ *modelcatalog.Capabilities, p *modelcatalog.Property) { p.Access = "ReadOnly" }},
		{"unknown access", "marks this control read-only", func(_ *modelcatalog.Capabilities, p *modelcatalog.Property) { p.Access = "Unknown" }},
		{"not boolean", "on/off", func(_ *modelcatalog.Capabilities, p *modelcatalog.Property) { p.PossibleValues = []string{"yes", "no"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, prop := *profile, property
			test.change(&p, &prop)
			if catalogAllowsBoolSettingWrite(power, &p, prop) {
				t.Fatal("ineligible model enabled power saving")
			}
			if reason := boolSettingWriteReason(nil, power, true, &p, prop); !strings.Contains(reason, test.reason) {
				t.Fatal("missing reason", reason)
			}
		})
	}
	definition := equalizerDefinitionForTest(t, 0)
	definition.Writable = false
	reason := choiceSettingWriteReason(definition, true, &modelcatalog.Capabilities{ExactFirmwareProfile: true}, modelcatalog.Property{})
	if !strings.Contains(reason, "exact firmware") {
		t.Fatal("unread structured firmware had no reason", reason)
	}
	loaded, err := decodeChoiceSettingPayload(definition, equalizerBytes(t, equalizerFlatReply))
	if err != nil {
		t.Fatal(err)
	}
	loaded.Definition.Help = ""
	loaded.Provenance = observedSetting(true, false, false, reason)
	info := ipcSettingInfo("headset", deviceSettingValue{Choice: &loaded})
	if info.Editable || strings.Contains(info.Help, "Adjust") || !strings.Contains(info.Help, "exact firmware") {
		t.Fatal("read-only row advertised an adjustment", info)
	}
}
