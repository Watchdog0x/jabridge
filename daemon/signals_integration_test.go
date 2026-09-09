package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/buttons"
	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/firmware"
)

func TestRawVendorSignalThroughPrivateServiceIPC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := DefaultConfig()
	cfg.SocketPath = filepath.Join(t.TempDir(), "signals.sock")
	cfg.PIDPath = cfg.SocketPath + ".pid"
	cfg.DisablePipeWire = true
	frames := make(chan []byte, 4)
	source := buttons.Source{ID: "simulated-source", PID: 0x0422, Connection: "usb", Ready: true, ObservesGNP: true}
	field := firmware.HIDField{SizeBits: 8, Count: 32, UsagePage: 0xff00, Usages: []uint32{1}, Flags: 0x102}
	decoder := buttons.NewInputDecoder(0x0422, 3, []firmware.HIDReport{{ID: 2, Kind: "input", Bytes: 33, Fields: []firmware.HIDField{field}}, {ID: 2, Kind: "output", Bytes: 33, Fields: []firmware.HIDField{field}}})
	cfg.ButtonsMonitor = func(ctx context.Context, sources func([]buttons.Source), emit func(buttons.Observation)) {
		sources([]buttons.Source{source})
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-frames:
				_, signal := decoder.Decode(frame)
				if signal != nil {
					emit(buttons.Observation{Source: source, Signal: signal, At: time.Now()})
				}
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
			t.Error("service did not stop")
		}
	})
	var caps ipc.ServiceCapabilities
	if err := client.Call(call, "service.capabilities", nil, &caps); err != nil || !slices.Contains(caps.Events, "device.signal") {
		t.Fatal(caps, err)
	}
	if err := client.Subscribe(call); err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 33)
	copy(frame, []byte{2, 0, 8, 0, 8, 0x12, 2, 1, 73})
	frames <- frame
	for {
		select {
		case notification := <-client.Notifications():
			if notification.Method != "device.signal" {
				continue
			}
			data, err := json.Marshal(notification.Params)
			if err != nil {
				t.Fatal(err)
			}
			var event buttons.SignalEvent
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			if event.Name != "battery" || event.Value != "73" || !event.Charging || event.PID != 0x0422 || event.Endpoint != 8 {
				t.Fatal(event)
			}
			var state buttons.Status
			if err := client.Call(call, "buttons.status", nil, &state); err != nil || len(state.LastSignals) != 1 || state.LastSignals[0].Value != "73" {
				t.Fatal(state, err)
			}
			if actions.Load() != 0 {
				t.Fatal("vendor signal controlled media")
			}
			return
		case <-call.Done():
			t.Fatal("no vendor event through IPC")
		}
	}
}
