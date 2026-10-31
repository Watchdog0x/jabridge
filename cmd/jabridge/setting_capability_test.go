package main

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func cycleProfileForTest() *modelcatalog.Capabilities {
	return &modelcatalog.Capabilities{PID: 0x2549, Variant: "01-A3", Firmware: "1.0.1", DeviceFirmware: "1.0.1", ExactFirmwareProfile: true}
}

func cycleDefinitionForTest(t *testing.T, profile *modelcatalog.Capabilities, route *jabra_DeviceInfo) choiceSettingDefinition {
	t.Helper()
	definition, ok := choiceSettingForCatalog(headsetChoiceSettingDefinitions[0], noiseCyclePropertyForTest(), profile.ExactFirmwareProfile)
	if !ok {
		t.Fatal("cycle profile not bound")
	}
	definition, _ = bindNoiseCycleProfile(definition, definition.Codec.(noiseCycleCodec), profile, route)
	definition.ExpectedFirmware = profile.Firmware
	return definition
}

func TestNoiseCycleChoicesDependOnExactSetup(t *testing.T) {
	for _, test := range []struct {
		name      string
		change    func(*modelcatalog.Capabilities, *jabra_DeviceInfo)
		qualified bool
	}{
		{"reported setup", func(*modelcatalog.Capabilities, *jabra_DeviceInfo) {}, true},
		{"different headset", func(p *modelcatalog.Capabilities, _ *jabra_DeviceInfo) { p.PID++ }, false},
		{"different variant", func(p *modelcatalog.Capabilities, _ *jabra_DeviceInfo) { p.Variant = "02-A3" }, false},
		{"new headset firmware", func(p *modelcatalog.Capabilities, _ *jabra_DeviceInfo) {
			p.Firmware, p.DeviceFirmware = "1.0.2", "1.0.2"
		}, false},
		{"fallback profile", func(p *modelcatalog.Capabilities, _ *jabra_DeviceInfo) { p.ExactFirmwareProfile = false }, false},
		{"firmware unknown", func(p *modelcatalog.Capabilities, _ *jabra_DeviceInfo) { p.DeviceFirmware = "" }, false},
		{"different dongle", func(_ *modelcatalog.Capabilities, d *jabra_DeviceInfo) { d.productID = 0x2e57 }, false},
		{"new dongle firmware", func(_ *modelcatalog.Capabilities, d *jabra_DeviceInfo) { d.firmwareVersion = "3.3.0" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := cycleProfileForTest()
			route := &jabra_DeviceInfo{isDongle: true, productID: 0x2e56, firmwareVersion: "3.2.0"}
			test.change(profile, route)
			definition := cycleDefinitionForTest(t, profile, route)
			var masks []byte
			for _, c := range definition.Choices {
				masks = append(masks, c.Raw)
			}
			want := []byte{7}
			if test.qualified {
				want = []byte{7, 6, 3}
			}
			if !slices.Equal(masks, want) {
				t.Fatalf("choices %v; want %v", masks, want)
			}
			if definition.Writable != profile.ExactFirmwareProfile {
				t.Fatal("firmware gate changed")
			}
		})
	}
	definition := cycleDefinitionForTest(t, cycleProfileForTest(), nil)
	if len(definition.Choices) != 1 || definition.Choices[0].Raw != 7 {
		t.Fatal("direct USB inherited dongle qualification")
	}
}

