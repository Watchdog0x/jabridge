package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/internal/history"
)

func TestSpeakVoiceGuidanceLabelsPreserveDeviceValues(t *testing.T) {
	definition := choiceSettingDefinition{Key: "voice-prompts", Label: "Voice prompts", Choices: []settingChoice{
		{Name: "Tones", Raw: 0, CatalogValue: "tones"},
		{Name: "Voice", Raw: 1, CatalogValue: "voice"},
	}}
	for _, pid := range []uint16{0x0420, 0x0422} {
		shown := presentVoiceGuidance(&jabra_DeviceInfo{productID: pid}, definition)
		if shown.Label != "Voice guidance" || shown.Help == "" || shown.Choices[0].Name != "Off" || shown.Choices[1].Name != "On" {
			t.Fatal(shown)
		}
		for input, wanted := range map[string]byte{"off": 0, "tones": 0, "on": 1, "voice": 1} {
			index, ok := findChoiceIndex(shown, input)
			if !ok || shown.Choices[index].Raw != wanted {
				t.Fatalf("%q did not map to %d", input, wanted)
			}
		}
		if definition.Choices[0].Name != "Tones" {
			t.Fatal("shared definition was mutated")
		}
	}
	other := presentVoiceGuidance(&jabra_DeviceInfo{productID: 0x24b7}, definition)
	if other.Label != "Voice prompts" || other.Choices[0].Name != "Tones" {
		t.Fatal("applied Speak-specific wording to another model")
	}
	definition.Choices = append(definition.Choices, settingChoice{Name: "Off", Raw: 2, CatalogValue: "off"})
	three := presentVoiceGuidance(&jabra_DeviceInfo{productID: 0x0422}, definition)
	index, ok := findChoiceIndex(three, "off")
	if !ok || three.Choices[index].Raw != 2 || three.Label != "Voice prompts" {
		t.Fatal("three-choice controls lost their distinct Off value")
	}
}

func TestSettingEvidenceDoesNotInferBehaviorOrCombineSessions(t *testing.T) {
	base := history.Event{Time: time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC), Session: "a", Operation: 1, USBProduct: 0x0422, Setting: "voice-prompts"}
	ack := base
	ack.Action, ack.Phase = "setting-ack", "ok"
	read := base
	read.Action, read.Phase, read.Error = "setting-readback", "error", "readback-mismatch"
	another := base
	another.Session, another.Action, another.Phase = "b", "setting-readback", "ok"
	var out bytes.Buffer
	writeSettingEvidenceSummary(&out, []history.Event{ack, read, another})
	text := out.String()
	for _, want := range []string{"device ACK=PASS; matching readback=FAIL (readback-mismatch)", "device ACK=NOT OBSERVED; matching readback=PASS", "Persistence after restart: NOT TESTED", "Physical behavior: NOT TESTED"} {
		if !strings.Contains(text, want) {
			t.Fatal(text)
		}
	}
	var old bytes.Buffer
	base.Action, base.Phase = "settings", "ok"
	writeSettingEvidenceSummary(&old, []history.Event{base})
	if !strings.Contains(old.String(), "No staged write evidence") || strings.Contains(old.String(), "ACK=PASS") {
		t.Fatal("invented evidence for an older log")
	}
}

func TestSettingEvidenceSummaryIsBounded(t *testing.T) {
	var events []history.Event
	for i := range 30 {
		events = append(events, history.Event{Session: "a", Operation: uint64(i + 1), USBProduct: 0x0422, Setting: "voice-prompts", Action: "setting-ack", Phase: "ok"})
	}
	var out bytes.Buffer
	writeSettingEvidenceSummary(&out, events)
	if strings.Count(out.String(), "device ACK=") != 12 {
		t.Fatal(out.String())
	}
}

func TestAdditionalSettingsUseMatchingPropertiesAndEncodings(t *testing.T) {
	for _, test := range []struct {
		key, property string
		op, address   byte
		empty         bool
	}{{"device-name", "deviceName", 0x5b, 0, false}, {"controller-name", "controllerName", 0x5b, 3, false}, {"speed-dial-2", "speedDialNumber2", 0x8b, 0, true}} {
		found := false
		for _, definition := range headsetTextSettingDefinitions {
			if definition.Key != test.key {
				continue
			}
			found = true
			if definition.Class != 0x13 || definition.Op != test.op || definition.Destination != test.address || !definition.LengthPrefixed || definition.AllowEmpty != test.empty || len(definition.CatalogProperties) != 1 || definition.CatalogProperties[0] != test.property {
				t.Fatal(definition)
			}
			payload, err := encodeSettingText(definition, "Test")
			if err != nil || !bytes.Equal(payload, []byte{4, 'T', 'e', 's', 't'}) {
				t.Fatal(payload, err)
			}
			if got, err := decodeSettingText(definition, payload); err != nil || got != "Test" {
				t.Fatal(got, err)
			}
		}
		if !found {
			t.Fatal("missing definition", test.key)
		}
	}
	softphone, ok := findBoolSettingDefinition(settingScopeHeadset, "softphone-integration")
	if !ok || softphone.Op != 0x4c || !softphone.NeedsConfigMode {
		t.Fatal(softphone, ok)
	}
	for _, definition := range choiceSettingDefinitions(settingScopeHeadset) {
		if definition.Key != "intellitone-level" {
			continue
		}
		if definition.Class != 0x13 || definition.Op != 0x26 {
			t.Fatal(definition)
		}
		choices := choicesAllowedByCatalog(definition.Choices, []string{"level79", "level82", "level85"})
		if len(choices) != 3 || choices[0].Raw != 79 || choices[1].Raw != 82 || choices[2].Raw != 85 {
			t.Fatal(choices)
		}
		return
	}
	t.Fatal("missing IntelliTone definition")
}
