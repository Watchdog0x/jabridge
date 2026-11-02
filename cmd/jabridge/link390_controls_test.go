package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func fullLink390Profile(t *testing.T, pid uint16) *modelcatalog.Capabilities {
	t.Helper()
	p := dongleProfileForTest(t, pid)
	p.Properties["radioPower"] = modelcatalog.Property{Kind: "Enum", PossibleValues: []string{"normal", "low", "veryLow"}, Access: "ReadWrite"}
	p.Properties["ringer"] = modelcatalog.Property{Kind: "Enum", PossibleValues: []string{"true", "false"}, Access: "ReadWrite"}
	p.Properties["softphoneIntegrationEnabled"] = modelcatalog.Property{Kind: "Enum", PossibleValues: []string{"true", "false"}, Access: "ReadWrite"}
	return p
}

func TestLink390ControlsModelBinding(t *testing.T) {
	for _, pid := range []uint16{0x2e56, 0x2e57} {
		t.Run(fmt.Sprintf("%x", pid), func(t *testing.T) {
			device, profile := dongleDeviceForTest(pid), fullLink390Profile(t, pid)
			choices := readChoiceSettingsSnapshot(device, choiceSettingDefinitions(settingScopeDongle), profile, nil, func(_ *jabra_DeviceInfo, d choiceSettingDefinition) ([]byte, error) {
				if d.Key == "headset-ringer" {
					return []byte{0, 1}, nil
				}
				return []byte{1}, nil
			})
			bools := readBoolSettingsSnapshot(device, settingScopeDongle, profile, nil, func(_ *jabra_DeviceInfo, d boolSettingDefinition) (bool, error) {
				if d.Key != "softphone-integration" {
					return false, errors.New("not in exact model")
				}
				return true, nil
			})
			if len(choices) != 4 || len(bools) != 1 || !bools[0].Editable || !bools[0].Definition.Link390Config {
				t.Fatal("missing exact controls", choices, bools)
			}
			for _, v := range choices {
				info := ipcSettingInfo("dongle", deviceSettingValue{Choice: &v})
				if !info.Editable || slices.Contains(info.Support.Sources, "hardware-report") {
					t.Fatal(info)
				}
				if (v.Definition.Key == "wireless-range") != info.MayRestart {
					t.Fatal("restart warning", info)
				}
			}
			info := ipcSettingInfo("dongle", deviceSettingValue{Boolean: &bools[0]})
			if !info.MayRestart || slices.Contains(info.Support.Sources, "hardware-report") {
				t.Fatal(info)
			}
			for _, fw := range []string{"1.3.0", "1.4.2", "3.0.0", "3.0.13", ""} {
				other := cloneDeviceInfo(device)
				other.firmwareVersion = fw
				d := bindLink390ConfigBool(other, bools[0].Definition, profile, nil)
				if isLink390ConfigBool(other, d) {
					t.Fatal("wrong firmware enabled", fw)
				}
			}
		})
	}
}

func TestLink390RingerSelectorAndUnknownValue(t *testing.T) {
	device, profile := dongleDeviceForTest(0x2e57), fullLink390Profile(t, 0x2e57)
	var d choiceSettingDefinition
	for _, candidate := range dongleChoiceSettingDefinitions {
		if candidate.Key == "headset-ringer" {
			d = candidate
		}
	}
	for _, bad := range [][]byte{nil, {0}, {1, 0}, {1, 1}, {0, 1, 0}} {
		if _, err := decodeChoiceSettingPayload(d, bad); err == nil {
			t.Fatal("accepted malformed selector/length", bad)
		}
	}
	rows := readChoiceSettingsSnapshot(device, []choiceSettingDefinition{d}, profile, nil, func(*jabra_DeviceInfo, choiceSettingDefinition) ([]byte, error) { return []byte{0, 2}, nil })
	if len(rows) != 1 || rows[0].Editable || !strings.Contains(rows[0].Provenance.Reason, "does not list") {
		t.Fatal(rows)
	}
}