func TestNoiseCycleUnconfirmedChoicesNeverReachDeviceOrLease(t *testing.T) {
	withPreviewSettingWrites(t)
	state := t.TempDir()
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", state)
	t.Setenv("JABRIDGE_HISTORY", "off")
	route := &jabra_DeviceInfo{isDongle: true, productID: 0x2e56, firmwareVersion: "3.2.0"}
	definition := cycleDefinitionForTest(t, cycleProfileForTest(), route)
	setting, err := decodeChoiceSettingPayload(definition, []byte{3, 2, 1, 0})
	if err != nil {
		t.Fatal(err)
	}
	for _, mask := range []byte{1, 2, 4, 5} {
		choice := noiseCycleChoice(mask)
		if _, err := definition.Codec.ParseChoice(choice.CatalogValue); err == nil {
			t.Fatal("parsed unconfirmed combination", mask)
		}
		if _, err := definition.Codec.Encode([]byte{3, 2, 1, 0}, mask); err == nil {
			t.Fatal("encoded unconfirmed combination", mask)
		}
		if _, _, err := applyDeviceSettingFromText(nil, deviceSettingValue{Choice: &setting}, choice.CatalogValue); err == nil {
			t.Fatal("CLI accepted unconfirmed combination", mask)
		}
		forged := setting
		forged.Definition.Choices = []settingChoice{choice}
		if err := writeChoiceSetting(nil, forged, 0); err == nil || !strings.Contains(err.Error(), "combinations confirmed") {
			t.Fatal("forged choice reached transport", err)
		}
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatal("rejected choice created a write lease", entries, err)
	}
}

func TestNoiseCycleObservedCurrentValueIsNoOpOnly(t *testing.T) {
	withPreviewSettingWrites(t)
	state := t.TempDir()
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", state)
	definition := cycleDefinitionForTest(t, cycleProfileForTest(), nil)
	current := []byte{2, 2, 0}
	value, err := decodeChoiceSettingPayload(definition, current)
	if err != nil || len(value.Definition.Choices) != 2 || !strings.Contains(choiceSettingName(value), "current; not confirmed") {
		t.Fatal("current cycle lost", value, err)
	}
	index, ok := findChoiceIndex(value.Definition, value.Definition.Choices[value.ChoiceIndex].Name)
	if !ok || index != value.ChoiceIndex {
		t.Fatal("current choice cannot be selected")
	}
	if err := writeChoiceSetting(nil, value, index); err != nil {
		t.Fatal("no-op touched device access", err)
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatal("no-op took a lease", entries, err)
	}
	if _, err := value.Definition.Codec.Encode([]byte{3, 2, 1, 0}, value.Raw); err == nil {
		t.Fatal("observed value became a new write choice")
	}
	written, err := value.Definition.Codec.Encode(current, 7)
	if err != nil || !slices.Equal(written, []byte{3, 2, 0, 1}) {
		t.Fatal("fresh order was discarded", written, err)
	}
}

func TestNoiseCycleRouteIsResolvedAgainAfterReconnect(t *testing.T) {
	withDeviceState(t, devices{
		1: {deviceID: 1, instance: "first", isDongle: true, productID: 0x2e56, firmwareVersion: "3.2.0"},
		2: {deviceID: 2, instance: "headset", productID: 0x2549, parentDeviceID: 1, deviceConnection: deviceConnectionType_BT},
	}, 2, 1)
	profile := cycleProfileForTest()
	original, ok := choiceSettingForCatalog(headsetChoiceSettingDefinitions[0], noiseCyclePropertyForTest(), true)
	if !ok {
		t.Fatal("missing cycle profile")
	}
	definition, qualified := constrainNoiseCycle(deviceForID(2), profile, original)
	if !qualified || len(definition.Choices) != 3 {
		t.Fatal("attached route not used")
	}
	for _, mask := range []byte{7, 6, 3} {
		payload, err := definition.Codec.Encode([]byte{3, 2, 1, 0}, mask)
		if err != nil {
			t.Fatal("confirmed combination missing", mask, err)
		}
		actual, _, err := definition.Codec.Decode(payload)
		if err != nil || actual != mask || definition.Codec.Verify(payload, payload) != nil {
			t.Fatal("confirmed combination did not round-trip", mask, err)
		}
	}
	deviceStateMu.Lock()
	deviceManager[1] = &jabra_DeviceInfo{deviceID: 1, instance: "second", isDongle: true, productID: 0x2e56, firmwareVersion: "3.3.0"}
	deviceStateMu.Unlock()
	definition, qualified = constrainNoiseCycle(deviceForID(2), profile, original)
	if qualified || len(definition.Choices) != 1 {
		t.Fatal("old route qualification survived reconnect")
	}
	if len(original.Choices) != 1 {
		t.Fatal("binding mutated the shared definition")
	}
}

