package main

import (
	"bytes"
	"testing"
)

func TestWriteAcknowledgementRequiresMatchingSourceSequenceAndReply(t *testing.T) {
	for _, ack := range [][]byte{{0, 8, 0x32, 0xc5, 0xff}, {0, 8, 0x32, 0xca, 0xff, 0, 0, 0, 0, 0}} {
		matched, err := matchGNPWriteReply(ack, 8, 0x32)
		if !matched || err != nil {
			t.Fatal(matched, err)
		}
	}
	for _, wrong := range [][]byte{{0, 1, 0x32, 0xc5, 0xff}, {0, 8, 0x31, 0xc6, 0xfe, 3}, {0, 8, 0x32, 5, 0xff}, {0, 8, 0x32, 0xca, 0xff}} {
		matched, err := matchGNPWriteReply(wrong, 8, 0x32)
		if matched || err != nil {
			t.Fatal("unrelated/incomplete reply accepted", wrong)
		}
	}
	if matched, err := matchGNPWriteReply([]byte{0, 8, 0x32, 0xc6, 0xfe, 3}, 8, 0x32); !matched || err == nil {
		t.Fatal(matched, err)
	}
}

func TestVariantLengthPrefixIncludesConfigurationVariant(t *testing.T) {
	if variant, ok := decodeDeviceVariant([]byte{3, 0x13, 0, 1}); !ok || variant != "13-00-01" {
		t.Fatal(variant, ok)
	}
	if _, ok := decodeDeviceVariant([]byte{3, 0x13, 0}); ok {
		t.Fatal("truncated variant accepted")
	}
}

func TestSpeedDialAndBluetoothNameEncoding(t *testing.T) {
	for _, definition := range headsetTextSettingDefinitions {
		if definition.Key != "bluetooth-name" && definition.Key != "speed-dial" {
			continue
		}
		value := "1234"
		encoded, err := encodeSettingText(definition, value)
		if err != nil {
			t.Fatal(err)
		}
		if definition.Key == "speed-dial" && !bytes.Equal(encoded, []byte{4, '1', '2', '3', '4'}) {
			t.Fatal(encoded)
		}
		if definition.Key == "bluetooth-name" && !bytes.Equal(encoded, []byte{'1', '2', '3', '4', 0}) {
			t.Fatal(encoded)
		}
		decoded, err := decodeSettingText(definition, encoded)
		if err != nil || decoded != value {
			t.Fatal(decoded, err)
		}
		if definition.AllowEmpty {
			encoded, err = encodeSettingText(definition, "")
			if err != nil || !bytes.Equal(encoded, []byte{0}) {
				t.Fatal(encoded, err)
			}
		}
	}
}

func TestSpeakButtonActionUsesItsOwnModelMapping(t *testing.T) {
	for _, definition := range headsetChoiceSettingDefinitions {
		if len(definition.CatalogProperties) != 1 || definition.CatalogProperties[0] != "button1Tap" {
			continue
		}
		if definition.Class != 0x13 || definition.Op != 0x27 || definition.ResponseIndex != 2 || !bytes.Equal(definition.Request, []byte{0, 0}) || !bytes.Equal(definition.WritePrefix, []byte{0, 0}) {
			t.Fatal(definition)
		}
		for _, choice := range definition.Choices {
			if choice.CatalogValue == "speedDial" {
				if choice.Raw != 12 {
					t.Fatal(choice)
				}
				return
			}
		}
	}
	t.Fatal("missing model-specific call-button mapping")
}
