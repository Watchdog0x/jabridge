package main

import (
	"context"
	"errors"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

type editorTestAPI struct {
	jabraAPIBridge
	mu       sync.Mutex
	value    string
	instance string
}

func (a *editorTestAPI) ListDevices() []ipc.DeviceInfo {
	return []ipc.DeviceInfo{{ID: 1, PID: 0x4052, Name: "Simulated headset", Connection: "usb", Selected: true, Firmware: "1.2.3"}}
}
func (a *editorTestAPI) GetFeatures() ipc.FeatureInfo           { return ipc.FeatureInfo{} }
func (a *editorTestAPI) GetPairingList() []ipc.PairedDeviceInfo { return nil }
func (a *editorTestAPI) ListSettings(string) ([]ipc.SettingInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return []ipc.SettingInfo{
		{Device: "headset", Key: "mute-button", Label: "Mute button", Value: "Mute", Kind: "choice", Editable: true, Choices: []string{"Busylight", "Call handling", "Mute", "Push to talk", "Speed dial", "None"}, Help: "Choose what this button does.", Target: &ipc.SettingTarget{ID: 1, Instance: a.instance}},
		{Device: "headset", Key: "device-name", Label: "Device name", Value: a.value, Kind: "text", Editable: true, MaxBytes: 32, Target: &ipc.SettingTarget{ID: 1, Instance: a.instance}},
	}, nil
}
func (a *editorTestAPI) SetSettingTarget(device, key, value string, target ipc.SettingTarget, previous string) (ipc.SettingInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if device != "headset" || target.ID != 1 || target.Instance != a.instance {
		return ipc.SettingInfo{}, errors.New("device changed")
	}
	if key == "device-name" {
		if previous != a.value {
			return ipc.SettingInfo{}, errors.New("setting changed")
		}
		a.value = value
	}
	return ipc.SettingInfo{Device: device, Key: key, Value: value, Target: &target}, nil
}

func TestBoundSettingIPCGuardsPreviousValueAndAttachment(t *testing.T) {
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "editor.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	api := &editorTestAPI{value: "Original", instance: strings.Repeat("a", 32)}
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
	params := map[string]any{"device": "headset", "key": "device-name", "value": "New name", "previous": "Original", "target": ipc.SettingTarget{ID: 1, Instance: strings.Repeat("a", 32)}}
	var response ipc.SettingInfo
	if err := client.Call(ctx, "settings.set", params, &response); err != nil {
		t.Fatal(err)
	}
	if response.Value != "New name" {
		t.Fatal(response)
	}
	params["value"] = "Overwrite"
	if err := client.Call(ctx, "settings.set", params, &response); err == nil {
		t.Fatal("stale previous value was accepted")
	}
	params["previous"] = "New name"
	api.mu.Lock()
	api.instance = strings.Repeat("b", 32)
	api.mu.Unlock()
	if err := client.Call(ctx, "settings.set", params, &response); err == nil {
		t.Fatal("old attachment was accepted")
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.value != "New name" {
		t.Fatal("rejected edit changed data")
	}
}

// Optional developer-only IPC fixture for exercising the real TUI process.
// The client must run with JABRIDGE_HISTORY=off; no real hardware is used.
func TestEditorManualHarness(t *testing.T) {
	socket := os.Getenv("JABRIDGE_TEST_EDITOR_SOCKET")
	if socket == "" {
		t.Skip("manual TUI fixture not requested")
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() { <-ctx.Done(); _ = listener.Close() }()
	api := &editorTestAPI{value: "Demo headset", instance: strings.Repeat("a", 32)}
	bus := ipc.NewEventBus()
	t.Log("Simulated headset IPC ready")
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			t.Fatal(err)
		}
		go ipc.HandleConnectionWithBus(conn, api, bus, time.Minute)
	}
}
