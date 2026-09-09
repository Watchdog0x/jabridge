package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/buttons"
	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

func TestButtonEventsTravelThroughPrivateServiceIPC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := DefaultConfig()
	cfg.SocketPath = filepath.Join(t.TempDir(), "buttons.sock")
	cfg.PIDPath = cfg.SocketPath + ".pid"
	cfg.DisablePipeWire = true
	observations := make(chan buttons.Observation, 2)
	source := buttons.Source{ID: "opaque-source", PID: 0x24c7, Connection: "usb", Ready: true}
	cfg.ButtonsMonitor = func(ctx context.Context, sources func([]buttons.Source), emit func(buttons.Observation)) {
		sources([]buttons.Source{source})
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-observations:
				emit(event)
			}
		}
	}
	var actions atomic.Int32
	cfg.MediaPlayPause = func(context.Context) error { actions.Add(1); return nil }
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, func(ctx context.Context) { <-ctx.Done() }, &nilAPI{}) }()
	call, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	client, err := ipc.DialWithRetry(call, cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("service failed to stop")
		}
	})
	var state buttons.Status
	if err := client.Call(call, "buttons.status", nil, &state); err != nil || state.Mode != "off" {
		t.Fatal(state, err)
	}
	if err := client.Subscribe(call); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(call, "buttons.configure", map[string]string{"mode": "play-pause"}, &state); err != nil {
		t.Fatal(err)
	}
	observations <- buttons.Observation{Source: source, Edge: buttons.Edge{Control: buttons.Control{Name: "play-pause", MusicEligible: true, Constant: true, Report: 9, Usage: 0xb0}, Pressed: true}, At: time.Now()}
	foundButton, foundSuppression := false, false
	for !foundButton || !foundSuppression {
		select {
		case event := <-client.Notifications():
			if event.Method == "device.button" {
				data, err := json.Marshal(event.Params)
				if err != nil {
					t.Fatal(err)
				}
				var button buttons.Event
				if err := json.Unmarshal(data, &button); err != nil {
					t.Fatal(err)
				}
				if button.Sequence != 1 || button.Session != state.Session || button.Name != "play-pause" || !button.Pressed {
					t.Fatal(button)
				}
				foundButton = true
			}
			if event.Method == "media.action" {
				foundSuppression = true
			}
		case <-call.Done():
			t.Fatal("missing IPC events")
		}
	}
	if actions.Load() != 0 {
		t.Fatal("missing audio/selection guard did not block playback")
	}
}

func TestButtonMusicNeedsUniqueSelectedUSBAndQuietMicrophone(t *testing.T) {
	source := buttons.Source{PID: 0x24c7, Connection: "usb"}
	d := ipc.DeviceInfo{PID: 0x24c7, Connection: "usb", Selected: true}
	if !selectedButtonSource([]ipc.DeviceInfo{d}, source) || selectedButtonSource([]ipc.DeviceInfo{d, d}, source) {
		t.Fatal("USB ambiguity guard")
	}
	d.Selected = false
	if selectedButtonSource([]ipc.DeviceInfo{d}, source) {
		t.Fatal("unselected device controls music")
	}
	snapshot := &pipewire.Snapshot{Nodes: []pipewire.Node{{State: "idle", Props: pipewire.NodeProps{MediaClass: "Audio/Source", NodeDescription: "Jabra Link 380", VendorID: "0x0b0e"}}}}
	if !microphoneQuiet(snapshot) {
		t.Fatal("idle microphone was not recognized")
	}
	snapshot.Nodes[0].State = "running"
	if microphoneQuiet(snapshot) || microphoneQuiet(nil) || microphoneQuiet(&pipewire.Snapshot{}) {
		t.Fatal("active/unknown audio not blocked")
	}
	snapshot.Nodes[0].State = ""
	if microphoneQuiet(snapshot) {
		t.Fatal("missing microphone state treated as quiet")
	}
}
