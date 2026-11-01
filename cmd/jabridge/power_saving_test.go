package main

import (
	"bytes"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/completion"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func TestPowerSavingVendorBooleanAndPayload(t *testing.T) {
	definition, ok := findBoolSettingDefinition(settingScopeHeadset, "power-saving")
	if !ok {
		t.Fatal("power saving is missing")
	}
	if definition.Class != gnpClassConfig || definition.Op != 0x1b || definition.ProbeWithoutCatalog {
		t.Fatalf("power saving has the wrong route or permits catalog-free probing: %+v", definition)
	}
	for raw := 0; raw <= 255; raw++ {
		got, err := decodeBoolSettingPayload(definition, []byte{byte(raw)})
		if err != nil || got != (raw != 255) {
			t.Fatalf("raw %d = %t, %v; only 255 means off", raw, got, err)
		}
	}
	for _, payload := range [][]byte{nil, {1, 0}} {
		if _, err := decodeBoolSettingPayload(definition, payload); err == nil {
			t.Fatalf("accepted malformed power-saving reply %x", payload)
		}
	}
	for _, test := range []struct {
		value bool
		want  []byte
	}{{true, []byte{1}}, {false, []byte{255}}} {
		if got := encodeBoolSettingPayload(definition, test.value); !bytes.Equal(got, test.want) {
			t.Fatalf("encode %t = %x, want %x", test.value, got, test.want)
		}
	}
	for _, raw := range []byte{0, 1, 2, 254, 255} {
		state, err := decodeBoolSettingPayload(definition, []byte{raw})
		if err != nil {
			t.Fatal(err)
		}
		setting := deviceSettingValue{Boolean: &boolSettingValue{Definition: definition, Value: state, Editable: true}}
		wanted := "off"
		if state {
			wanted = "on"
		}
		// A matching request must return before any device transport is opened,
		// even when the device's on value is not the canonical write value 1.
		value, changed, err := applyDeviceSettingFromText(nil, setting, wanted)
		if err != nil || changed || value != wanted {
			t.Fatalf("matching request for raw %d = %q, changed=%t, %v", raw, value, changed, err)
		}
	}
}

func TestPowerSavingClientAndCompletionValues(t *testing.T) {
	definition, ok := findBoolSettingDefinition(settingScopeHeadset, "power-saving")
	if !ok {
		t.Fatal("power saving is missing")
	}
	scope, key, err := parseSettingSelector("headset.power-saving")
	if err != nil || scope != settingScopeHeadset || key != definition.Key {
		t.Fatal("CLI selector does not select the headset's power-saving control")
	}
	setting := deviceSettingValue{Boolean: &boolSettingValue{Definition: definition, Value: true, Editable: true}}
	info := ipcSettingInfo("headset", setting)
	if info.Key != key || info.Label != "Power saving" || info.Value != "On" || info.Kind != "boolean" || !info.Editable || !slices.Equal(info.Choices, []string{"Off", "On"}) {
		t.Fatalf("IPC and menu control = %+v", info)
	}
	setting.Boolean.Editable = false
	if _, _, err := applyDeviceSettingFromText(nil, setting, "off"); err == nil {
		t.Fatal("read-only power saving accepted a write")
	}
	for _, test := range []struct {
		words string
		index string
		want  []string
	}{
		{"jabridge settings set headset.power", "3", []string{"headset.power-saving"}},
		{"jabridge ipc set headset power", "4", []string{"power-saving"}},
		{"jabridge settings set headset.power-saving ''", "4", []string{"on", "off"}},
		{"jabridge ipc set headset power-saving ''", "5", []string{"on", "off"}},
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

func TestPowerSavingRequiresExactBooleanCatalog(t *testing.T) {
	definition, ok := findBoolSettingDefinition(settingScopeHeadset, "power-saving")
	if !ok {
		t.Fatal("power saving is missing")
	}
	capabilities := &modelcatalog.Capabilities{ExactFirmwareProfile: true, DeviceFirmware: "1.0.1"}
	property := modelcatalog.Property{Kind: "Enum", PossibleValues: []string{"true", "false"}}
	if !catalogAllowsBoolSettingWrite(definition, capabilities, property) {
		t.Fatal("matching read/write profile was refused")
	}
	if catalogAllowsBoolSettingWrite(definition, nil, property) || catalogAllowsBoolSettingWrite(definition, &modelcatalog.Capabilities{}, property) {
		t.Fatal("power-saving write allowed without an exact firmware profile")
	}
	for _, values := range [][]string{nil, {"true"}, {"false"}, {"true", "true"}, {"on", "off"}, {"true", "false", "other"}} {
		candidate := property
		candidate.PossibleValues = values
		if catalogAllowsBoolSettingWrite(definition, capabilities, candidate) {
			t.Fatalf("write allowed for unsupported choices %v", values)
		}
	}
	property.Access = "readOnly"
	if catalogAllowsBoolSettingWrite(definition, capabilities, property) {
		t.Fatal("catalog read-only setting became writable")
	}
}
