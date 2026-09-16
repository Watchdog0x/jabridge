package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/daemon/pipewire"
	"github.com/Watchdog0x/jabridge/internal/headsetvolume"
	"github.com/Watchdog0x/jabridge/internal/history"
)

type volumeClientAPI struct {
	jabraAPIBridge
	devices    []ipc.DeviceInfo
	calls      int
	percent    int
	target     ipc.SettingTarget
	mismatched bool
	reads      int
	infos      int
}

func TestHeadsetVolumePanicHistory(t *testing.T) {
	if os.Getenv("JABRIDGE_TEST_VOLUME_PANIC_CHILD") != "1" {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		t.Setenv("STATE_DIRECTORY", "")
		t.Setenv("JABRIDGE_HISTORY", "on")
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(self, "-test.run=^TestHeadsetVolumePanicHistory$")
		cmd.Env = append(os.Environ(), "JABRIDGE_TEST_VOLUME_PANIC_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(output), err)
		}
		return
	}
	configureHistory()
	device := &jabra_DeviceInfo{deviceID: 2, instance: strings.Repeat("a", 32), productID: headsetvolume.ProductID, firmwareVersion: headsetvolume.Firmware, deviceConnection: deviceConnectionType_USB}
	withDeviceState(t, devices{2: device}, 2, -1)
	openHeadsetVolume = func(context.Context, *headsetvolume.Attachment, uint16, string) (*headsetvolume.USB, error) {
		panic("synthetic volume transport panic")
	}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = (&jabraAPIBridge{}).SetHeadsetVolume(*settingTarget(device), 50)
	}()
	if recovered != "synthetic volume transport panic" {
		t.Fatal("original panic was swallowed", recovered)
	}
	directory, err := history.Directory()
	if err != nil {
		t.Fatal(err)
	}
	events, _, err := (&history.Recorder{Dir: directory}).Read(200)
	if err != nil {
		t.Fatal(err)
	}
	var volume []history.Event
	for _, event := range events {
		if event.Action == "volume" {
			volume = append(volume, event)
		}
	}
	if len(volume) != 2 || volume[0].Phase != "start" || volume[1].Phase != "error" || volume[1].Error != "panic" || volume[0].Operation != volume[1].Operation {
		t.Fatal("panic was recorded as success", volume)
	}
}

func (a *volumeClientAPI) ListDevices() []ipc.DeviceInfo { return a.devices }
func (a *volumeClientAPI) SetHeadsetVolume(target ipc.SettingTarget, percent int) (headsetvolume.Value, error) {
	a.calls++
	a.target = target
	a.percent = percent
	if a.mismatched {
		return headsetvolume.Value{Percent: percent - 1}, nil
	}
	return headsetvolume.Value{Percent: percent}, nil
}
func (a *volumeClientAPI) GetHeadsetVolume(target ipc.SettingTarget) (headsetvolume.Value, error) {
	a.reads++
	a.target = target
	return headsetvolume.Value{Percent: 25, Source: "saved"}, nil
}
func (a *volumeClientAPI) HeadsetVolumeCapabilities(target ipc.SettingTarget) (headsetvolume.Capabilities, error) {
	a.infos++
	a.target = target
	return headsetvolume.ForDevice(0x0e36, "1.11.0", "usb"), nil
}
func (a *volumeClientAPI) ChangeSound(pipewire.SoundTarget, string, int, string) (pipewire.SoundNode, error) {
	return pipewire.SoundNode{}, errors.New("headset volume reached PipeWire")
}
func (a *volumeClientAPI) GetSound() pipewire.SoundState { return pipewire.SoundState{} }

