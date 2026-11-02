package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func editableProfileForTest(t *testing.T) *modelcatalog.Capabilities {
	t.Helper()
	fixture, err := os.ReadFile("../../internal/modelcatalog/testdata/evolve3-65-1.0.1.json")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bundles" {
			_, _ = w.Write([]byte(`{"unbundledProducts":[{"variants":[{"vendorId":2830,"productId":9545,"variantType":"01-A3"}],"firmwareReleases":[{"version":"1.0.1"}]}]}`))
			return
		}
		_, _ = w.Write(fixture)
	}))
	defer server.Close()
	client := modelcatalog.NewClient()
	client.BundlesURL, client.ModelsBaseURL, client.HTTPClient = server.URL+"/bundles", server.URL, server.Client()
	profile, err := client.Lookup(context.Background(), 0x2549, "01-A3", "1.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func editableSnapshotForTest(t *testing.T, profile *modelcatalog.Capabilities, noise []byte, routeFirmware string) []deviceSettingValue {
	t.Helper()
	parent := &jabra_DeviceInfo{deviceID: 1, isDongle: true, productID: 0x2e56, firmwareVersion: routeFirmware, instance: "parent", hidrawPath: "simulated-parent"}
	child := &jabra_DeviceInfo{deviceID: 2, productID: 0x2549, variantType: "01-A3", firmwareVersion: "1.0.1", deviceConnection: deviceConnectionType_BT, parentDeviceID: 1, instance: "child"}
	withDeviceState(t, devices{1: parent, 2: child}, 2, 1)
	var values []deviceSettingValue
	for _, value := range readBoolSettingsSnapshot(child, settingScopeHeadset, profile, nil, func(_ *jabra_DeviceInfo, definition boolSettingDefinition) (bool, error) {
		if definition.Key != "power-saving" {
			return false, ErrNotSupported
		}
		return true, nil
	}) {
		copy := value
		values = append(values, deviceSettingValue{Boolean: &copy})
	}
	flat := equalizerBytes(t, equalizerFlatReply)
	for _, value := range readChoiceSettingsSnapshot(child, choiceSettingDefinitions(settingScopeHeadset), profile, nil, func(_ *jabra_DeviceInfo, definition choiceSettingDefinition) ([]byte, error) {
		if definition.Key == "noise-cycle" {
			return noise, nil
		}
		if isEqualizerBand(definition.Key) {
			return flat, nil
		}
		return nil, ErrNotSupported
	}) {
		copy := value
		values = append(values, deviceSettingValue{Choice: &copy})
	}
	return values
}

type editablePolicyAPI struct {
	jabraAPIBridge
	values []deviceSettingValue
	device *jabra_DeviceInfo
	scope  string
	calls  atomic.Int32
}

func (a *editablePolicyAPI) ListSettings(string) ([]ipc.SettingInfo, error) {
	values := make([]ipc.SettingInfo, 0, len(a.values))
	scope := a.scope
	if scope == "" {
		scope = "headset"
	}
	for _, setting := range a.values {
		values = append(values, ipcSettingInfo(scope, setting))
	}
	return values, nil
}

func (a *editablePolicyAPI) SetSetting(_, key, value string) (ipc.SettingInfo, error) {
	a.calls.Add(1)
	setting, ok := findDeviceSettingValue(a.values, key)
	if !ok {
		return ipc.SettingInfo{}, ErrNotSupported
	}
	if _, _, err := applyDeviceSettingFromText(a.device, setting, value); err != nil {
		return ipc.SettingInfo{}, err
	}
	return ipcSettingInfo("headset", setting), nil
}

func policyIPCClient(t *testing.T, api ipc.API) *ipc.Client {
	t.Helper()
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "policy.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		if conn, err := listener.Accept(); err == nil {
			ipc.HandleConnection(conn, api)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := ipc.Dial(ctx, listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(); <-done })
	return client
}

