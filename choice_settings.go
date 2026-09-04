package main

import (
	"fmt"
	"strings"
	"time"
)

type settingChoice struct {
	Name         string
	Raw          byte
	CatalogValue string
}

type choiceSettingDefinition struct {
	Key                 string
	Label               string
	Scope               settingScope
	Class               byte
	Op                  byte
	Destination         byte
	Request             []byte
	ResponseIndex       int
	WritePrefix         []byte
	Choices             []settingChoice
	CatalogProperties   []string
	Writable            bool
	NeedsConfigMode     bool
	BitMask             byte
	PreservePayload     bool
	ProbeWithoutCatalog bool
}

type choiceSettingValue struct {
	Definition  choiceSettingDefinition
	ChoiceIndex int
	Raw         byte
	Editable    bool
}

var commonButtonFunctions = []settingChoice{
	{Name: "None", Raw: 0, CatalogValue: "noFunction"},
	{Name: "Call handling", Raw: 1, CatalogValue: "callHandling"},
	{Name: "Mute", Raw: 2, CatalogValue: "mute"},
	{Name: "Speed dial", Raw: 3, CatalogValue: "speedDial"},
	{Name: "Busylight", Raw: 4, CatalogValue: "busylight"},
	{Name: "Push to talk", Raw: 5, CatalogValue: "pushToTalk"},
	{Name: "Headset busylight", Raw: 6, CatalogValue: "busylightHeadset"},
	{Name: "Microsoft Teams", Raw: 7, CatalogValue: "msTeam"},
	{Name: "Music", Raw: 8, CatalogValue: "music"},
}

