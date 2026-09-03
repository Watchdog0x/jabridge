package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDongleSettingDefinitionsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, definition := range dongleBoolSettingDefinitions {
		if definition.Key == "" || definition.Label == "" {
			t.Fatalf("setting has an empty key or label: %#v", definition)
		}
		if seen[definition.Key] {
			t.Fatalf("duplicate setting key %q", definition.Key)
		}
		seen[definition.Key] = true
	}
	for _, key := range []string{"auto-pairing", "prioritize-computer-audio", "dedicated-call"} {
		definition, ok := findBoolSettingDefinition(settingScopeDongle, key)
		if !ok || !definition.Writable || !definition.ValidatedLink380 {
			t.Fatalf("editable Link 380 setting %q is missing or not validated", key)
		}
	}
}

func TestBoolSettingPayloadConversion(t *testing.T) {
	normal := boolSettingDefinition{Key: "normal"}
	if got, err := decodeBoolSettingPayload(normal, []byte{1}); err != nil || !got {
		t.Fatalf("decode normal true = %v, %v", got, err)
	}
	if got := encodeBoolSettingPayload(normal, false); !bytes.Equal(got, []byte{0}) {
		t.Fatalf("encode normal false = %x", got)
	}

	inverted := boolSettingDefinition{Key: "inverted", Invert: true}
	if got, err := decodeBoolSettingPayload(inverted, []byte{0}); err != nil || !got {
		t.Fatalf("decode inverted true = %v, %v", got, err)
	}
	if got := encodeBoolSettingPayload(inverted, true); !bytes.Equal(got, []byte{0}) {
		t.Fatalf("encode inverted true = %x", got)
	}

	nested := boolSettingDefinition{Key: "nested", ResponseIndex: 1, WritePrefix: []byte{7}}
	if got, err := decodeBoolSettingPayload(nested, []byte{7, 1}); err != nil || !got {
		t.Fatalf("decode nested true = %v, %v", got, err)
	}
	if got := encodeBoolSettingPayload(nested, false); !bytes.Equal(got, []byte{7, 0}) {
		t.Fatalf("encode nested false = %x", got)
	}
}

func TestBoolSettingPayloadRejectsInvalidValues(t *testing.T) {
	definition := boolSettingDefinition{Key: "test", ResponseIndex: 1}
	if _, err := decodeBoolSettingPayload(definition, []byte{0}); err == nil {
		t.Fatal("short setting payload was accepted")
	}
	if _, err := decodeBoolSettingPayload(definition, []byte{0, 2}); err == nil {
		t.Fatal("non-boolean setting payload was accepted")
	}
}

func TestLink380SettingPackets(t *testing.T) {
	tests := []struct {
		key  string
		op   byte
		want []byte
	}{
		{"auto-pairing", 0x40, []byte{0x05, 0x01, 0x00, 0x30, 0x87, 0x13, 0x40, 0x01}},
		{"prioritize-computer-audio", 0x99, []byte{0x05, 0x01, 0x00, 0x30, 0x87, 0x13, 0x99, 0x01}},
		{"dedicated-call", 0x88, []byte{0x05, 0x01, 0x00, 0x30, 0x87, 0x13, 0x88, 0x01}},
	}
	for _, test := range tests {
		definition, ok := findBoolSettingDefinition(settingScopeDongle, test.key)
		if !ok || definition.Op != test.op {
			t.Fatalf("definition %q = %#v", test.key, definition)
		}
		report, err := buildGNPReport(gnpSrcDongle, 0x30, gnpFlagCmd, definition.Class, definition.Op, encodeBoolSettingPayload(definition, true))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(report[:len(test.want)], test.want) {
			t.Errorf("%s packet = %x, want %x", test.key, report[:len(test.want)], test.want)
		}
	}
}

func TestConfigurationModePackets(t *testing.T) {
	permission, err := buildGNPReport(gnpSrcDongle, 0x44, gnpFlagQuery, gnpClassPairingDevice, 0x11, nil)
	if err != nil {
		t.Fatal(err)
	}
	permissionWant := []byte{0x05, 0x01, 0x00, 0x44, 0x46, 0x0d, 0x11}
	if !bytes.Equal(permission[:len(permissionWant)], permissionWant) {
		t.Fatalf("permission packet = %x, want %x", permission[:len(permissionWant)], permissionWant)
	}
	end, err := buildGNPReport(gnpSrcDongle, 0x45, gnpFlagCmd, gnpClassPairingDevice, 0x12, nil)
	if err != nil {
		t.Fatal(err)
	}
	endWant := []byte{0x05, 0x01, 0x00, 0x45, 0x86, 0x0d, 0x12}
	if !bytes.Equal(end[:len(endWant)], endWant) {
		t.Fatalf("end packet = %x, want %x", end[:len(endWant)], endWant)
	}
}

func TestSettingEditPolicy(t *testing.T) {
	definition, _ := findBoolSettingDefinition(settingScopeDongle, "auto-pairing")
	if !canEditBoolSetting(&jabra_DeviceInfo{isDongle: true, productID: 0x24c7}, definition) {
		t.Fatal("validated Link 380 setting is not editable")
	}
	if canEditBoolSetting(&jabra_DeviceInfo{isDongle: true, productID: 0x0a17}, definition) {
		t.Fatal("untested Link 370 setting became editable")
	}

	headsetDefinition, _ := findBoolSettingDefinition(settingScopeHeadset, "sidetone")
	headset := &jabra_DeviceInfo{productID: 0x24b7, deviceConnection: deviceConnectionType_USB}
	if !canEditBoolSetting(headset, headsetDefinition) {
		t.Fatal("capability-confirmed direct-USB headset setting is not editable")
	}
	headset.deviceConnection = deviceConnectionType_BT
	if !canEditBoolSetting(headset, headsetDefinition) {
		t.Fatal("capability-confirmed through-dongle headset setting is not editable")
	}
}

func TestSettingDestinations(t *testing.T) {
	tests := []struct {
		name       string
		device     *jabra_DeviceInfo
		definition boolSettingDefinition
		fallback   byte
		want       byte
	}{
		{"dongle", &jabra_DeviceInfo{isDongle: true}, boolSettingDefinition{}, 1, 1},
		{"direct headset", &jabra_DeviceInfo{}, boolSettingDefinition{}, 8, 8},
		{"wireless headset", &jabra_DeviceInfo{deviceConnection: deviceConnectionType_BT}, boolSettingDefinition{}, 4, 4},
		{"link controller override", &jabra_DeviceInfo{}, boolSettingDefinition{Destination: 3}, 8, 3},
	}
	for _, test := range tests {
		if got := settingDestination(test.definition.Destination, test.fallback); got != test.want {
			t.Errorf("%s destination = %d, want %d", test.name, got, test.want)
		}
	}
}

func TestFormatBoolSettingUsesSimpleText(t *testing.T) {
	setting := boolSettingValue{
		Definition: boolSettingDefinition{Label: "Auto pairing"},
		Value:      true,
		Editable:   true,
	}
	if got := formatBoolSetting(setting); got != "[ON ] Auto pairing" {
		t.Fatalf("editable setting line = %q", got)
	}
	setting.Editable = false
	if got := formatBoolSetting(setting); !strings.Contains(got, "read only") {
		t.Fatalf("read-only setting line = %q", got)
	}
}