func TestModelBoundControlsEditableThroughIPCWithoutFlags(t *testing.T) {
	t.Setenv("JABRIDGE_HISTORY", "off")
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	profile := editableProfileForTest(t)
	api := &editablePolicyAPI{values: editableSnapshotForTest(t, profile, []byte{3, 2, 1, 0}, "3.2.0")}
	client := policyIPCClient(t, api)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var settings []ipc.SettingInfo
	if err := client.Call(ctx, "settings.list", map[string]string{"device": "headset"}, &settings); err != nil {
		t.Fatal(err)
	}
	if len(settings) != 7 {
		t.Fatal("missing implemented controls", len(settings))
	}
	for _, setting := range settings {
		if !setting.Editable || setting.Support.State != "available" || !slices.Contains(setting.Support.Sources, "exact-firmware-profile") {
			t.Fatal("exact implemented setting remained read-only", setting)
		}
		if reported := slices.Contains(setting.Support.Sources, "hardware-report"); reported != (setting.Key == "noise-cycle") {
			t.Fatal("invented or lost physical report", setting)
		}
	}
	for _, extra := range []string{"editable", "force"} {
		params := map[string]any{"device": "headset", "key": "power-saving", "value": "on", extra: true}
		if err := client.Call(ctx, "settings.set", params, nil); err == nil {
			t.Fatal("client forged a service permission", extra)
		}
	}
	if api.calls.Load() != 0 {
		t.Fatal("extra parameter reached API")
	}
	if err := client.Call(ctx, "settings.set", map[string]string{"device": "headset", "key": "power-saving", "value": "on"}, nil); err != nil || api.calls.Load() != 1 {
		t.Fatal("ordinary unchanged request needed an unlock or device access", err)
	}
}

func TestSnapshotRefusalsExplainWhyWithoutAdjustmentAdvice(t *testing.T) {
	for _, cause := range []string{"fallback", "unread firmware", "vendor read-only", "wrong boolean choices", "unsupported equalizer"} {
		t.Run(cause, func(t *testing.T) {
			profile := editableProfileForTest(t)
			reason := "exact firmware"
			switch cause {
			case "fallback":
				profile.ExactFirmwareProfile = false
			case "unread firmware":
				profile.DeviceFirmware = ""
			case "vendor read-only":
				for key, property := range profile.Properties {
					property.Access = "ReadOnly"
					profile.Properties[key] = property
				}
				reason = "marks this control read-only"
			case "wrong boolean choices":
				property := profile.Properties["powerSaveEnabled"]
				property.PossibleValues = []string{"yes", "no"}
				profile.Properties["powerSaveEnabled"] = property
				reason = "on/off"
			case "unsupported equalizer":
				property := profile.Properties["equalizerConfig"]
				property.CompositeSettings = nil
				profile.Properties["equalizerConfig"] = property
				reason = "Not supported on this firmware"
			}
			values := editableSnapshotForTest(t, profile, []byte{3, 2, 1, 0}, "3.2.0")
			checked := 0
			for _, setting := range values {
				if cause == "wrong boolean choices" && setting.key() != "power-saving" || cause == "unsupported equalizer" && setting.key() != "equalizer" {
					continue
				}
				checked++
				if setting.editable() || strings.Contains(setting.help(), "Adjust") || !strings.Contains(setting.help()+" "+setting.valueName(), reason) {
					t.Fatal("bad read-only state or reason", setting.key(), setting.help())
				}
			}
			if checked == 0 {
				t.Fatal("no refusal was exercised")
			}
		})
	}
}

func TestNoiseCycleSnapshotOffersOnlyMeaningfulChanges(t *testing.T) {
	profile := editableProfileForTest(t)
	for _, current := range [][]byte{{3, 2, 1, 0}, {2, 2, 1}} {
		values := editableSnapshotForTest(t, profile, current, "different-dongle-firmware")
		value, ok := findDeviceSettingValue(values, "noise-cycle")
		if !ok {
			t.Fatal("cycle missing")
		}
		if value.editable() != (len(current) == 3) {
			t.Fatal("no-op cycle was offered as an edit, or full cycle was lost", value.help())
		}
		if len(current) == 4 && !strings.Contains(value.help(), "Other combinations") {
			t.Fatal("single-choice cycle had no explanation")
		}
	}
	definition := equalizerDefinitionForTest(t, 0)
	loaded, err := decodeChoiceSettingPayload(definition, equalizerBytes(t, equalizerFlatReply))
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", state)
	index, ok := findChoiceIndex(definition, "3")
	if !ok {
		t.Fatal("gain missing")
	}
	if err := writeChoiceSetting(nil, loaded, index); err == nil || errors.Is(err, ErrNotSupported) {
		t.Fatal("missing bound firmware was not refused before transport", err)
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatal("missing firmware acquired a lease", entries, err)
	}
}