func TestSettingSupportIsAdditiveAndDoesNotPromoteReads(t *testing.T) {
	for _, kind := range []string{"bool", "choice", "text", "dongle"} {
		t.Run(kind, func(t *testing.T) {
			p := observedSetting(kind != "dongle", true, kind == "dongle", "")
			var setting deviceSettingValue
			switch kind {
			case "bool", "dongle":
				setting.Boolean = &boolSettingValue{Provenance: p}
			case "choice":
				setting.Choice = &choiceSettingValue{Provenance: p}
			case "text":
				setting.Text = &textSettingValue{Provenance: p}
			}
			info := ipcSettingInfo("headset", setting)
			if info.Support == nil || info.Support.State != "read-only" || !slices.Contains(info.Support.Sources, "device-read") {
				t.Fatal("read promoted to write", info)
			}
			if kind == "dongle" && slices.Contains(info.Support.Sources, "model-catalog") {
				t.Fatal("invented dongle catalog evidence")
			}
			data, err := json.Marshal(info)
			if err != nil {
				t.Fatal(err)
			}
			var old map[string]json.RawMessage
			if err := json.Unmarshal(data, &old); err != nil {
				t.Fatal(err)
			}
			delete(old, "support")
			legacy, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			var roundtrip ipc.SettingInfo
			if err := json.Unmarshal(legacy, &roundtrip); err != nil {
				t.Fatal(err)
			}
			info.Support = nil
			if !reflect.DeepEqual(info, roundtrip) {
				t.Fatal("old fields changed", info, roundtrip)
			}
		})
	}
}

func TestNoiseCycleCommandTokensRoundTrip(t *testing.T) {
	withPreviewSettingWrites(t)
	t.Setenv("JABRIDGE_HISTORY", "off")
	definition := cycleDefinitionForTest(t, cycleProfileForTest(), nil)
	current, err := decodeChoiceSettingPayload(definition, []byte{2, 2, 0})
	if err != nil {
		t.Fatal(err)
	}
	for _, choice := range current.Definition.Choices {
		token := choiceCommandToken(current.Definition, choice)
		index, ok := findChoiceIndex(current.Definition, token)
		if !ok || current.Definition.Choices[index].Raw != choice.Raw {
			t.Fatal("printed token rejected", token)
		}
	}
	token := choiceCommandToken(current.Definition, current.Definition.Choices[current.ChoiceIndex])
	if token != "off,anc" {
		t.Fatal("command token contains display annotation", token)
	}
	if _, changed, err := applyDeviceSettingFromText(nil, deviceSettingValue{Choice: &current}, token); err != nil || changed {
		t.Fatal("current token made a change", changed, err)
	}
	if token := choiceCommandToken(choiceSettingDefinition{}, settingChoice{Name: "30 min"}); token != "30-min" {
		t.Fatal("ordinary choice token changed", token)
	}
}

func TestVoiceLanguageReadKeepsProfileEvidence(t *testing.T) {
	value, err := voiceLanguageSettingFromProfile(0x2549, []byte{9, 4}, cycleProfileForTest())
	if err != nil {
		t.Fatal(err)
	}
	info := ipcSettingInfo("headset", value)
	if info.Support == nil || info.Support.State != "read-only" || !slices.Equal(info.Support.Sources, []string{"model-catalog", "exact-firmware-profile", "device-read"}) {
		t.Fatal("voice-language read lost provenance", info.Support)
	}
}

func TestUnavailableNoiseCycleDisplayDoesNotInventCurrentValue(t *testing.T) {
	for _, label := range []string{"Unavailable", "Not supported on this firmware"} {
		value := unavailableChoiceSetting(headsetChoiceSettingDefinitions[0], label)
		if got := choiceDisplayValue(value); got != choiceValueToken(label) || strings.Contains(got, "current") {
			t.Fatal("unavailable control displayed as a current value", got)
		}
	}
}
