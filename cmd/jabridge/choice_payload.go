package main

import "github.com/Watchdog0x/jabridge/internal/modelcatalog"

// Structured settings still appear as ordinary choices to CLI, IPC and TUI.
// Their IDs describe choices, not bytes to send. Encode always receives a
// fresh device reply, and Verify checks the complete resulting setting.
type choicePayloadCodec interface {
	BindProperty(modelcatalog.Property) (choicePayloadCodec, []settingChoice, error)
	Decode([]byte) (byte, string, error)
	Encode(current []byte, choice byte) ([]byte, error)
	Verify(written, read []byte) error
	ParseChoice(string) (byte, error)
}

type choiceReadKey struct {
	class, op, destination byte
	request                string
}

type choiceReadResult struct {
	payload []byte
	err     error
}

// Scoped to one device's settings snapshot. All five EQ rows share one read;
// writes and later snapshots always make their own fresh reads.
type choiceSnapshotReads map[choiceReadKey]choiceReadResult

func readChoiceSettingFromSnapshot(definition choiceSettingDefinition, reads choiceSnapshotReads, read func() ([]byte, error)) (choiceSettingValue, error) {
	if definition.Codec == nil {
		payload, err := read()
		if err != nil {
			return choiceSettingValue{}, err
		}
		return decodeChoiceSettingPayload(definition, payload)
	}
	key := choiceReadKey{definition.Class, definition.Op, definition.Destination, string(definition.Request)}
	result, found := reads[key]
	if !found {
		result.payload, result.err = read()
		reads[key] = result
	}
	if result.err != nil {
		return choiceSettingValue{}, result.err
	}
	return decodeChoiceSettingPayload(definition, result.payload)
}

func unavailableChoiceSetting(definition choiceSettingDefinition, value string) choiceSettingValue {
	definition.Writable = false
	definition.Help = "This control is unavailable. Reopen Settings or run jabridge debug for details."
	return choiceSettingValue{Definition: definition, ChoiceIndex: -1, FallbackName: value, Editable: false, Provenance: settingProvenance{Reason: definition.Help}}
}

func appendUnavailableChoice(values []choiceSettingValue, seen map[string]struct{}, definition choiceSettingDefinition, value string) []choiceSettingValue {
	seen[definition.Key] = struct{}{}
	if isEqualizerBand(definition.Key) {
		if _, exists := seen["equalizer"]; exists {
			return values
		}
		seen["equalizer"] = struct{}{}
		definition.Key, definition.Label = "equalizer", "Equalizer"
		definition.Choices = nil
	}
	return append(values, unavailableChoiceSetting(definition, value))
}
