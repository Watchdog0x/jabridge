package modelcatalog

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func evolve3SettingsFixture(t *testing.T) map[string]Property {
	t.Helper()
	raw, err := os.ReadFile("testdata/evolve3-65-1.0.1.json")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	properties := map[string]Property{}
	collectProperties(document, properties)
	return properties
}

func TestCompositeSettingsKeepMembersAndLanguageHandling(t *testing.T) {
	properties := evolve3SettingsFixture(t)
	// Decode through JSON so this regression can run against the old Property
	// type too: the old parser exposes the parent but loses every member.
	for _, test := range []struct {
		name  string
		count int
	}{
		{"ancSoundModeLoop", 3},
		{"equalizerConfig", 21},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(properties[test.name])
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				CompositeSettings []struct {
					ID       string
					Kind     string
					GetPath  string
					SetPath  string
					IsStatic *bool
					Choices  []struct{ Key, Value string }
				}
			}
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if len(decoded.CompositeSettings) != test.count {
				t.Fatalf("%s members = %d, want %d", test.name, len(decoded.CompositeSettings), test.count)
			}
			static, dynamic := 0, 0
			for _, member := range decoded.CompositeSettings {
				if member.ID == "" || member.Kind == "" || member.SetPath == "" || member.IsStatic == nil {
					t.Fatalf("incomplete member: %+v", member)
				}
				if *member.IsStatic {
					static++
					if len(member.Choices) != 1 || member.Choices[0].Key != "SET" || member.Choices[0].Value == "" {
						t.Fatalf("lost fixed value: %+v", member)
					}
				} else {
					dynamic++
					if member.GetPath != member.SetPath {
						t.Fatalf("lost gain/mode path: %+v", member)
					}
				}
			}
			if test.name == "equalizerConfig" && (static != 16 || dynamic != 5) {
				t.Fatalf("equalizer static/dynamic members = %d/%d, want 16/5", static, dynamic)
			}
			if test.name == "ancSoundModeLoop" && (static != 0 || dynamic != 3 || decoded.CompositeSettings[0].Choices[0].Value != "anc" || decoded.CompositeSettings[0].Choices[1].Value != "") {
				t.Fatal("noise cycle lost its on/off members or empty off value")
			}
		})
	}
	raw, err := json.Marshal(properties["currentVoiceLanguage"])
	if err != nil {
		t.Fatal(err)
	}
	var language struct{ SpecialHandling string }
	if err := json.Unmarshal(raw, &language); err != nil {
		t.Fatal(err)
	}
	if language.SpecialHandling != "OnWriteFirmwareUpdateTunesPack" {
		t.Fatal("lost the language-pack requirement", string(raw))
	}
}

func TestCompositeMetadataPreservesEveryExistingFlatProperty(t *testing.T) {
	// Captured from the unchanged parser at 11ef109, using the same official
	// fixture. Compare all legacy fields, not just the newly supported controls.
	wantBytes, err := os.ReadFile("testdata/evolve3-65-flat-properties.json")
	if err != nil {
		t.Fatal(err)
	}
	gotBytes, err := json.Marshal(evolve3SettingsFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]map[string]any
	if err := json.Unmarshal(wantBytes, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotBytes, &got); err != nil {
		t.Fatal(err)
	}
	for _, property := range got {
		delete(property, "CompositeSettings")
		delete(property, "SpecialHandling")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("composite metadata changed existing flat property names, values or guards")
	}
}
