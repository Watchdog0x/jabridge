package main

import (
	"strings"
	"testing"

	shellcompletion "github.com/Watchdog0x/jabridge/internal/completion"
)

func TestFormatBatteryLine(t *testing.T) {
	tests := []struct {
		name    string
		battery *batteryStatus
		want    string
	}{
		{"valid", &batteryStatus{levelInPercent: 48}, "Headset: 48%"},
		{"charging", &batteryStatus{levelInPercent: 73, charging: true}, "Headset: 73% (charging)"},
		{"missing", nil, "Battery: unavailable"},
		{"invalid", &batteryStatus{levelInPercent: 230}, "Battery: unavailable"},
		{
			"components",
			&batteryStatus{components: []batteryComponentStatus{
				{label: "Left", levelInPercent: 61},
				{label: "Right", levelInPercent: 48, charging: true},
			}},
			"Headset: Left 61%, Right 48% charging",
		},
		{
			"components-global-charging",
			&batteryStatus{charging: true, components: []batteryComponentStatus{
				{label: "Left", levelInPercent: 61},
				{label: "Right", levelInPercent: 48},
			}},
			"Headset: Left 61%, Right 48% (charging)",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatBatteryLine("Headset", test.battery); got != test.want {
				t.Fatalf("formatBatteryLine() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBashCompletionCoversEverySetting(t *testing.T) {
	for _, definition := range dongleBoolSettingDefinitions {
		selector := "dongle." + definition.Key
		if !strings.Contains(shellcompletion.JabridgeBash, selector) {
			t.Errorf("Bash completion is missing %s", selector)
		}
	}
	for _, definition := range headsetBoolSettingDefinitions {
		selector := "headset." + definition.Key
		if !strings.Contains(shellcompletion.JabridgeBash, selector) {
			t.Errorf("Bash completion is missing %s", selector)
		}
	}
	for _, definition := range headsetChoiceSettingDefinitions {
		selector := "headset." + definition.Key
		if !strings.Contains(shellcompletion.JabridgeBash, selector) {
			t.Errorf("Bash completion is missing %s", selector)
		}
	}
	for _, definition := range headsetTextSettingDefinitions {
		selector := "headset." + definition.Key
		if !strings.Contains(shellcompletion.JabridgeBash, selector) {
			t.Errorf("Bash completion is missing %s", selector)
		}
	}
}
