package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/completion"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func TestNoiseCycleSettingIsSeparateFromNoiseControl(t *testing.T) {
	for _, definition := range choiceSettingDefinitions(settingScopeHeadset) {
		if definition.Key == "noise-cycle" {
			if definition.Class != gnpClassConfig || definition.Op != 0xbf || len(definition.CatalogProperties) != 1 || definition.CatalogProperties[0] != "ancSoundModeLoop" {
				t.Fatalf("wrong noise button cycle command: %+v", definition)
			}
			return
		}
	}
	t.Fatal("noise control button cycle is missing")
}

func TestNoiseCycleBashCompletion(t *testing.T) {
	values := []string{"off", "hearthrough", "anc", "off,hearthrough", "off,anc", "hearthrough,anc", "off,hearthrough,anc"}
	for _, test := range []struct {
		words string
		index string
		want  []string
	}{
		{"jabridge settings set headset.noise-cyc", "3", []string{"headset.noise-cycle"}},
		{"jabridge ipc set headset noise-cyc", "4", []string{"noise-cycle"}},
		{"jabridge settings set headset.noise-cycle ''", "4", values},
		{"jabridge ipc set headset noise-cycle ''", "5", values},
	} {
		t.Run(test.words, func(t *testing.T) {
			cmd := exec.Command("bash", "--noprofile", "--norc")
			cmd.Stdin = strings.NewReader(completion.JabridgeBash + "\nCOMP_WORDS=(" + test.words + ")\nCOMP_CWORD=" + test.index + "\n_jabridge_completion\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n")
			output, err := cmd.CombinedOutput()
			if err != nil || !slices.Equal(strings.Fields(string(output)), test.want) {
				t.Fatalf("completion = %s, %v; want %v", output, err, test.want)
			}
		})
	}
}

func noiseCyclePropertyForTest() modelcatalog.Property {
	var members []modelcatalog.CompositeSetting
	for _, mode := range []struct{ id, value string }{
		{"OFF", "off"}, {"HEARTHROUGH", "hearThrough"}, {"ANC", "anc"},
	} {
		fixed := false
		members = append(members, modelcatalog.CompositeSetting{
			ID: "ANC_SOUND_MODE_LOOP_" + mode.id, Kind: "Enum", GetPath: "/-", SetPath: "/-", IsStatic: &fixed,
			Choices: []modelcatalog.PropertyChoice{{Key: "True", Value: mode.value}, {Key: "False", Value: ""}},
		})
	}
	return modelcatalog.Property{Name: "ancSoundModeLoop", CompositeSettings: members}
}

func TestNoiseCyclePayloadKeepsOrderAndRequiresExactReadback(t *testing.T) {
	codec, choices, err := (noiseCycleCodec{}).BindProperty(noiseCyclePropertyForTest())
	if err != nil || len(choices) != 7 {
		t.Fatal(err, choices)
	}
	for _, invalid := range [][]byte{nil, {0}, {1}, {2, 0}, {1, 3}, {2, 0, 0}, {4, 0, 1, 2, 0}} {
		if _, _, err := codec.Decode(invalid); err == nil {
			t.Fatalf("accepted malformed noise button cycle %x", invalid)
		}
	}
	for _, test := range []struct {
		current []byte
		wanted  byte
		payload []byte
	}{
		{[]byte{2, 2, 1}, 7, []byte{3, 2, 1, 0}},
		{[]byte{3, 2, 1, 0}, 5, []byte{2, 2, 0}},
		{[]byte{3, 2, 1, 0}, 2, []byte{1, 1}},
		{[]byte{1, 2}, 7, []byte{3, 2, 0, 1}},
	} {
		got, err := codec.Encode(test.current, test.wanted)
		if err != nil || !bytes.Equal(got, test.payload) {
			t.Fatalf("encode %x to set %d = %x, %v; want %x", test.current, test.wanted, got, err, test.payload)
		}
		id, _, err := codec.Decode(got)
		if err != nil || id != test.wanted || codec.Verify(got, got) != nil {
			t.Fatal("cycle did not round trip", id, err)
		}
	}
	if _, err := codec.Encode([]byte{1, 2}, 0); err == nil {
		t.Fatal("accepted an empty cycle")
	}
	if _, err := codec.Encode([]byte{1, 2}, 8); err == nil {
		t.Fatal("accepted an unknown cycle")
	}
	if err := codec.Verify([]byte{3, 2, 1, 0}, []byte{3, 0, 1, 2}); err == nil {
		t.Fatal("accepted a reordered readback with the same modes")
	}
}

