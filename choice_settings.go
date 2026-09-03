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
	Key             string
	Label           string
	Scope           settingScope
	Class           byte
	Op              byte
	Destination     byte
	Request         []byte
	ResponseIndex   int
	WritePrefix     []byte
	Choices         []settingChoice
	CatalogProperty string
	Writable        bool
	NeedsConfigMode bool
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
	{Name: "Headset busylight", Raw: 6, CatalogValue: "headsetBusylight"},
	{Name: "Microsoft Teams", Raw: 7, CatalogValue: "microsoftTeams"},
	{Name: "Music", Raw: 8, CatalogValue: "music"},
}

var headsetChoiceSettingDefinitions = []choiceSettingDefinition{
	{
		Key: "sidetone-level", Label: "Sidetone level", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x68, Choices: sidetoneLevelChoices(), CatalogProperty: "sidetoneLevelEnum", Writable: true,
	},
	{
		Key: "voice-prompts", Label: "Voice prompts", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x3a, Choices: []settingChoice{
			{Name: "Tones", Raw: 0, CatalogValue: "tones"},
			{Name: "Voice", Raw: 1, CatalogValue: "voice"},
			{Name: "Off", Raw: 2, CatalogValue: "off"},
		}, CatalogProperty: "audioGuidance", Writable: true,
	},
	{
		Key: "controller-ringer-volume", Label: "Controller ringer volume", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x03, Destination: 3, Request: []byte{0}, ResponseIndex: 1,
		WritePrefix: []byte{0}, Choices: []settingChoice{
			{Name: "Off", Raw: 0, CatalogValue: "0"},
			{Name: "Low", Raw: 1, CatalogValue: "1"},
			{Name: "Medium", Raw: 2, CatalogValue: "2"},
			{Name: "High", Raw: 3, CatalogValue: "3"},
		}, CatalogProperty: "controllerRingerVolume", Writable: true,
	},
	{
		Key: "controller-ringtone", Label: "Controller ringtone", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x3d, Destination: 3, Request: []byte{0}, ResponseIndex: 1,
		WritePrefix: []byte{0}, Choices: ringtoneChoices(), CatalogProperty: "controllerRingtone", Writable: true,
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
		CatalogProperty: catalogProperty, Writable: true, NeedsConfigMode: true,
	}
}

func sidetoneLevelChoices() []settingChoice {
	levels := []struct {
		value   int
		catalog string
	}{
		{-9, "minus9dB"}, {-6, "minus6dB"}, {-3, "minus3dB"},
		{0, "_0dB"}, {3, "plus3dB"}, {6, "plus6dB"},
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
	h, defaultDestination, err := settingTransport(device)
	if err != nil {
		return choiceSettingValue{}, err
	}
	defer h.close()
	destination := settingDestination(definition.Destination, defaultDestination)
	payload, err := gnpQueryPayloadWithDataTimeout(
		h, destination, nextSeq(), definition.Class, definition.Op, definition.Request, 900*time.Millisecond,
	)
	if err != nil {
		return choiceSettingValue{}, err
	}
	if definition.ResponseIndex < 0 || definition.ResponseIndex >= len(payload) {
		return choiceSettingValue{}, fmt.Errorf("setting %s returned %d bytes", definition.Key, len(payload))
	}
	raw := payload[definition.ResponseIndex]
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

func writeChoiceSetting(device *jabra_DeviceInfo, setting choiceSettingValue, choiceIndex int) error {
	definition := setting.Definition
	if !setting.Editable || choiceIndex < 0 || choiceIndex >= len(definition.Choices) {
		return fmt.Errorf("setting %s is read-only on this device: %w", definition.Key, ErrNotSupported)
	}
	payload := append(append([]byte(nil), definition.WritePrefix...), definition.Choices[choiceIndex].Raw)
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
	for _, definition := range definitions {
		if catalogErr == nil {
			property, supported := capabilities.Properties[definition.CatalogProperty]
			if !supported {
				continue
			}
			choices := choicesAllowedByCatalog(definition.Choices, property.PossibleValues)
			if len(choices) == 0 {
				continue
			}
			definition.Choices = choices
		} else {
			// A current device-model profile is needed before Jabridge can
			// promise that every write choice is valid for this exact model.
			// Keep successful reads visible, but do not offer an unbounded write.
			definition.Writable = false
		}
		value, err := readChoiceSetting(device, definition)
		if err == nil {
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
