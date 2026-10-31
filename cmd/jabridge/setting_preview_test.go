package main

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func withPreviewSettingWrites(t testing.TB) {
	t.Helper()
	old := previewSettingWritesEnabled.Load()
	previewSettingWritesEnabled.Store(true)
	t.Cleanup(func() { previewSettingWritesEnabled.Store(old) })
}

func preservePreviewSettingWrites(t *testing.T) {
	t.Helper()
	old := previewSettingWritesEnabled.Load()
	t.Cleanup(func() { previewSettingWritesEnabled.Store(old) })
}

func TestPreviewSettingWriteInventoryAndExactOption(t *testing.T) {
	preservePreviewSettingWrites(t)
	want := []string{"equalizer-1khz", "equalizer-250hz", "equalizer-4khz", "equalizer-60hz", "equalizer-7.6khz", "noise-cycle", "power-saving"}
	for _, value := range []string{"", "yes", "enabled", previewSettingWritesAck} {
		t.Setenv(previewSettingWritesEnv, value)
		t.Setenv(experimentalWritesEnv, experimentalWritesAck)
		capturePreviewSettingOption()
		var locked []string
		for _, definition := range choiceSettingDefinitions(settingScopeHeadset) {
			if previewSettingLocked(definition.Key, definition.Codec != nil) {
				locked = append(locked, definition.Key)
			}
		}
		for _, definition := range settingDefinitions(settingScopeHeadset) {
			if previewSettingLocked(definition.Key, false) {
				locked = append(locked, definition.Key)
			}
		}
		slices.Sort(locked)
		if value == previewSettingWritesAck {
			if len(locked) != 0 {
				t.Fatal("explicit setting option did not unlock the preview", locked)
			}
		} else if !slices.Equal(locked, want) {
			t.Fatal("wrong locked inventory or global flag granted preview writes", value, locked)
		}
	}
	previewSettingWritesEnabled.Store(false)
	if !previewSettingLocked("future-structured", true) || previewSettingLocked("auto-sleep", false) || previewSettingLocked("noise-control", false) {
		t.Fatal("future codec failed open or legacy control was locked")
	}
}

func TestPreviewOffRejectsForgedEditableBeforeIO(t *testing.T) {
	preservePreviewSettingWrites(t)
	previewSettingWritesEnabled.Store(false)
	state := t.TempDir()
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", state)
	t.Setenv("JABRIDGE_HISTORY", "off")
	codec, choices, err := (noiseCycleCodec{}).BindProperty(noiseCyclePropertyForTest())
	if err != nil {
		t.Fatal(err)
	}
	definition := choiceSettingDefinition{Key: "noise-cycle", Codec: codec, Writable: true, Choices: choices, ExpectedFirmware: "1.0.1"}
	value := choiceSettingValue{Definition: definition, Editable: true, Raw: choices[0].Raw, ChoiceIndex: 0}
	if err := writeChoiceSetting(nil, value, 0); !errors.Is(err, ErrNotSupported) {
		t.Fatal("forged choice bypassed release policy", err)
	}
	if _, _, err := applyDeviceSettingFromText(nil, deviceSettingValue{Choice: &value}, choices[0].CatalogValue); !errors.Is(err, ErrNotSupported) {
		t.Fatal("disabled current-value request bypassed policy", err)
	}
	boolean, ok := findBoolSettingDefinition(settingScopeHeadset, "power-saving")
	if !ok {
		t.Fatal("missing read definition")
	}
	if err := writeBoolSetting(&jabra_DeviceInfo{}, boolean, true); !errors.Is(err, ErrNotSupported) {
		t.Fatal("forged boolean bypassed release policy", err)
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatal("blocked preview acquired a write lease", entries, err)
	}
}

func TestPreviewSnapshotAndIPCRemainReadOnly(t *testing.T) {
	preservePreviewSettingWrites(t)
	previewSettingWritesEnabled.Store(false)
	for _, key := range []string{"noise-cycle", "equalizer-60hz", "equalizer-250hz", "equalizer-1khz", "equalizer-4khz", "equalizer-7.6khz"} {
		definition := applyPreviewChoicePolicy(choiceSettingDefinition{Key: key, Writable: true, Choices: []settingChoice{{Name: "Current", Raw: 1}}, Help: "Adjust this control"})
		value, err := decodeChoiceSettingPayload(definition, []byte{1})
		if err != nil {
			t.Fatal(err)
		}
		value.Provenance = observedSetting(true, true, true, definition.Help)
		info := ipcSettingInfo("headset", deviceSettingValue{Choice: &value})
		if value.Editable || info.Editable || info.Support.State != "read-only" || !strings.Contains(info.Help, "Read-only") || strings.Contains(info.Help, "Adjust") {
			t.Fatal("edit advice or permission leaked into release", info)
		}
		if !slices.Contains(info.Support.Sources, "hardware-report") {
			t.Fatal("locking writes erased report provenance")
		}
	}
	// Catalog binding still validates data independently of release permission.
	if _, supported := choiceSettingForCatalog(choiceSettingDefinition{Choices: []settingChoice{{Name: "15 min", Raw: 3, CatalogValue: "3"}}, Writable: true}, modelcatalog.Property{PossibleValues: []string{"3"}}, true); !supported {
		t.Fatal("legacy sleep binding changed")
	}
}

func TestStablePairingDefaultsRejectCachedAndRawReadsBeforeIO(t *testing.T) {
	preservePairingOption(t)
	link380CSearchConnectEnabled.Store(false)
	t.Setenv(experimentalWritesEnv, experimentalWritesAck)
	for _, pid := range []uint16{0x24c9, 0x24ca, 0x24e9, 0x2e56} {
		d := &jabra_DeviceInfo{deviceID: 0, instance: "default-off", productID: pid, isDongle: true, hidrawPath: "synthetic-no-device", searchConnect: true, pairingList: &pairingList{pairedDevices: []pairedDevice{{deviceName: "Cached", isConnected: true}}}}
		withDeviceState(t, devices{0: d}, -1, 0)
		if _, err := getPairingList(0); !errors.Is(err, ErrNotSupported) {
			t.Fatal("disabled list reached transport", pid, err)
		}
		if list := (&jabraAPIBridge{}).GetPairingList(); len(list) != 0 {
			t.Fatal("disabled list exposed cached entries", pid, list)
		}
		if err := setDongleInBTPairing(true); !errors.Is(err, ErrNotSupported) {
			t.Fatal("global write flag bypassed pairing policy", pid, err)
		}
	}
}

func TestOptedInServiceKeepsFindVisibleToClient(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, make(devices), -1, -1)
	preservePairingOption(t)
	link380CSearchConnectEnabled.Store(false) // The client never captures the option.
	old := currentTUIBackend()
	setTUIBackend(&tuiIPCBackend{})
	t.Cleanup(func() { setTUIBackend(old) })
	for _, granted := range []bool{false, true} {
		replaceTUIDeviceState([]ipc.DeviceInfo{{ID: 0, Instance: "service", Name: "Link 380c", IsDongle: true, PID: 0x24c9, Selected: true, SearchConnect: granted}}, nil, ipc.FeatureInfo{})
		updateStartMenu()
		found := false
		for _, item := range startMenu {
			found = found || item.id == 0
		}
		if found != granted || !rememberedPreviewIsReadOnly() {
			t.Fatal("client lost daemon permission or unlocked remembered actions", granted, found)
		}
	}
}