var headsetChoiceSettingDefinitions = []choiceSettingDefinition{
	{
		Key: "sidetone-level", Label: "Sidetone level", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x68, Choices: sidetoneLevelChoices(),
		CatalogProperties: []string{"sidetoneLevelEnum"}, Writable: true, ProbeWithoutCatalog: true,
	},
	{
		Key: "sidetone-level", Label: "Sidetone level", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x7c, ResponseIndex: 1, Choices: sidetoneLevelChoices(),
		CatalogProperties: []string{"sidetoneLevelDsp"}, Writable: true, PreservePayload: true,
	},
	{
		Key: "voice-prompts", Label: "Voice prompts", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x3a, Choices: []settingChoice{
			{Name: "Tones", Raw: 0, CatalogValue: "tones"},
			{Name: "Voice", Raw: 1, CatalogValue: "voice"},
			{Name: "Off", Raw: 2, CatalogValue: "off"},
		}, CatalogProperties: []string{"voicePrompts"}, Writable: true, ProbeWithoutCatalog: true,
	},
	{
		Key: "controller-ringer-volume", Label: "Controller ringer volume", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x03, Destination: 3, Request: []byte{0}, ResponseIndex: 1,
		WritePrefix: []byte{0}, Choices: []settingChoice{
			{Name: "Off", Raw: 0, CatalogValue: "0"},
			{Name: "Low", Raw: 1, CatalogValue: "1"},
			{Name: "Medium", Raw: 2, CatalogValue: "2"},
			{Name: "High", Raw: 3, CatalogValue: "3"},
		}, CatalogProperties: []string{"controllerRingerVolume"}, Writable: true, ProbeWithoutCatalog: true,
	},
	{
		Key: "controller-ringtone", Label: "Controller ringtone", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x3d, Destination: 3, Request: []byte{0}, ResponseIndex: 1,
		WritePrefix: []byte{0}, Choices: ringtoneChoices(), CatalogProperties: []string{"controllerRingtone"}, Writable: true,
	},
	{
		Key: "boom-arm-action", Label: "Boom arm mute/action", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x98, Choices: []settingChoice{
			{Name: "Disabled", Raw: 0, CatalogValue: "disabled"},
			{Name: "Mute", Raw: 1, CatalogValue: "mute"},
			{Name: "End call", Raw: 4, CatalogValue: "endCall"},
			{Name: "Full mute", Raw: 8, CatalogValue: "fullMute"},
		}, CatalogProperties: []string{"boomArmRotateInCallAction"}, Writable: true,
		BitMask: 0x0d, PreservePayload: true,
	},
	{
		Key: "boom-arm-guidance", Label: "Boom arm guidance", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0xbc, Choices: []settingChoice{
			{Name: "Sound effects", Raw: 0, CatalogValue: "soundEffects"},
			{Name: "Voice prompts", Raw: 1, CatalogValue: "prompts"},
			{Name: "Off", Raw: 2, CatalogValue: "off"},
		}, CatalogProperties: []string{"callAcceptedSound"}, Writable: true,
	},
	{
		Key: "audio-protection", Label: "Audio protection", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x01, Choices: []settingChoice{
			{Name: "Basic PeakStop", Raw: 0, CatalogValue: "peakStopOnly"},
			{Name: "Level 1", Raw: 1, CatalogValue: "level1"},
			{Name: "Level 2", Raw: 2, CatalogValue: "level2"},
			{Name: "Level 3", Raw: 3, CatalogValue: "level3"},
			{Name: "Level 4", Raw: 4, CatalogValue: "level4"},
			{Name: "G616", Raw: 5, CatalogValue: "g616"},
		}, CatalogProperties: []string{"intellitoneLevel"}, Writable: true,
	},
	{
		Key: "auto-sleep", Label: "Auto sleep", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x90, Choices: []settingChoice{
			{Name: "Never", Raw: 0, CatalogValue: "0"},
			{Name: "30 min", Raw: 3, CatalogValue: "3"},
			{Name: "1 hour", Raw: 6, CatalogValue: "6"},
			{Name: "2 hours", Raw: 12, CatalogValue: "12"},
			{Name: "4 hours", Raw: 24, CatalogValue: "24"},
			{Name: "8 hours", Raw: 48, CatalogValue: "48"},
			{Name: "12 hours", Raw: 72, CatalogValue: "72"},
			{Name: "16 hours", Raw: 96, CatalogValue: "96"},
		}, CatalogProperties: []string{"inactivityDelay"}, Writable: true,
	},
	{
		Key: "mute-reminder", Label: "Mute reminder", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x1e, Choices: muteReminderChoices(),
		CatalogProperties: []string{"muteReminderInterval"}, Writable: true,
	},
	{
		Key: "sound-mode", Label: "Sound mode", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x15, Choices: []settingChoice{
			{Name: "Normal", Raw: 0, CatalogValue: "normal"},
			{Name: "Bass", Raw: 1, CatalogValue: "bass"},
			{Name: "Treble", Raw: 2, CatalogValue: "treble"},
		}, CatalogProperties: []string{"soundMode"}, Writable: true,
	},
	buttonFunctionDefinition("call-button", "Call button", "buttonFunctionMfb", 0),
	buttonFunctionDefinition("mute-button", "Mute button", "buttonFunctionMute", 8),
	buttonFunctionDefinition("three-dot-button", "Three-dot button", "buttonFunction3Dots", 18),
	buttonFunctionDefinition("four-dot-button", "Four-dot button", "buttonFunction4Dots", 19),
}

func buttonFunctionDefinition(key, label, catalogProperty string, buttonID byte) choiceSettingDefinition {
	return choiceSettingDefinition{
		Key: key, Label: label, Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x8a, Request: []byte{buttonID}, ResponseIndex: 1,
		WritePrefix: []byte{buttonID}, Choices: append([]settingChoice(nil), commonButtonFunctions...),
		CatalogProperties: []string{catalogProperty}, Writable: true, NeedsConfigMode: true, ProbeWithoutCatalog: true,
	}
}

func muteReminderChoices() []settingChoice {
	choices := []settingChoice{{Name: "Off", Raw: 0, CatalogValue: "0"}}
	for seconds := 10; seconds <= 60; seconds += 10 {
		choices = append(choices, settingChoice{
			Name: fmt.Sprintf("%d seconds", seconds), Raw: byte(seconds), CatalogValue: fmt.Sprintf("%d", seconds),
		})
	}
	return choices
}