func TestHeadsetVolumeCLIUsesBoundDeviceNotPipeWire(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "volume.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	a := &volumeClientAPI{devices: []ipc.DeviceInfo{{ID: 2, Instance: strings.Repeat("a", 32), PID: headsetvolume.ProductID, Firmware: headsetvolume.Firmware, Connection: "usb", Selected: true}}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			ipc.HandleConnection(conn, a)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := ipc.Dial(ctx, listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close(); <-done }()
	for _, args := range [][]string{{"volume", "50"}, {"volume", "0"}} {
		percent, err := parseHeadsetVolume(args)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runHeadsetVolumeClient(client, percent, &out); err != nil {
			t.Fatal(err)
		}
		if a.target.ID != 2 || a.target.Instance != strings.Repeat("a", 32) {
			t.Fatal("selection was not bound")
		}
		if !strings.Contains(out.String(), "accepted volume request") {
			t.Fatal("inaccurate volume claim", out.String())
		}
	}
	if a.calls != 2 {
		t.Fatal("incorrect request count")
	}
	for _, args := range [][]string{{"volume"}, {"volume", "get"}, {"volume", "info"}} {
		command, err := parseHeadsetVolumeCommand(args)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runHeadsetVolumeCommand(client, command, &out); err != nil {
			t.Fatal(err)
		}
		if command.Action == "get" && (!strings.Contains(out.String(), "Saved headset volume: 25%") || !strings.Contains(out.String(), "may differ")) {
			t.Fatal("saved reading was described as live", out.String())
		}
		if command.Action == "info" && !strings.Contains(out.String(), "Set percentage: true") {
			t.Fatal(out.String())
		}
	}
	if a.calls != 2 || a.reads != 2 || a.infos != 1 {
		t.Fatal("read or capability query changed volume", a.calls, a.reads, a.infos)
	}
	a.mismatched = true
	var out bytes.Buffer
	if err := runHeadsetVolumeClient(client, 50, &out); err == nil || !strings.Contains(err.Error(), "reply does not match") || out.Len() != 0 {
		t.Fatal("mismatched reply reported success", err, out.String())
	}
}

func TestHeadsetVolumeCapabilityLookupDoesNotProbeOrFallbackToDongle(t *testing.T) {
	device := &jabra_DeviceInfo{deviceID: 2, instance: strings.Repeat("a", 32), productID: headsetvolume.ProductID, firmwareVersion: headsetvolume.Firmware, deviceConnection: deviceConnectionType_USB}
	withDeviceState(t, devices{2: device}, 2, -1)
	api := &jabraAPIBridge{}
	// No USB attachment exists. A metadata-only query must still work.
	c, err := api.HeadsetVolumeCapabilities(*settingTarget(device))
	if err != nil || c.Read != "saved" || !c.SetPercent {
		t.Fatal(c, err)
	}
	device.deviceConnection = deviceConnectionType_BT
	c, err = api.HeadsetVolumeCapabilities(*settingTarget(device))
	if err != nil || c.SetPercent || c.Read != "unavailable" {
		t.Fatal("USB profile authorized a dongle path", c, err)
	}
	if _, err := api.SetHeadsetVolume(*settingTarget(device), 50); err == nil || !strings.Contains(err.Error(), "dongle") {
		t.Fatal("unverified dongle route reached a write", err)
	}
	if _, err := api.GetHeadsetVolume(*settingTarget(device)); err == nil || !strings.Contains(err.Error(), "dongle") {
		t.Fatal("unverified dongle route reached a query", err)
	}
	if _, err := api.GetHeadsetVolume(ipc.SettingTarget{ID: 2, Instance: strings.Repeat("b", 32)}); err == nil {
		t.Fatal("stale saved-volume target accepted")
	}
}

func TestHeadsetVolumeRejectsInvalidInputAndStaleSelection(t *testing.T) {
	for _, args := range [][]string{nil, {"volume"}, {"other"}, {"volume", "-1"}, {"volume", "101"}, {"volume", "50.5"}, {"volume", "50", "extra"}} {
		if _, err := parseHeadsetVolume(args); err == nil {
			t.Fatal(args)
		}
	}
	device := &jabra_DeviceInfo{deviceID: 2, instance: strings.Repeat("a", 32), productID: headsetvolume.ProductID, firmwareVersion: headsetvolume.Firmware, deviceConnection: deviceConnectionType_USB}
	withDeviceState(t, devices{2: device}, 2, -1)
	api := &jabraAPIBridge{}
	percent := 50
	if _, err := api.SetHeadsetVolume(ipc.SettingTarget{ID: 2, Instance: strings.Repeat("b", 32)}, percent); err == nil {
		t.Fatal("stale target accepted")
	}
	if _, err := api.SetHeadsetVolume(*settingTarget(device), percent); err == nil {
		t.Fatal("uncaptured attachment accepted")
	}
}