func TestLink390ControlsAllSettersUseVerifiedFlow(t *testing.T) {
	for _, mode := range []string{"cli-choice", "cli-bool", "low-choice", "low-bool"} {
		t.Run(mode, func(t *testing.T) {
			sim := newLink390Sim(t, "setter")
			installLink390Sim(t, sim)
			device := link390SimRegistry(t, sim)
			profile := fullLink390Profile(t, device.productID)
			if strings.HasSuffix(mode, "choice") {
				rows := readChoiceSettingsSnapshot(device, choiceSettingDefinitions(settingScopeDongle), profile, nil, func(_ *jabra_DeviceInfo, d choiceSettingDefinition) ([]byte, error) {
					if d.Key == "headset-ringer" {
						return []byte{0, 1}, nil
					}
					return []byte{1}, nil
				})
				var setting choiceSettingValue
				for _, v := range rows {
					if v.Definition.Key == "wireless-range" {
						setting = v
					}
				}
				var err error
				if mode == "low-choice" {
					err = writeChoiceSetting(device, setting, 2)
				} else {
					_, _, err = applyDeviceSettingFromText(device, deviceSettingValue{Choice: &setting}, "very-low")
				}
				if err != nil {
					t.Fatal(err)
				}
			} else {
				def, _ := findBoolSettingDefinition(settingScopeDongle, "softphone-integration")
				def = bindLink390ConfigBool(device, def, profile, nil)
				var err error
				if mode == "low-bool" {
					err = writeBoolSetting(device, def, false)
				} else {
					_, _, err = applyDeviceSettingFromText(device, deviceSettingValue{Boolean: &boolSettingValue{Definition: def, Value: true, Editable: true}}, "off")
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if got := sim.list(); strings.Count(got, "write:") != 1 || strings.Count(got, "end@") != 1 || !strings.HasPrefix(got, "permission@") {
				t.Fatal("bypassed verified transaction", got)
			}
			assertLink390Idle(t)
		})
	}
}

// The API must return the verified new attachment instead of resolving by PID.
func TestLink390ControlsBackendReturnsReconnectedTarget(t *testing.T) {
	sim := newLink390Sim(t, "backend")
	installLink390Sim(t, sim)
	device := link390SimRegistry(t, sim)
	profile := fullLink390Profile(t, device.productID)
	deviceModelMu.Lock()
	old := deviceModelCache
	deviceModelCache = map[string]modelCacheEntry{fmt.Sprintf("%04x:%s:%s", device.productID, device.variantType, device.firmwareVersion): {capabilities: profile}}
	deviceModelMu.Unlock()
	t.Cleanup(func() { deviceModelMu.Lock(); deviceModelCache = old; deviceModelMu.Unlock() })
	sim.endMode = "lost"
	sim.onFrame = func(frame string) {
		if strings.HasPrefix(frame, "end@") {
			sim.restart("/dev/simhidraw-backend2")
		}
	}
	target, previous := settingTarget(device), "Normal"
	api := &jabraAPIBridge{}
	updated, err := api.setSettingChecked("dongle", "wireless-range", "low", target, &previous)
	if err != nil || updated.Value != "Low" || updated.Target == nil || updated.Target.Instance == device.instance || updated.Target.Instance != deviceForID(7).instance {
		t.Fatal(updated, err)
	}
	if strings.Count(sim.list(), "write:") != 1 {
		t.Fatal(sim.list())
	}
}

func TestLink390ControlsRefuseForgedRowsBeforeLease(t *testing.T) {
	device, profile := dongleDeviceForTest(0x2e57), fullLink390Profile(t, 0x2e57)
	definitions := choiceSettingDefinitions(settingScopeDongle)
	for _, key := range []string{"wireless-range", "headset-ringer"} {
		for _, d := range definitions {
			if d.Key != key {
				continue
			}
			d.ExpectedFirmware = device.firmwareVersion
			value := choiceSettingValue{Definition: d, Editable: true, ChoiceIndex: 0, Raw: d.Choices[0].Raw}
			state := t.TempDir()
			t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", state)
			for _, target := range []*jabra_DeviceInfo{nil, {instance: "headset"}, {isDongle: true, productID: 0x24c7}} {
				if err := writeChoiceSetting(target, value, 0); !errors.Is(err, ErrNotSupported) {
					t.Fatal(key, err)
				}
				if _, _, err := applyDeviceSettingFromText(target, deviceSettingValue{Choice: &value}, d.Choices[0].Name); !errors.Is(err, ErrNotSupported) {
					t.Fatal(key, err)
				}
			}
			forged := value
			forged.Definition.DongleExactProfile = false
			if err := writeChoiceSetting(device, forged, 0); !errors.Is(err, ErrNotSupported) {
				t.Fatal(key, err)
			}
			property, _ := firstCatalogProperty(profile, d.CatalogProperties)
			property.Access = "ReadOnly"
			bound, ok := choiceSettingForCatalog(d, property, true)
			if !ok {
				t.Fatal(key)
			}
			forged.Definition = bound
			forged.Editable = true
			if _, _, err := applyDeviceSettingFromText(device, deviceSettingValue{Choice: &forged}, d.Choices[0].Name); !errors.Is(err, ErrNotSupported) {
				t.Fatal("read-only row forged", key, err)
			}
			if entries, err := os.ReadDir(state); err != nil || len(entries) != 0 {
				t.Fatal("refused row acquired a lease", key, entries, err)
			}
		}
	}
	boolDef, _ := findBoolSettingDefinition(settingScopeDongle, "softphone-integration")
	boolDef = bindLink390ConfigBool(device, boolDef, profile, nil)
	for _, target := range []*jabra_DeviceInfo{nil, {instance: "headset"}, {isDongle: true, productID: 0x24c7}} {
		forged := deviceSettingValue{Boolean: &boolSettingValue{Definition: boolDef, Editable: true}}
		if _, _, err := applyDeviceSettingFromText(target, forged, "on"); !errors.Is(err, ErrNotSupported) {
			t.Fatal(err)
		}
	}
}

func TestLink390OfflineListingUsesOneCatalogLookup(t *testing.T) {
	tripwire := &settingLookupTripwire{}
	oldClient, oldCache := deviceModelClient, deviceModelCache
	deviceModelClient = modelcatalog.NewClient()
	deviceModelClient.HTTPClient = &http.Client{Transport: tripwire}
	deviceModelCache = map[string]modelCacheEntry{}
	t.Cleanup(func() { deviceModelClient, deviceModelCache = oldClient, oldCache })
	_ = readSupportedDeviceSettings(dongleDeviceForTest(0x2e57), settingScopeDongle)
	if tripwire.calls != 1 {
		t.Fatal("offline listing repeated catalog lookup", tripwire.calls)
	}
}