func sidetoneLevelChoices() []settingChoice {
	levels := []struct {
		value   int
		catalog string
	}{
		{-9, "minus9dB"}, {-6, "minus6dB"}, {-4, "minus4dB"}, {-3, "minus3dB"}, {-2, "minus2dB"},
		{0, "_0dB"}, {2, "plus2dB"}, {3, "plus3dB"}, {4, "plus4dB"}, {6, "plus6dB"},
	}
	choices := make([]settingChoice, 0, len(levels))
	for _, level := range levels {
		choices = append(choices, settingChoice{
			Name: fmt.Sprintf("%d dB", level.value), Raw: byte(int8(level.value)), CatalogValue: level.catalog,
		})
	}
	return choices
}

func signedRangeChoices(minimum, maximum int, suffix string) []settingChoice {
	choices := make([]settingChoice, 0, maximum-minimum+1)
	for value := minimum; value <= maximum; value++ {
		choices = append(choices, settingChoice{Name: fmt.Sprintf("%d%s", value, suffix), Raw: byte(int8(value))})
	}
	return choices
}

func ringtoneChoices() []settingChoice {
	choices := make([]settingChoice, 0, 13)
	for index := 0; index < 10; index++ {
		choices = append(choices, settingChoice{
			Name: fmt.Sprintf("Tone %d", index+1), Raw: byte(index), CatalogValue: fmt.Sprintf("type%d", index),
		})
	}
	return append(choices,
		settingChoice{Name: "Custom", Raw: 128, CatalogValue: "custom"},
		settingChoice{Name: "Random", Raw: 254, CatalogValue: "random"},
		settingChoice{Name: "Off", Raw: 255, CatalogValue: "off"},
	)
}

func choiceSettingDefinitions(scope settingScope) []choiceSettingDefinition {
	if scope == settingScopeHeadset {
		return headsetChoiceSettingDefinitions
	}
	return nil
}

func readChoiceSetting(device *jabra_DeviceInfo, definition choiceSettingDefinition) (choiceSettingValue, error) {
	payload, err := readChoiceSettingPayload(device, definition)
	if err != nil {
		return choiceSettingValue{}, err
	}
	if definition.ResponseIndex < 0 || definition.ResponseIndex >= len(payload) {
		return choiceSettingValue{}, fmt.Errorf("setting %s returned %d bytes", definition.Key, len(payload))
	}
	raw := payload[definition.ResponseIndex]
	if definition.BitMask != 0 {
		raw &= definition.BitMask
	}
	index := -1
	for choiceIndex, choice := range definition.Choices {
		if choice.Raw == raw {
			index = choiceIndex
			break
		}
	}
	return choiceSettingValue{
		Definition:  definition,
		ChoiceIndex: index,
		Raw:         raw,
		Editable:    definition.Writable,
	}, nil
}

