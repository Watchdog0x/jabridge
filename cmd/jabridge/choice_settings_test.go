package main

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/completion"
)

// Issue #56 reports these durations from Jabra Plus. The exact Evolve3 65
// profile maps sleep1..sleep7 to 3, 6, 12, 24, 48, 72, 96 respectively.
func TestAutoSleepDurationsAndCLIValues(t *testing.T) {
	definition := autoSleepDefinitionForTest(t)
	want := []settingChoice{
		{Name: "Never", Raw: 0, CatalogValue: "0"},
		{Name: "15 min", Raw: 3, CatalogValue: "3"},
		{Name: "30 min", Raw: 6, CatalogValue: "6"},
		{Name: "1 hour", Raw: 12, CatalogValue: "12"},
		{Name: "2 hours", Raw: 24, CatalogValue: "24"},
		{Name: "4 hours", Raw: 48, CatalogValue: "48"},
		{Name: "6 hours", Raw: 72, CatalogValue: "72"},
		{Name: "8 hours", Raw: 96, CatalogValue: "96"},
	}
	if len(definition.Choices) != len(want) {
		t.Fatalf("auto-sleep choices = %v, want %v", definition.Choices, want)
	}
	for index, expected := range want {
		t.Run(expected.Name, func(t *testing.T) {
			if got := definition.Choices[index]; got != expected {
				t.Errorf("choice = %#v, want %#v", got, expected)
			}
			value := choiceSettingValue{Definition: definition, ChoiceIndex: index, Raw: expected.Raw}
			if got := choiceSettingName(value); got != expected.Name {
				t.Errorf("shown duration for raw %d = %q, want %q", expected.Raw, got, expected.Name)
			}
			for _, input := range []string{expected.Name, choiceValueToken(expected.Name), expected.CatalogValue} {
				got, ok := findChoiceIndex(definition, input)
				if !ok || definition.Choices[got].Raw != expected.Raw {
					t.Errorf("%q did not select raw %d", input, expected.Raw)
				}
			}
		})
	}
	for _, invalid := range []string{"12-hours", "16-hours"} {
		if _, ok := findChoiceIndex(definition, invalid); ok {
			t.Errorf("accepted old incorrect duration %q", invalid)
		}
	}
}

func TestAutoSleepCatalogProfilesKeepRawValues(t *testing.T) {
	definition := autoSleepDefinitionForTest(t)
	// Official AUTO_SLEEP profiles: Evolve3 65 2549/01-A3/1.0.1,
	// Evolve2 75, Evolve2 85 24bb/01-67/1.5.7, and Evolve3 85.
	profiles := map[string][]string{
		"Evolve3 65": {"3", "6", "12", "24", "48", "72", "96", "0"},
		"Evolve2 75": {"3", "96", "6", "24", "48", "72", "12", "0"},
		"Evolve2 85": {"72", "3", "48", "6", "0", "24", "96", "12"},
		"Evolve3 85": {"96", "24", "12", "0", "3", "72", "6", "48"},
	}
	for name, values := range profiles {
		t.Run(name, func(t *testing.T) {
			if got := choicesAllowedByCatalog(definition.Choices, values); !slices.Equal(got, definition.Choices) {
				t.Fatalf("catalog filtering changed raw choices: %v", got)
			}
		})
	}
	limited := choicesAllowedByCatalog(definition.Choices, []string{"3", "24", "255"})
	if len(limited) != 2 || limited[0].Name != "15 min" || limited[0].Raw != 3 || limited[1].Name != "2 hours" || limited[1].Raw != 24 {
		t.Fatalf("limited profile = %v; must keep only the supported 15-min and 2-hour choices", limited)
	}
	if got := choicesAllowedByCatalog(definition.Choices, nil); len(got) != 0 {
		t.Fatalf("offered choices without catalog support: %v", got)
	}
}

func TestAutoSleepBashCompletionMatchesChoices(t *testing.T) {
	want := []string{"never", "15-min", "30-min", "1-hour", "2-hours", "4-hours", "6-hours", "8-hours"}
	for _, command := range []struct {
		name  string
		words string
		index string
	}{
		{"settings", "jabridge settings set headset.auto-sleep ''", "4"},
		{"ipc", "jabridge ipc set headset auto-sleep ''", "5"},
	} {
		t.Run(command.name, func(t *testing.T) {
			script := completion.JabridgeBash + "\nCOMP_WORDS=(" + command.words + ")\nCOMP_CWORD=" + command.index + "\n_jabridge_completion\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n"
			cmd := exec.Command("bash", "--noprofile", "--norc")
			cmd.Stdin = strings.NewReader(script)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("completion: %v\n%s", err, out)
			}
			if got := strings.Fields(string(out)); !slices.Equal(got, want) {
				t.Fatalf("completion = %v, want %v", got, want)
			}
		})
	}
}

func autoSleepDefinitionForTest(t *testing.T) choiceSettingDefinition {
	t.Helper()
	for _, definition := range headsetChoiceSettingDefinitions {
		if definition.Key == "auto-sleep" {
			return definition
		}
	}
	t.Fatal("auto-sleep definition missing")
	return choiceSettingDefinition{}
}

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
	want := []string{"-9 dB", "-6 dB", "-4 dB", "-3 dB", "-2 dB", "0 dB", "2 dB", "3 dB", "4 dB", "6 dB"}
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

func TestButtonCatalogNamesMatchPropertyDefinitions(t *testing.T) {
	allowed := []string{"busylightHeadset", "msTeam"}
	choices := choicesAllowedByCatalog(commonButtonFunctions, allowed)
	if len(choices) != 2 || choices[0].Name != "Headset busylight" || choices[1].Name != "Microsoft Teams" {
		t.Fatalf("button property aliases = %#v", choices)
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
		if definition.Op == 0x27 {
			continue
		} // Separate model-filtered BUTTON_FUNCTION command.
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

func TestExpandedHeadsetChoiceCommands(t *testing.T) {
	want := map[string]byte{
		"boom-arm-action":   0x98,
		"boom-arm-guidance": 0xbc,
		"audio-protection":  0x01,
		"auto-sleep":        0x90,
		"mute-reminder":     0x1e,
		"sound-mode":        0x15,
	}
	for _, definition := range headsetChoiceSettingDefinitions {
		if operation, exists := want[definition.Key]; exists {
			if definition.Op != operation || definition.Class != gnpClassConfig {
				t.Errorf("%s = %#v", definition.Key, definition)
			}
			delete(want, definition.Key)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing expanded choices: %v", want)
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

func TestMaskedChoiceKeepsUnrelatedBoomArmBits(t *testing.T) {
	got, err := mergeSettingPayload([]byte{0x0a}, 0, 0x0d, 0x01)
	if err != nil || len(got) != 1 || got[0] != 0x03 {
		t.Fatalf("masked boom-arm value = %x, %v; want 03", got, err)
	}
	dsp, err := mergeSettingPayload([]byte{0x00, 0xfa}, 1, 0, 0x03)
	if err != nil || len(dsp) != 2 || dsp[0] != 0 || dsp[1] != 3 {
		t.Fatalf("DSP sidetone value = %x, %v; want 0003", dsp, err)
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
