package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func restartFlag(value bool) *bool { return &value }

func TestCatalogRestartEvidenceKeepsUnknownDistinctFromFalse(t *testing.T) {
	for _, test := range []struct {
		name                  string
		known, restart, exact bool
		firmware              string
		want                  *bool
	}{
		{"unknown", false, false, true, "1.2.3", nil},
		{"exact yes", true, true, true, "1.2.3", restartFlag(true)},
		{"fallback yes", true, true, false, "1.2.3", restartFlag(true)},
		{"unread firmware yes", true, true, true, "", restartFlag(true)},
		{"exact no", true, false, true, "1.2.3", restartFlag(false)},
		{"fallback no", true, false, false, "1.2.3", nil},
		{"unread firmware no", true, false, true, "", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			property := modelcatalog.Property{Help: "Useful help", Group: "Audio", RestartKnown: test.known, RequiresRestart: test.restart}
			profile := &modelcatalog.Capabilities{ExactFirmwareProfile: test.exact, DeviceFirmware: test.firmware}
			value := boolSettingValue{Definition: boolSettingDefinition{Key: "sidetone", Writable: false}, Editable: false}
			value.Catalog = bindSettingCatalogMetadata(nil, value.Definition.Key, 0, property, profile)
			info := ipcSettingInfo("headset", deviceSettingValue{Boolean: &value})
			if (info.RequiresRestart == nil) != (test.want == nil) || test.want != nil && *info.RequiresRestart != *test.want {
				t.Fatalf("restart=%v; want %v", info.RequiresRestart, test.want)
			}
			if value.Definition.Writable || value.Editable || info.Editable || info.MayRestart || info.Group != "Audio" || info.Help != "Useful help" {
				t.Fatalf("metadata changed policy or was lost: %#v", info)
			}
		})
	}
}

func TestCatalogMetadataStaysOffControllerAndUnboundRows(t *testing.T) {
	profile := &modelcatalog.Capabilities{ExactFirmwareProfile: true, DeviceFirmware: "1.2.3"}
	property := modelcatalog.Property{Help: "Headset help", Group: "Audio", RestartKnown: true, RequiresRestart: true}
	device := &jabra_DeviceInfo{productID: 0x4052, deviceConnection: deviceConnectionType_USB}
	if got := bindSettingCatalogMetadata(device, "controller-name", 3, property, profile); got != (settingCatalogMetadata{}) {
		t.Fatal("headset schema attached to controller", got)
	}
	if got := bindSettingCatalogMetadata(nil, "sidetone", 0, property, nil); got != (settingCatalogMetadata{}) {
		t.Fatal("metadata invented without a model", got)
	}
}

func TestCatalogHelpPreservesLocalAdviceAndReadOnlyReasons(t *testing.T) {
	value := boolSettingValue{Definition: boolSettingDefinition{Help: "Local advice"}, Catalog: settingCatalogMetadata{Help: "Catalog advice"}}
	setting := deviceSettingValue{Boolean: &value}
	if setting.help() != "Local advice" {
		t.Fatal(setting.help())
	}
	value.Definition.Help = ""
	value.Catalog.Help = strings.Repeat("Catalog advice ", 30)
	value.Provenance = observedSetting(true, false, false, "Read-only: model differs.")
	if got := trimToWidth(setting.help(), 35); !strings.HasPrefix(got, "Read-only: model differs.") {
		t.Fatal("long description hid read-only reason", got)
	}
	value.Catalog.Group = strings.Repeat("Long group ", 30)
	if got := trimToWidth(setting.detailLine(), 35); !strings.HasPrefix(got, "Read-only: model differs.") {
		t.Fatal("group hid read-only reason", got)
	}
	value.Catalog.Help += value.Provenance.Reason
	if got := trimToWidth(setting.detailLine(), 35); !strings.HasPrefix(got, value.Provenance.Reason) {
		t.Fatal("reason at end of catalog text was not moved to the front", got)
	}
	value.Definition.Key = "unknown-composite"
	value.Catalog = bindSettingCatalogMetadata(nil, value.Definition.Key, 0, modelcatalog.Property{Help: "Change this setting", CompositeSettings: []modelcatalog.CompositeSetting{{ID: "unknown"}}}, &modelcatalog.Capabilities{})
	value.Provenance.Reason = settingWriteNotImplemented
	if setting.help() != settingWriteNotImplemented {
		t.Fatal("unknown control advertised edits", setting.help())
	}
}

func TestCatalogRestartDoesNotChangeConfigPacketsOrEditorRules(t *testing.T) {
	value := boolSettingValue{Definition: boolSettingDefinition{Key: "sidetone", Writable: true}, Editable: true}
	setting := deviceSettingValue{Boolean: &value}
	if setting.usesEditor() || setting.restartNotice() != "" {
		t.Fatal("ordinary toggle changed")
	}
	value.Catalog.Restart = restartFlag(true)
	if !setting.usesEditor() || setting.needsConfigMode() || !strings.Contains(setting.restartNotice(), "model data") {
		t.Fatal("catalog restart was treated as configuration mode")
	}
	value.Definition.NeedsConfigMode = true
	value.Catalog.Restart = restartFlag(false)
	if !setting.usesEditor() || !strings.Contains(setting.restartNotice(), "Do not edit during a call") {
		t.Fatal("catalog false suppressed observed restart warning")
	}
}

func TestSettingMetadataJSONPreservesOldPayloadAndThreeRestartStates(t *testing.T) {
	for _, restart := range []*bool{nil, restartFlag(false), restartFlag(true)} {
		value := textSettingValue{Definition: textSettingDefinition{Key: "name", Help: "Local help"}, Catalog: settingCatalogMetadata{Group: "General", Restart: restart}}
		original := ipcSettingInfo("headset", deviceSettingValue{Text: &value})
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var decoded ipc.SettingInfo
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Group != "General" || decoded.MayRestart || (decoded.RequiresRestart == nil) != (restart == nil) || restart != nil && *decoded.RequiresRestart != *restart {
			t.Fatalf("metadata changed on the wire: %s", data)
		}
	}
	var old ipc.SettingInfo
	if err := json.Unmarshal([]byte(`{"key":"mode","mayRestart":true}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.RequiresRestart != nil || old.Group != "" || !strings.Contains(remoteSettingRestartNotice(old), "Saving may restart") {
		t.Fatal("old service payload lost its warning")
	}
}