func readChoiceSettingPayload(device *jabra_DeviceInfo, definition choiceSettingDefinition) ([]byte, error) {
	h, defaultDestination, err := settingTransport(device)
	if err != nil {
		return nil, err
	}
	defer h.close()
	destination := settingDestination(definition.Destination, defaultDestination)
	payload, err := gnpQueryPayloadWithDataTimeout(
		h, destination, nextSeq(), definition.Class, definition.Op, definition.Request, 900*time.Millisecond,
	)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func writeChoiceSetting(device *jabra_DeviceInfo, setting choiceSettingValue, choiceIndex int) error {
	definition := setting.Definition
	if !setting.Editable || choiceIndex < 0 || choiceIndex >= len(definition.Choices) {
		return fmt.Errorf("setting %s is read-only on this device: %w", definition.Key, ErrNotSupported)
	}
	payload := append(append([]byte(nil), definition.WritePrefix...), definition.Choices[choiceIndex].Raw)
	if definition.PreservePayload {
		current, err := readChoiceSettingPayload(device, definition)
		if err != nil {
			return fmt.Errorf("read %s before write: %w", definition.Key, err)
		}
		raw := definition.Choices[choiceIndex].Raw
		payload, err = mergeSettingPayload(current, definition.ResponseIndex, definition.BitMask, raw)
		if err != nil {
			return fmt.Errorf("setting %s: %w", definition.Key, err)
		}
	}
	if err := writeSettingPacket(device, definition.Destination, definition.Class, definition.Op, payload, definition.NeedsConfigMode); err != nil {
		return fmt.Errorf("write %s: %w", definition.Key, err)
	}
	readBack, err := readChoiceSettingWithRetry(device, definition)
	if err != nil {
		return fmt.Errorf("verify %s: %w", definition.Key, err)
	}
	if readBack.Raw != definition.Choices[choiceIndex].Raw {
		return fmt.Errorf("verify %s: value did not match", definition.Key)
	}
	return nil
}

func readChoiceSettingWithRetry(device *jabra_DeviceInfo, definition choiceSettingDefinition) (choiceSettingValue, error) {
	var lastErr error
	for attempt := 0; attempt < 12; attempt++ {
		candidate := refreshedSettingsDevice(device)
		value, err := readChoiceSetting(candidate, definition)
		if err == nil {
			return value, nil
		}
		lastErr = err
		if !definition.NeedsConfigMode {
			break
		}
		time.Sleep(time.Second)
		scanAndAttachDevices()
	}
	return choiceSettingValue{}, lastErr
}

func readSupportedChoiceSettings(device *jabra_DeviceInfo, scope settingScope) []choiceSettingValue {
	definitions := choiceSettingDefinitions(scope)
	if len(definitions) == 0 {
		return nil
	}
	values := make([]choiceSettingValue, 0, len(definitions))
	capabilities, catalogErr := lookupDeviceModel(device)
	seen := make(map[string]struct{})
	for _, definition := range definitions {
		if _, exists := seen[definition.Key]; exists {
			continue
		}
		if catalogErr == nil {
			property, supported := firstCatalogProperty(capabilities, definition.CatalogProperties)
			if !supported {
				continue
			}
			choices := choicesAllowedByCatalog(definition.Choices, property.PossibleValues)
			if len(choices) == 0 {
				continue
			}
			definition.Choices = choices
		} else if definition.ProbeWithoutCatalog {
			// A current device-model profile is needed before Jabridge can
			// promise that every write choice is valid for this exact model.
			// Keep successful reads visible, but do not offer an unbounded write.
			definition.Writable = false
		} else {
			continue
		}
		value, err := readChoiceSetting(device, definition)
		if err == nil {
			seen[definition.Key] = struct{}{}
			values = append(values, value)
		}
	}
	return values
}

func choicesAllowedByCatalog(choices []settingChoice, possibleValues []string) []settingChoice {
	if len(possibleValues) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(possibleValues))
	for _, value := range possibleValues {
		allowed[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	filtered := make([]settingChoice, 0, len(choices))
	for _, choice := range choices {
		catalogValue := choice.CatalogValue
		if catalogValue == "" {
			catalogValue = choice.Name
		}
		if _, exists := allowed[strings.ToLower(strings.TrimSpace(catalogValue))]; exists {
			filtered = append(filtered, choice)
		}
	}
	return filtered
}

func choiceSettingName(setting choiceSettingValue) string {
	if setting.ChoiceIndex >= 0 && setting.ChoiceIndex < len(setting.Definition.Choices) {
		return setting.Definition.Choices[setting.ChoiceIndex].Name
	}
	return fmt.Sprintf("Unknown (%d)", setting.Raw)
}

func nextChoiceIndex(setting choiceSettingValue) (int, error) {
	if len(setting.Definition.Choices) == 0 {
		return 0, fmt.Errorf("setting %s has no choices", setting.Definition.Key)
	}
	if setting.ChoiceIndex < 0 {
		return 0, nil
	}
	return (setting.ChoiceIndex + 1) % len(setting.Definition.Choices), nil
}

func findChoiceIndex(definition choiceSettingDefinition, name string) (int, bool) {
	wanted := strings.ToLower(strings.TrimSpace(name))
	wantedToken := choiceValueToken(name)
	for index, choice := range definition.Choices {
		if strings.ToLower(choice.Name) == wanted || choiceValueToken(choice.Name) == wantedToken {
			return index, true
		}
	}
	return 0, false
}

func choiceValueToken(name string) string {
	name = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "_", " ")
	return strings.Join(strings.Fields(name), "-")
}
