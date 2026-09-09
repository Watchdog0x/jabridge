package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/buttons"
	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/gnpevents"
)

type identityLifecycleAPI struct {
	nilAPI
	mu        sync.Mutex
	devices   []ipc.DeviceInfo
	firstRead chan struct{}
	once      sync.Once
}

func (a *identityLifecycleAPI) ListDevices() []ipc.DeviceInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	copy := append([]ipc.DeviceInfo(nil), a.devices...)
	a.once.Do(func() { close(a.firstRead) })
	return copy
}

func TestDeviceIdentityClearsSignalCacheThroughService(t *testing.T) {
	parent := ipc.DeviceInfo{ID: 0, Instance: "adapter", PID: 0x24c7, IsDongle: true, Connection: "usb"}
	child := ipc.DeviceInfo{ID: 7, Instance: "child-a", PID: 0x24a3, ParentID: 0, Connection: "dongle"}
	api := &identityLifecycleAPI{devices: []ipc.DeviceInfo{parent, child}, firstRead: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := DefaultConfig()
	cfg.SocketPath = filepath.Join(t.TempDir(), "identity.sock")
	cfg.PIDPath = cfg.SocketPath + ".pid"
	cfg.DisablePipeWire = true
	observations := make(chan buttons.Observation, 1)
	source := buttons.Source{ID: "same-usb-interface", PID: 0x24c7, Connection: "usb", Ready: true, ObservesGNP: true}
	cfg.ButtonsMonitor = func(ctx context.Context, publish func([]buttons.Source), emit func(buttons.Observation)) {
		publish([]buttons.Source{source})
		for {
			select {
			case <-ctx.Done():
				return
			case o := <-observations:
				emit(o)
			}
		}
	}
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, func(ctx context.Context) { <-ctx.Done() }, api) }()
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
	select {
	case <-api.firstRead:
	case <-call.Done():
		t.Fatal("registry was not sampled")
	}
	if err := client.Subscribe(call); err != nil {
		t.Fatal(err)
	}
	signal := gnpevents.Signal{Kind: "state", Name: "battery", Value: "73", Endpoint: 4}
	observations <- buttons.Observation{Source: source, Signal: &signal, At: time.Now()}
	for {
		select {
		case n := <-client.Notifications():
			if n.Method == "device.signal" {
				goto observed
			}
		case <-call.Done():
			t.Fatal("signal was not observed")
		}
	}
observed:
	var state buttons.Status
	if err := client.Call(call, "buttons.status", nil, &state); err != nil || len(state.LastSignals) != 1 {
		t.Fatal(state, err)
	}
	api.mu.Lock()
	api.devices[1].Instance = "child-b"
	api.mu.Unlock()
	foundDetach, foundAttach := false, false
	for !foundDetach || !foundAttach {
		select {
		case n := <-client.Notifications():
			if n.Method != "device.detached" && n.Method != "device.attached" {
				continue
			}
			data, err := json.Marshal(n.Params)
			if err != nil {
				t.Fatal(err)
			}
			var device ipc.DeviceInfo
			if err := json.Unmarshal(data, &device); err != nil {
				t.Fatal(err)
			}
			if device.ID != 7 {
				t.Fatal("unaffected dongle was detached")
			}
			if n.Method == "device.detached" {
				if device.Instance != "child-a" || foundAttach {
					t.Fatal("wrong detach identity/order")
				}
				foundDetach = true
			}
			if n.Method == "device.attached" {
				if device.Instance != "child-b" || !foundDetach {
					t.Fatal("wrong attach identity/order")
				}
				foundAttach = true
			}
		case <-call.Done():
			t.Fatal("replacement lifecycle notifications missing")
		}
	}
	if err := client.Call(call, "buttons.status", nil, &state); err != nil || len(state.LastSignals) != 0 || state.SignalsInvalidated != 1 || len(state.Sources) != 1 || !state.Sources[0].Ready {
		t.Fatal("same-USB child replacement kept old signal cache", state, err)
	}
}
