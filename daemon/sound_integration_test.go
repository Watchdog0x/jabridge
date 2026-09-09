package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

func TestServiceOwnsSoundAndDeliversChangeEvents(t *testing.T) {
	var volume atomic.Int32
	volume.Store(40)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := DefaultConfig()
	cfg.SocketPath = filepath.Join(t.TempDir(), "sound.sock")
	cfg.PIDPath = cfg.SocketPath + ".pid"
	cfg.SoundBackend = pipewire.SoundBackend{
		Snapshot: func(context.Context) (*pipewire.Snapshot, error) {
			return &pipewire.Snapshot{Cookie: "10", Nodes: []pipewire.Node{{ID: 1, Props: pipewire.NodeProps{MediaClass: "Audio/Sink", NodeName: "PRIVATE", NodeDescription: "Jabra Link 380", VendorID: "0x0b0e", ObjectSerial: "123"}}}}, nil
		},
		Command: func(_ context.Context, args ...string) (string, error) {
			if args[0] == "get-volume" {
				return fmt.Sprintf("Volume: %.2f", float64(volume.Load())/100), nil
			}
			p, err := strconv.Atoi(strings.TrimSuffix(args[2], "%"))
			if err != nil {
				return "", err
			}
			volume.Store(int32(p))
			return "", nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, func(ctx context.Context) { <-ctx.Done() }, &nilAPI{}) }()
	clientCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	client, err := ipc.DialWithRetry(clientCtx, cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
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
	}()
	if err := client.Subscribe(clientCtx); err != nil {
		t.Fatal(err)
	}
	var state pipewire.SoundState
	for i := 0; i < 20; i++ {
		if err := client.Call(clientCtx, "sound.list", nil, &state); err != nil {
			t.Fatal(err)
		}
		if state.Available {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(state.Nodes) != 1 {
		t.Fatal(state)
	}
	var changed pipewire.SoundNode
	if err := client.Call(clientCtx, "sound.volume", map[string]any{"target": state.Nodes[0].Target, "percent": 39}, &changed); err != nil {
		t.Fatal(err)
	}
	if changed.Volume == nil || *changed.Volume != 39 {
		t.Fatal(changed)
	}
	for {
		select {
		case event := <-client.Notifications():
			if event.Method == "sound.changed" {
				data, err := json.Marshal(event.Params)
				if err != nil {
					t.Fatal(err)
				}
				var update pipewire.SoundState
				if err := json.Unmarshal(data, &update); err != nil {
					t.Fatal(err)
				}
				if len(update.Nodes) == 1 && update.Nodes[0].Volume != nil && *update.Nodes[0].Volume == 39 {
					return
				}
			}
		case <-clientCtx.Done():
			t.Fatal("no sound.changed notification")
		}
	}
}
