package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

type controllerEditorTestAPI struct{ editorTestAPI }

func (a *controllerEditorTestAPI) ListDevices() []ipc.DeviceInfo {
	return []ipc.DeviceInfo{{ID: 1, PID: 0x4052, Name: "Simulated Engage setup", Connection: "usb", Selected: true, Parts: []ipc.ControlPartInfo{{Role: "headset", Address: 1, Variant: "01-72", Ready: true}, {Role: "controller", Address: 3, Variant: "03-05", Ready: true}}}}
}
func (a *controllerEditorTestAPI) ListSettings(scope string) ([]ipc.SettingInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	target := &ipc.SettingTarget{ID: 1, Instance: a.instance, Topology: strings.Repeat("b", 32)}
	controller := ipc.SettingInfo{Device: "controller", Component: "controller", Key: "controller-name", Label: "Controller name", Value: a.value, Kind: "text", MaxBytes: 32, Editable: true, Target: target}
	button := ipc.SettingInfo{Device: "controller", Component: "controller", Key: "call-button", Label: "Call button", Value: "Mute", Kind: "choice", Choices: []string{"None", "Mute", "Call handling", "Push to talk"}, Editable: true, Target: target}
	if scope == "controller" {
		return []ipc.SettingInfo{controller, button}, nil
	}
	return []ipc.SettingInfo{{Device: "headset", Component: "headset", Key: "sidetone", Label: "Sidetone", Value: "On", Kind: "boolean", Choices: []string{"Off", "On"}, Editable: true, Target: target}, controller, button}, nil
}
func (a *controllerEditorTestAPI) SetSettingTarget(device, key, value string, target ipc.SettingTarget, previous string) (ipc.SettingInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if device != "controller" || target.ID != 1 || target.Instance != a.instance || target.Topology != strings.Repeat("b", 32) {
		return ipc.SettingInfo{}, errors.New("part changed")
	}
	if key == "controller-name" {
		if previous != a.value {
			return ipc.SettingInfo{}, errors.New("setting changed")
		}
		a.value = value
	}
	return ipc.SettingInfo{Device: device, Component: "controller", Key: key, Value: value, Target: &target}, nil
}

func TestControllerIPCAndTUIListsKeepTheirOwnSettings(t *testing.T) {
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "parts.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	api := &controllerEditorTestAPI{editorTestAPI: editorTestAPI{value: "Original", instance: strings.Repeat("a", 32)}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			ipc.HandleConnection(conn, api)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, err := ipc.Dial(ctx, listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close(); <-done }()
	withDeviceState(t, nil, -1, -1)
	oldBackend := currentTUIBackend()
	setTUIBackend(&tuiIPCBackend{client: client})
	defer setTUIBackend(oldBackend)
	if err := syncTUIState(client); err != nil {
		t.Fatal(err)
	}
	_, headset, err := loadIPCSettings(settingScopeHeadset)
	if err != nil || len(headset) != 1 || headset[0].key() != "sidetone" {
		t.Fatal("headset list", len(headset), err)
	}
	_, controller, err := loadIPCSettings(settingScopeController)
	if err != nil || len(controller) != 2 || controller[0].Remote.Target.Topology == "" {
		t.Fatal("controller list", len(controller), err)
	}
	if err := setIPCSetting(controller[0], "Work control"); err != nil {
		t.Fatal(err)
	}
	if err := setIPCSetting(controller[0], "stale overwrite"); err == nil {
		t.Fatal("stale value accepted")
	}
	_, controller, err = loadIPCSettings(settingScopeController)
	if err != nil {
		t.Fatal(err)
	}
	controller[0].Remote.Target.Topology = strings.Repeat("c", 32)
	if err := setIPCSetting(controller[0], "wrong part"); err == nil {
		t.Fatal("changed topology accepted")
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.value != "Work control" {
		t.Fatal(api.value)
	}
}

func TestCombinedModelUsesHeadsetIdentityNotFirstResponder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bundles.json" {
			_, _ = fmt.Fprint(w, `{"unbundledProducts":[{"productName":"Engage fixture","variants":[{"vendorId":2830,"productId":16466,"variantType":"01-72","fwuProtocolId":4}],"firmwareReleases":[{"version":"4.1.3"}]}]}`)
			return
		}
		if strings.Contains(r.URL.Path, "/variants/01-72/") {
			_, _ = fmt.Fprint(w, `{"device":{"settings":[{"sdkProperties":["sidetoneEnabled"],"possibleValues":[{"value":true},{"value":false}]},{"sdkProperties":["controllerName"]}]}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	oldClient, oldCache := deviceModelClient, deviceModelCache
	defer func() { deviceModelClient, deviceModelCache = oldClient, oldCache }()
	deviceModelClient = &modelcatalog.Client{HTTPClient: server.Client(), BundlesURL: server.URL + "/bundles.json", ModelsBaseURL: server.URL, ModelName: "test", SchemaVersion: "1"}
	deviceModelCache = map[string]modelCacheEntry{}
	d := engageFixture(0x4052)
	d.variantType = "03-05" // RC23's first-responder identity must not win.
	catalog, err := lookupDeviceModel(d)
	if err != nil || catalog.Variant != "01-72" || catalog.PID != 0x4052 || len(catalog.Properties) != 2 {
		t.Fatal(catalog, err)
	}
}

func TestLateHeadsetLoadCannotOverwriteControllerScreen(t *testing.T) {
	oldScope, oldGeneration := headsetSettingsScope, headsetSettingsGeneration
	oldValues, oldLines := headsetSettingsValues, headsetSettingsLines
	defer func() {
		headsetSettingsScope, headsetSettingsGeneration = oldScope, oldGeneration
		headsetSettingsValues, headsetSettingsLines = oldValues, oldLines
	}()
	headsetSettingsScope, headsetSettingsGeneration = settingScopeController, 8
	headsetSettingsValues = nil
	for _, load := range []*settingsLoadResult{{scope: settingScopeHeadset, generation: 8}, {scope: settingScopeController, generation: 7}} {
		load.values = []deviceSettingValue{{Remote: &remoteSettingValue{Key: "wrong-screen"}}}
		applyActionResult(actionResult{settingsLoad: load}, make(chan actionResult, 1))
		if len(headsetSettingsValues) != 0 {
			t.Fatal("stale settings replaced controller screen")
		}
	}
}
