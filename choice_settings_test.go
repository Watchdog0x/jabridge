package main

import "testing"

func TestSignedRangeChoices(t *testing.T) {
	choices := signedRangeChoices(-2, 1, " dB")
	if len(choices) != 4 {
		t.Fatalf("choice count = %d", len(choices))
	}
	if choices[0].Name != "-2 dB" || choices[0].Raw != 0xfe {
		t.Fatalf("first choice = %#v", choices[0])
	}
	if choices[3].Name != "1 dB" || choices[3].Raw != 1 {
		t.Fatalf("last choice = %#v", choices[3])
	}
}

func TestSidetoneLevelsMatchPublishedModelChoices(t *testing.T) {
	choices := sidetoneLevelChoices()
	want := []string{"-9 dB", "-6 dB", "-3 dB", "0 dB", "3 dB", "6 dB"}
	if len(choices) != len(want) {
		t.Fatalf("sidetone choices = %d, want %d", len(choices), len(want))
	}
	for index := range want {
		if choices[index].Name != want[index] {
			t.Fatalf("sidetone choice %d = %q, want %q", index, choices[index].Name, want[index])
		}
	}
}

func TestCatalogLimitsButtonChoicesForExactModel(t *testing.T) {
	allowed := []string{"pushToTalk", "callHandling", "busylight", "noFunction", "speedDial", "mute"}
	choices := choicesAllowedByCatalog(commonButtonFunctions, allowed)
	if len(choices) != len(allowed) {
		t.Fatalf("filtered choices = %d, want %d", len(choices), len(allowed))
	}
	for _, choice := range choices {
		if choice.Name == "Microsoft Teams" || choice.Name == "Music" || choice.Name == "Headset busylight" {
			t.Fatalf("catalog-excluded choice remained: %q", choice.Name)
		}
	}
}

func TestButtonFunctionDefinitions(t *testing.T) {
	wantIDs := map[string]byte{
		"call-button":      0,
		"mute-button":      8,
		"three-dot-button": 18,
		"four-dot-button":  19,
	}
	for _, definition := range headsetChoiceSettingDefinitions {
		want, exists := wantIDs[definition.Key]
		if !exists {
			continue
		}
		if len(definition.Request) != 1 || definition.Request[0] != want {
			t.Errorf("%s request = %v, want button %d", definition.Key, definition.Request, want)
		}
		if !definition.NeedsConfigMode || definition.Op != 0x8a {
			t.Errorf("%s does not use guarded button configuration: %#v", definition.Key, definition)
		}
		delete(wantIDs, definition.Key)
	}
	if len(wantIDs) != 0 {
		t.Fatalf("missing button definitions: %v", wantIDs)
	}
}

func TestChoiceCyclingAndLookup(t *testing.T) {
	definition := choiceSettingDefinition{
		Key:     "test",
		Choices: []settingChoice{{Name: "One", Raw: 1}, {Name: "Two", Raw: 2}, {Name: "Three", Raw: 3}},
	}
	setting := choiceSettingValue{Definition: definition, ChoiceIndex: 0, Raw: 1, Editable: true}
	if got, err := nextChoiceIndex(setting); err != nil || got != 1 {
		t.Fatalf("next choice = %d, %v", got, err)
	}
	setting.ChoiceIndex = 1
	if got, err := nextChoiceIndex(setting); err != nil || got != 2 {
		t.Fatalf("third choice = %d, %v", got, err)
	}
	setting.ChoiceIndex = 2
	if got, err := nextChoiceIndex(setting); err != nil || got != 0 {
		t.Fatalf("wrapped choice = %d, %v", got, err)
	}
	if got, ok := findChoiceIndex(definition, "two"); !ok || got != 1 {
		t.Fatalf("choice lookup = %d, %v", got, ok)
	}
	spaced := choiceSettingDefinition{Choices: []settingChoice{{Name: "Call handling"}, {Name: "-9 dB"}}}
	if got, ok := findChoiceIndex(spaced, "call-handling"); !ok || got != 0 {
		t.Fatalf("hyphenated choice lookup = %d, %v", got, ok)
	}
	if got, ok := findChoiceIndex(spaced, "-9-db"); !ok || got != 1 {
		t.Fatalf("unit choice lookup = %d, %v", got, ok)
	}
}

func TestConfigModeChoicesBecomeEditableAfterDiscovery(t *testing.T) {
	for _, definition := range headsetChoiceSettingDefinitions {
		if !definition.NeedsConfigMode {
			continue
		}
		setting := choiceSettingValue{Definition: definition, ChoiceIndex: 0, Raw: definition.Choices[0].Raw, Editable: definition.Writable}
		if !setting.Editable {
			t.Fatalf("config-mode setting %s is not editable after capability discovery", definition.Key)
		}
	}
}

func TestDeviceSettingValueFormatting(t *testing.T) {
	choice := choiceSettingValue{
		Definition:  choiceSettingDefinition{Key: "voice", Label: "Voice prompts", Choices: []settingChoice{{Name: "Voice", Raw: 1}}},
		ChoiceIndex: 0,
		Raw:         1,
		Editable:    true,
	}
	setting := deviceSettingValue{Choice: &choice}
	if got := formatDeviceSetting(setting); got != "[VOICE  ] Voice prompts" {
		t.Fatalf("formatted choice = %q", got)
	}
}
