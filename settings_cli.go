package main

import (
	"fmt"
	"github.com/Watchdog0x/jabridge/internal/history"
	"strings"
)

func runSettings(args []string) error {
	scanAndAttachDevices()
	refreshDongleChildDevice()

	if len(args) == 0 || (len(args) == 1 && args[0] == "list") {
		return printSettings()
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		printSettingsUsage()
		return nil
	}
	if len(args) != 3 || args[0] != "set" {
		return fmt.Errorf("usage: jabridge settings set DEVICE.SETTING VALUE")
	}

	scope, key, err := parseSettingSelector(args[1])
	if err != nil {
		return err
	}
	device, exists := selectedSettingsDevice(scope)
	if !exists {
		return fmt.Errorf("no %s connected", settingScopeName(scope))
	}
	settings := readSupportedDeviceSettings(device, scope)
	setting, exists := findDeviceSettingValue(settings, key)
	if !exists {
		return fmt.Errorf("setting %q is not supported by %s; run jabridge settings to list available settings", args[1], device.deviceName)
	}
	return setDeviceSettingFromText(device, args[1], setting, args[2])
}

func printSettings() error {
	found := false
	if dongle, exists := selectedDongleSnapshot(); exists {
		found = true
		printSettingsForDevice("Dongle", "dongle", dongle, settingScopeDongle)
	}
	if headset, exists := selectedHeadsetSnapshot(); exists {
		found = true
		printSettingsForDevice("Headset", "headset", headset, settingScopeHeadset)
	}
	if !found {
		return fmt.Errorf("no supported Jabra device found")
	}
	return nil
}

func printSettingsForDevice(kind, prefix string, device *jabra_DeviceInfo, scope settingScope) {
	fmt.Printf("%s: %s\n", kind, device.deviceName)
	values := readSupportedDeviceSettings(device, scope)
	if len(values) == 0 {
		if !device.gnpDestinationKnown {
			fmt.Println("  Settings unavailable: the control interface did not answer. Run jabridge diagnose and jabridge debug.")
		} else {
			fmt.Println("  No settings could be read for this model. Run jabridge diagnose and jabridge model.")
		}
		return
	}
	for _, setting := range values {
		mode := "read only"
		if setting.editable() {
			mode = "editable"
		}
		if setting.Choice != nil {
			choices := make([]string, 0, len(setting.Choice.Definition.Choices))
			for _, choice := range setting.Choice.Definition.Choices {
				choices = append(choices, choiceValueToken(choice.Name))
			}
			fmt.Printf("  %s.%s = %s  (%s; choices: %s)\n",
				prefix, setting.key(), choiceValueToken(setting.valueName()), mode, strings.Join(choices, ", "))
			continue
		}
		if setting.Text != nil {
			fmt.Printf("  %s.%s = %q  (%s; set a quoted name with the command below)\n",
				prefix, setting.key(), setting.valueName(), mode)
			continue
		}
		fmt.Printf("  %s.%s = %s  (%s)\n", prefix, setting.key(), strings.ToLower(setting.valueName()), mode)
	}
}

func findDeviceSettingValue(settings []deviceSettingValue, key string) (deviceSettingValue, bool) {
	for _, setting := range settings {
		if setting.key() == key {
			return setting, true
		}
	}
	return deviceSettingValue{}, false
}

func setDeviceSettingFromText(device *jabra_DeviceInfo, selector string, setting deviceSettingValue, textValue string) error {
	value, changed, err := applyDeviceSettingFromText(device, setting, textValue)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Printf("%s is already %s. No change made.\n", selector, value)
		return nil
	}
	fmt.Printf("%s changed to %s and verified.\n", selector, value)
	return nil
}

func applyDeviceSettingFromText(device *jabra_DeviceInfo, setting deviceSettingValue, textValue string) (result string, changed bool, actionErr error) {
	entry := historyDeviceEvent(device, "settings")
	entry.Setting = setting.key()
	finish := history.Begin(entry)
	defer history.EndDeferred(finish, &actionErr)
	if !setting.editable() {
		return "", false, fmt.Errorf("setting %s is read-only", setting.key())
	}
	if setting.Boolean != nil {
		wanted, err := parseOnOff(textValue)
		if err != nil {
			return "", false, err
		}
		valueName := strings.ToLower(onOff(wanted))
		if setting.Boolean.Value == wanted {
			return valueName, false, nil
		}
		if err := writeBoolSetting(device, setting.Boolean.Definition, wanted); err != nil {
			return "", false, err
		}
		return valueName, true, nil
	}
	if setting.Choice != nil {
		choiceIndex, ok := findChoiceIndex(setting.Choice.Definition, textValue)
		if !ok {
			return "", false, fmt.Errorf("invalid value %q for %s; choices: %s", textValue, setting.key(), choiceNames(setting.Choice.Definition.Choices))
		}
		valueName := setting.Choice.Definition.Choices[choiceIndex].Name
		if setting.Choice.ChoiceIndex == choiceIndex {
			return valueName, false, nil
		}
		if err := writeChoiceSetting(device, *setting.Choice, choiceIndex); err != nil {
			return "", false, err
		}
		return valueName, true, nil
	}
	if setting.Text != nil {
		value := strings.TrimSpace(textValue)
		if setting.Text.Value == value {
			return value, false, nil
		}
		if err := writeTextSetting(device, setting.Text.Definition, value); err != nil {
			return "", false, err
		}
		return value, true, nil
	}
	return "", false, fmt.Errorf("invalid setting %s", setting.key())
}

func choiceNames(choices []settingChoice) string {
	names := make([]string, 0, len(choices))
	for _, choice := range choices {
		names = append(names, choice.Name)
	}
	return strings.Join(names, ", ")
}

func parseSettingSelector(selector string) (settingScope, string, error) {
	prefix, key, ok := strings.Cut(selector, ".")
	if !ok || key == "" {
		return 0, "", fmt.Errorf("setting must look like dongle.auto-pairing or headset.sidetone")
	}
	switch prefix {
	case "dongle":
		return settingScopeDongle, key, nil
	case "headset":
		return settingScopeHeadset, key, nil
	default:
		return 0, "", fmt.Errorf("unknown settings device %q; use dongle or headset", prefix)
	}
}

func parseOnOff(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	default:
		return false, fmt.Errorf("value must be on or off")
	}
}

func settingScopeName(scope settingScope) string {
	if scope == settingScopeDongle {
		return "dongle"
	}
	return "headset"
}

func printSettingsUsage() {
	fmt.Println(`Usage:
  jabridge settings
  jabridge settings set dongle.auto-pairing on
  jabridge settings set dongle.prioritize-computer-audio off
  jabridge settings set headset.sidetone on
  jabridge settings set headset.voice-prompts voice
  jabridge settings set headset.headset-name "Office headset"
  jabridge settings set headset.three-dot-button push-to-talk

Only settings answered by the connected device are listed. Every change is
read back from the device before Jabridge reports success. Bash completion
suggests the valid setting names and values.`)
}
