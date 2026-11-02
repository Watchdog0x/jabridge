package main

import (
	"os"
	"strings"
	"testing"
)

func TestDirectSettingsListShowsBoolAndTextDetailsWithoutChangingValueLines(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "settings-output")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	oldOutput := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = oldOutput }()
	values := []deviceSettingValue{
		{Boolean: &boolSettingValue{Definition: boolSettingDefinition{Key: "sidetone", Help: "Hear your own voice."}, Value: true, Editable: true, Catalog: settingCatalogMetadata{Group: "Audio", Restart: restartFlag(true)}}},
		{Text: &textSettingValue{Definition: textSettingDefinition{Key: "name", Help: "Name shown to other devices."}, Value: "My Name", Catalog: settingCatalogMetadata{Group: "General"}}},
	}
	printSettingValues("headset", &jabra_DeviceInfo{}, values)
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"  headset.sidetone = on  (editable)\n",
		"  headset.name = \"My Name\"  (read only; set a quoted name with the command below)\n",
		"    Group: Audio\n", "    Hear your own voice.\n", "    Name shown to other devices.\n",
		"    Jabra's model data says this change restarts the device.\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
}
