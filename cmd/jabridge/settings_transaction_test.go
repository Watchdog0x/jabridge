package main

import (
	"encoding/binary"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingTransactionBusyAndRelease(t *testing.T) {
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	first, err := beginSettingTransaction()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first)
	if second, err := beginSettingTransaction(); err == nil {
		second()
		t.Fatal("overlapping read-modify-write transactions were allowed")
	} else if !strings.Contains(err.Error(), "another Jabridge change") {
		t.Fatal(err)
	}
	first()
	third, err := beginSettingTransaction()
	if err != nil {
		t.Fatal("transaction did not release its locks", err)
	}
	third()
}

func TestSettingFirmwareCheckIsScopedAndRejectsStaleProfiles(t *testing.T) {
	reads := 0
	read := func() ([]byte, error) { reads++; return []byte{5, '1', '.', '0', '.', '1'}, nil }
	if err := checkSettingFirmware("", read); err != nil || reads != 0 {
		t.Fatal("legacy scalar path queried firmware", reads, err)
	}
	if err := checkSettingFirmware("1.0.1", read); err != nil || reads != 1 {
		t.Fatal("matching firmware refused", reads, err)
	}
	if err := checkSettingFirmware("1.1.0", read); err == nil {
		t.Fatal("stale profile was accepted")
	}
	if err := checkSettingFirmware("1.0.1", func() ([]byte, error) { return []byte{5, '1'}, nil }); err == nil {
		t.Fatal("malformed firmware reply was accepted")
	}
	sentinel := errors.New("read failed")
	if err := checkSettingFirmware("1.0.1", func() ([]byte, error) { return nil, sentinel }); !errors.Is(err, sentinel) {
		t.Fatal("firmware query error was lost", err)
	}
	if err := writeProfiledSettingPacket(nil, "test", 0, 0, gnpClassConfig, 0x7d, nil, ""); err == nil {
		t.Fatal("missing profile reached device access")
	}
}

func TestStructuredChoiceRejectsStaleTargetAndKeepsFreshOtherBands(t *testing.T) {
	withPreviewSettingWrites(t)
	definition := equalizerDefinitionForTest(t, 0)
	flat := equalizerBytes(t, equalizerFlatReply)
	loaded, err := decodeChoiceSettingPayload(definition, flat)
	if err != nil {
		t.Fatal(err)
	}
	index, ok := findChoiceIndex(definition, "3")
	if !ok {
		t.Fatal("missing target gain")
	}
	otherChanged := append([]byte(nil), flat...)
	binary.BigEndian.PutUint16(otherChanged[13:15], 120)
	written, err := encodeStructuredChoice(loaded, definition.Choices[index].Raw, otherChanged)
	if err != nil || binary.BigEndian.Uint16(written[12:14]) != 120 {
		t.Fatal("fresh other-band change was discarded", err)
	}
	targetChanged := append([]byte(nil), flat...)
	binary.BigEndian.PutUint16(targetChanged[6:8], 30)
	if _, err := encodeStructuredChoice(loaded, definition.Choices[index].Raw, targetChanged); err == nil {
		t.Fatal("stale edited gain was overwritten")
	}
	oldOffStep := append([]byte(nil), flat...)
	binary.BigEndian.PutUint16(oldOffStep[6:8], 80)
	loaded, err = decodeChoiceSettingPayload(definition, oldOffStep)
	if err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint16(oldOffStep[6:8], 81)
	if _, err := encodeStructuredChoice(loaded, definition.Choices[index].Raw, oldOffStep); err == nil {
		t.Fatal("different non-half-step values shared the unknown logical ID")
	}
}

func TestPlainScalarWritesDoNotTakeSettingsLease(t *testing.T) {
	state := t.TempDir()
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", state)
	device := &jabra_DeviceInfo{deviceID: 60000, instance: "not-attached", productID: 0x2549}
	_ = writeBoolSetting(device, boolSettingDefinition{Key: "plain", Scope: settingScopeHeadset, Writable: true}, true)
	_ = writeChoiceSetting(device, choiceSettingValue{Editable: true, Definition: choiceSettingDefinition{Key: "plain", Choices: []settingChoice{{Name: "One", Raw: 1}}}}, 0)
	matches, err := filepath.Glob(filepath.Join(state, "jabridge", "firmware-recovery*.lock"))
	if err != nil || len(matches) != 0 {
		t.Fatal("plain writes created new coordination files", matches, err)
	}
}
