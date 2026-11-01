package main

import (
	"bytes"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

type cliOwnerTestAPI struct {
	jabraAPIBridge
	writes chan string
}

func (a *cliOwnerTestAPI) ListDevices() []ipc.DeviceInfo {
	return []ipc.DeviceInfo{{ID: 7, PID: 0x0422, Name: "Speaker", Selected: true, Connection: "usb", Firmware: "2.32.8"}}
}
func (a *cliOwnerTestAPI) SetSetting(device, key, value string) (ipc.SettingInfo, error) {
	a.writes <- device + "." + key + "=" + value
	return ipc.SettingInfo{Key: key, Value: value}, nil
}

func TestCLIUsesExistingSocketForStatusAndSettingWrites(t *testing.T) {
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "service.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	api := &cliOwnerTestAPI{writes: make(chan string, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			ipc.HandleConnection(conn, api)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := ipc.Dial(ctx, listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close(); <-done }()
	var out bytes.Buffer
	if err := runServiceCLI(client, []string{"status"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2.32.8") || !strings.Contains(out.String(), "0422") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := runServiceCLI(client, []string{"settings", "set", "headset.speed-dial", ""}, &out); err != nil {
		t.Fatal(err)
	}
	if got := <-api.writes; got != "headset.speed-dial=" {
		t.Fatal(got)
	}
}

type metadataCLIAPI struct{ cliOwnerTestAPI }

func (a *metadataCLIAPI) ListSettings(string) ([]ipc.SettingInfo, error) {
	return []ipc.SettingInfo{
		{Key: "sidetone", Label: "Sidetone", Kind: "boolean", Value: "On", Editable: true, Choices: []string{"Off", "On"}, Help: "Hear your own voice.", Group: "Audio", RequiresRestart: restartFlag(true)},
		{Key: "name", Label: "Name", Kind: "text", Value: "My Name", Help: "Name shown to other devices.", RequiresRestart: restartFlag(false)},
		{Key: "unknown", Value: "On"},
		{Key: "legacy", Value: "Off", MayRestart: true},
	}, nil
}

func (a *metadataCLIAPI) SetSetting(_, key, value string) (ipc.SettingInfo, error) {
	return ipc.SettingInfo{Key: key, Value: value, Group: "Audio", RequiresRestart: restartFlag(true)}, nil
}

func metadataCLIClient(t *testing.T) *ipc.Client {
	t.Helper()
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "metadata.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		if conn, err := listener.Accept(); err == nil {
			ipc.HandleConnection(conn, &metadataCLIAPI{})
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

func TestServiceSettingsMetadataKeepsValueLinesAndExplainsRestart(t *testing.T) {
	client := metadataCLIClient(t)
	var out bytes.Buffer
	if err := runServiceCLI(client, []string{"settings"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  headset.sidetone = On (editable; choices: Off, On)\n",
		"  headset.name = My Name (read only)\n", "    Group: Audio\n",
		"    Hear your own voice.\n", "    Name shown to other devices.\n",
		"    Jabra's model data says this change restarts the device.\n",
		"    Saving may restart this device. Do not edit during a call.\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
	out.Reset()
	if err := runServiceCLI(client, []string{"settings", "set", "headset.sidetone", "off"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "headset.sidetone = off (read back from device)\n") || !strings.Contains(out.String(), "model data says this change restarts") {
		t.Fatal(out.String())
	}
}

func TestSettingsMetadataReachesTUIThroughRealLocalIPC(t *testing.T) {
	client := metadataCLIClient(t)
	withDeviceState(t, devices{7: {deviceID: 7, deviceName: "Fixture", deviceConnection: deviceConnectionType_USB}}, 7, -1)
	old := currentTUIBackend()
	setTUIBackend(&tuiIPCBackend{client: client})
	t.Cleanup(func() { setTUIBackend(old) })
	_, values, err := loadIPCSettings(settingScopeHeadset)
	if err != nil || len(values) != 4 {
		t.Fatal(len(values), err)
	}
	if values[0].group() != "Audio" || values[0].requiresRestart() == nil || !*values[0].requiresRestart() || values[0].needsConfigMode() {
		t.Fatal("catalog metadata did not survive IPC")
	}
	if values[1].requiresRestart() == nil || *values[1].requiresRestart() || values[2].requiresRestart() != nil || !values[3].needsConfigMode() {
		t.Fatal("false, unknown or legacy mayRestart changed")
	}
}
