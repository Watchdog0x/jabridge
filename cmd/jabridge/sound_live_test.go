package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

// Opt-in only: reversible PipeWire changes on a verified Link 380. Normal
// unit tests never access live sound devices or change desktop defaults.
func TestLiveLink380SoundIPC(t *testing.T) {
	socket := os.Getenv("JABRIDGE_TEST_LIVE_SOUND_SOCKET")
	if socket == "" {
		t.Skip("live sound test not requested")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client, err := ipc.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	var state pipewire.SoundState
	for i := 0; i < 30; i++ {
		if err := client.Call(ctx, "sound.list", nil, &state); err != nil {
			t.Fatal(err)
		}
		if state.Available {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !state.Available || state.InCall {
		t.Fatal("live sound unavailable or a call is active")
	}
	count := 0
	for _, node := range state.Nodes {
		if node.Name != "Jabra Link 380" || node.Connection != "usb" || !node.Editable || node.Volume == nil || node.Muted == nil || node.AboveLimit {
			continue
		}
		count++
		func() {
			originalVolume, originalMute := *node.Volume, *node.Muted
			defer func() {
				restoreCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
				defer stop()
				var result pipewire.SoundNode
				if err := client.Call(restoreCtx, "sound.volume", map[string]any{"target": node.Target, "percent": originalVolume}, &result); err != nil {
					t.Error("restore volume", err)
				}
				mode := "off"
				if originalMute {
					mode = "on"
				}
				if err := client.Call(restoreCtx, "sound.mute", map[string]any{"target": node.Target, "mode": mode}, &result); err != nil {
					t.Error("restore mute", err)
				}
			}()
			var result pipewire.SoundNode
			if err := client.Call(ctx, "sound.volume", map[string]any{"target": node.Target, "percent": max(0, originalVolume-1)}, &result); err != nil {
				t.Fatal(err)
			}
			// Never unmute a muted output during the test.
			if err := client.Call(ctx, "sound.mute", map[string]any{"target": node.Target, "mode": "on"}, &result); err != nil {
				t.Fatal(err)
			}
			if node.Default {
				if err := client.Call(ctx, "sound.default", map[string]any{"target": node.Target}, &result); err != nil {
					t.Fatal(err)
				}
			}
		}()
		var current pipewire.SoundState
		if err := client.Call(ctx, "sound.list", nil, &current); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, now := range current.Nodes {
			if now.Target == node.Target {
				found = true
				if now.Volume == nil || now.Muted == nil || *now.Volume != *node.Volume || *now.Muted != *node.Muted {
					t.Fatal("sound not restored")
				}
			}
		}
		if !found {
			t.Fatal("device disappeared during restoration")
		}
		t.Logf("%s volume/mute reads and changes PASS; original values restored", node.Kind)
	}
	if count != 2 {
		t.Fatalf("expected Link 380 output and microphone, tested %d", count)
	}
}