func TestNoiseCycleCatalogAndClientGuard(t *testing.T) {
	var original choiceSettingDefinition
	for _, definition := range headsetChoiceSettingDefinitions {
		if definition.Key == "noise-cycle" {
			original = definition
		}
	}
	property := noiseCyclePropertyForTest()
	bound, supported := choiceSettingForCatalog(original, property, true)
	if !supported || !bound.Writable || len(bound.Choices) != 7 {
		t.Fatal("exact complete profile did not bind", supported, bound)
	}
	for _, input := range []string{"anc,hearthrough,off", "Off, ANC, HearThrough"} {
		index, ok := findChoiceIndex(bound, input)
		if !ok || bound.Choices[index].Raw != 7 {
			t.Fatal("CLI did not resolve the requested cycle", input)
		}
	}
	for _, input := range []string{"", "anc,anc", "anc,other", "anc,"} {
		if _, ok := findChoiceIndex(bound, input); ok {
			t.Fatal("accepted invalid cycle", input)
		}
	}
	read, err := decodeChoiceSettingPayload(bound, []byte{3, 2, 1, 0})
	if err != nil {
		t.Fatal(err)
	}
	info := ipcSettingInfo("headset", deviceSettingValue{Choice: &read})
	if info.Kind != "choice" || info.Key != "noise-cycle" || len(info.Choices) != 7 || info.Value != "Off, HearThrough, ANC" || !info.Editable {
		t.Fatal("incorrect TUI/IPC control", info)
	}
	readonly, supported := choiceSettingForCatalog(original, property, false)
	if !supported || readonly.Writable {
		t.Fatal("fallback firmware profile enabled a cycle write")
	}
	property.Access = "ReadOnly"
	readonly, supported = choiceSettingForCatalog(original, property, true)
	if !supported || readonly.Writable {
		t.Fatal("read-only catalog enabled a cycle write")
	}
	property = noiseCyclePropertyForTest()
	property.CompositeSettings = property.CompositeSettings[:2]
	subset, supported := choiceSettingForCatalog(original, property, true)
	if !supported || len(subset.Choices) != 3 {
		t.Fatal("supported mode subset was lost")
	}
	if _, ok := findChoiceIndex(subset, "anc"); ok {
		t.Fatal("offered a mode excluded by this model")
	}
	for _, damage := range []func(*modelcatalog.Property){
		func(p *modelcatalog.Property) { p.CompositeSettings[0].IsStatic = nil },
		func(p *modelcatalog.Property) { p.CompositeSettings[0].ID = "" },
		func(p *modelcatalog.Property) { p.CompositeSettings[0].SetPath = "" },
		func(p *modelcatalog.Property) { p.CompositeSettings[0].Choices[0].Value = "<invalid>" },
		func(p *modelcatalog.Property) { p.CompositeSettings[1] = p.CompositeSettings[0] },
	} {
		// A JSON round trip gives each case independent slices and pointers.
		data, err := json.Marshal(noiseCyclePropertyForTest())
		if err != nil {
			t.Fatal(err)
		}
		var broken modelcatalog.Property
		if err := json.Unmarshal(data, &broken); err != nil {
			t.Fatal(err)
		}
		damage(&broken)
		if _, supported := choiceSettingForCatalog(original, broken, true); supported {
			t.Fatal("accepted incomplete or ambiguous cycle metadata")
		}
	}
}
