package pipewire

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestLiveIdleLink380MusicCallsRoundTrip(t *testing.T) {
	if os.Getenv("JABRIDGE_AUDIO_MODE_LIVE_TEST") != "1" {
		t.Skip("explicit reversible audio-profile test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	snapshot, err := TakeSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range snapshot.StreamNodes() {
		if node.State != "idle" && node.State != "suspended" {
			t.Skip("audio stream active or unknown; do not interrupt it")
		}
	}
	var device AudioDevice
	found := false
	for _, candidate := range snapshot.Devices {
		if candidate.Props.VendorID == "0x0b0e" && candidate.Props.ProductID == "0x24c7" {
			if found {
				t.Fatal("multiple matching Link 380 devices")
			}
			device = candidate
			found = true
		}
	}
	if !found {
		t.Skip("Link 380 not connected")
	}
	original := audioModeName(device.Profile.Name)
	if !device.Known || original == "unknown" {
		t.Skip("original profile not supported by this test")
	}
	for _, node := range snapshot.Nodes {
		if node.Props.DeviceID == device.ID && node.State != "idle" && node.State != "suspended" {
			t.Skip("Link audio node is active or unknown")
		}
	}
	binding := audioDeviceBinding(snapshot, device)
	c := NewSoundController(SoundBackend{}, nil, nil)
	target := func() (SoundTarget, bool) {
		c.Refresh(context.Background())
		for _, node := range c.State().Nodes {
			if node.DeviceID == device.ID && node.Kind == "output" {
				return node.Target, true
			}
		}
		return SoundTarget{}, false
	}
	defer func() {
		restoreCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		current, e := TakeSnapshotContext(restoreCtx)
		if e != nil || audioDeviceBinding(current, current.Devices[device.ID]) != binding {
			t.Error("device identity changed; cannot safely restore profile")
			return
		}
		fresh, ok := target()
		if !ok {
			t.Error("no target for profile restoration")
			return
		}
		if _, e := c.ChangeMode(restoreCtx, fresh, original); e != nil {
			t.Error("restore profile:", e)
			return
		}
		current, e = TakeSnapshotContext(restoreCtx)
		if e != nil {
			t.Error(e)
			return
		}
		for _, wanted := range []struct{ name, current string }{{snapshot.DefaultSink, current.DefaultSink}, {snapshot.DefaultSource, current.DefaultSource}} {
			if wanted.name == "" || wanted.name == wanted.current {
				continue
			}
			restored := false
			for _, node := range current.Nodes {
				if node.Props.NodeName == wanted.name {
					if _, e := SoundCommand(restoreCtx, "set-default", strconv.Itoa(node.ID)); e != nil {
						t.Error("restore default:", e)
					} else {
						restored = true
					}
					break
				}
			}
			if !restored {
				t.Error("original audio default could not be restored")
			}
		}
	}()
	for _, mode := range []string{"music", "calls"} {
		fresh, ok := target()
		if !ok {
			t.Fatal("no output target")
		}
		if _, err := c.ChangeMode(ctx, fresh, mode); err != nil {
			t.Fatal(mode, err)
		}
		t.Log("profile and microphone availability read back:", mode)
	}
}
