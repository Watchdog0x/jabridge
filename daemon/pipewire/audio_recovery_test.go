package pipewire

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type recoveryFixture struct {
	snap                   Snapshot
	events                 []string
	name                   string
	started, closed, alive bool
	fail                   string
	beforeCommand          func(string)
	timestamps             map[string]time.Time
}

func (f *recoveryFixture) event(name string) {
	f.events = append(f.events, name)
	f.timestamps[name] = time.Now()
}

func (f *recoveryFixture) Alive() bool { return f.alive }
func (f *recoveryFixture) Close() error {
	f.event("capture-stop")
	f.closed, f.alive = true, false
	if f.fail == "close" {
		return errors.New("stop failed")
	}
	return nil
}

func recoveryTestController(t *testing.T) (*SoundController, *recoveryFixture, SoundTarget) {
	t.Helper()
	f := &recoveryFixture{timestamps: map[string]time.Time{}, snap: Snapshot{Cookie: "123", Devices: map[int]AudioDevice{}}}
	base := NodeProps{DeviceID: 7, DeviceBus: "usb", DeviceAPI: "alsa", VendorID: "0x0b0e", ProductID: "0x0e36"}
	output := Node{ID: 10, State: "running", Props: base}
	output.Props.MediaClass, output.Props.ObjectSerial, output.Props.NodeName = "Audio/Sink", "100", "test-output"
	mic := Node{ID: 11, State: "suspended", Props: base}
	mic.Props.MediaClass, mic.Props.ObjectSerial, mic.Props.NodeName = "Audio/Source", "101", "test-mic"
	f.snap.Nodes = []Node{output, mic}
	base.ObjectSerial = "99"
	f.snap.Devices[7] = AudioDevice{ID: 7, Props: base, Known: true, Profile: AudioProfile{Index: 1, Name: "output:analog-stereo+input:mono-fallback"}}
	c := NewSoundController(SoundBackend{
		RecoveryWait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		Snapshot: func(context.Context) (*Snapshot, error) {
			data, _ := json.Marshal(f.snap)
			var clone Snapshot
			_ = json.Unmarshal(data, &clone)
			if f.fail == "replaced" && f.started {
				clone.Nodes[0].Props.ObjectSerial = "999"
			}
			if f.fail == "profile" && f.started {
				d := clone.Devices[7]
				d.Profile.Index = 2
				clone.Devices[7] = d
			}
			return &clone, nil
		},
		Command: func(context.Context, ...string) (string, error) {
			t.Fatal("recovery must not change volume, mute, defaults or profiles")
			return "", nil
		},
		Capture: func(_ context.Context, serial, name string) (RecoveryCapture, error) {
			if serial != "101" {
				t.Fatal("wrong microphone", serial)
			}
			f.name = name
			f.event("capture-start")
			if f.fail == "capture-start" {
				return nil, errors.New("cannot capture")
			}
			f.started, f.alive = true, true
			f.snap.Nodes[1].State = "running"
			f.snap.Nodes = append(f.snap.Nodes, Node{ID: 12, State: "running", Props: NodeProps{NodeName: name, MediaClass: "Stream/Input/Audio", ObjectSerial: "102"}})
			f.snap.Links = append(f.snap.Links, Link{OutputNodeID: 11, InputNodeID: 12, State: "active"})
			if f.fail == "capture-exited" {
				f.alive = false
			}
			if f.fail == "competing-capture" {
				f.snap.Links = append(f.snap.Links, Link{OutputNodeID: 11, InputNodeID: 99, State: "active"})
			}
			return f, nil
		},
		NodeCommand: func(_ context.Context, id int, action string) error {
			if id != 10 || action == "Suspend" && !f.alive {
				t.Fatal("wrong target or capture not active", id, action)
			}
			f.event(action)
			if f.beforeCommand != nil {
				f.beforeCommand(action)
			}
			if f.fail == action {
				return errors.New("command failed")
			}
			if action == "Suspend" {
				f.snap.Nodes[0].State = "suspended"
			} else {
				f.snap.Nodes[0].State = "running"
			}
			return nil
		},
	}, nil, nil)
	return c, f, SoundTarget{ID: 10, Token: c.token(&f.snap, output)}
}

func TestAudioRecoverySequenceAndCleanup(t *testing.T) {
	c, f, target := recoveryTestController(t)
	result, err := c.RecoverPlayback(context.Background(), target)
	if err != nil || !result.SequenceCompleted || !result.CaptureStopped {
		t.Fatal(result, err)
	}
	if !reflect.DeepEqual(f.events, []string{"capture-start", "Suspend", "Start", "capture-stop"}) {
		t.Fatal(f.events)
	}
}

func TestAudioRecoveryRefusesUnsupportedOrBusyDevices(t *testing.T) {
	for _, kind := range []string{"dongle", "bluetooth", "other-model", "missing-mic", "busy-mic", "stale-target", "no-playback"} {
		t.Run(kind, func(t *testing.T) {
			c, f, target := recoveryTestController(t)
			switch kind {
			case "dongle":
				f.snap.Nodes[0].Props.ProductID = "0x24c7"
			case "bluetooth":
				f.snap.Nodes[0].Props.DeviceBus = "bluetooth"
			case "other-model":
				f.snap.Nodes[0].Props.ProductID = "0x0e41"
			case "missing-mic":
				f.snap.Nodes = f.snap.Nodes[:1]
			case "busy-mic":
				f.snap.Nodes[1].State = "running"
			case "stale-target":
				target.Token = strings.Repeat("f", 64)
			case "no-playback":
				f.snap.Nodes[0].State = "idle"
			}
			if kind != "stale-target" {
				target.Token = c.token(&f.snap, f.snap.Nodes[0])
			}
			if _, err := c.RecoverPlayback(context.Background(), target); err == nil {
				t.Fatal("accepted", kind)
			}
			if len(f.events) != 0 {
				t.Fatal("busy or unsupported device changed", f.events)
			}
		})
	}
}

func TestAudioRecoveryFailureAlwaysClosesCapture(t *testing.T) {
	for _, failure := range []string{"capture-start", "capture-exited", "competing-capture", "replaced", "profile", "Suspend", "Start", "close"} {
		t.Run(failure, func(t *testing.T) {
			c, f, target := recoveryTestController(t)
			f.fail = failure
			result, err := c.RecoverPlayback(context.Background(), target)
			if err == nil {
				t.Fatal("failure reported success", result)
			}
			if f.started && !f.closed {
				t.Fatal("capture was left open")
			}
			if failure == "replaced" || failure == "profile" || failure == "competing-capture" || failure == "capture-exited" {
				for _, event := range f.events {
					if event == "Suspend" || event == "Start" {
						t.Fatal("changed playback after invalidation", f.events)
					}
				}
			}
		})
	}
}

func TestAudioRecoveryCancellationResumesPlaybackAndClosesCapture(t *testing.T) {
	c, f, target := recoveryTestController(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.beforeCommand = func(action string) {
		if action == "Suspend" {
			cancel()
		}
	}
	_, err := c.RecoverPlayback(ctx, target)
	if err == nil || !f.closed {
		t.Fatal(err, f.closed)
	}
	if !reflect.DeepEqual(f.events, []string{"capture-start", "Suspend", "Start", "capture-stop"}) {
		t.Fatal(f.events)
	}
}

func TestAudioRecoveryCaptureLossAfterSuspendResumesOriginalPlayback(t *testing.T) {
	c, f, target := recoveryTestController(t)
	f.beforeCommand = func(action string) {
		if action == "Suspend" {
			f.alive = false
		}
	}
	_, err := c.RecoverPlayback(context.Background(), target)
	if err == nil || !f.closed {
		t.Fatal(err, f.closed)
	}
	if !reflect.DeepEqual(f.events, []string{"capture-start", "Suspend", "Start", "capture-stop"}) {
		t.Fatal(f.events)
	}
}

func TestAudioRecoveryKeepsCaptureDuringReportedTiming(t *testing.T) {
	c, f, target := recoveryTestController(t)
	c.backend.RecoveryWait = waitRecoveryStage
	result, err := c.RecoverPlayback(context.Background(), target)
	if err != nil || !result.SequenceCompleted || !result.CaptureStopped {
		t.Fatal(result, err)
	}
	for _, step := range []struct {
		from, to string
		minimum  time.Duration
	}{
		{"capture-start", "Suspend", 2 * time.Second},
		{"Suspend", "Start", 500 * time.Millisecond},
		{"Start", "capture-stop", 2 * time.Second},
	} {
		if elapsed := f.timestamps[step.to].Sub(f.timestamps[step.from]); elapsed < step.minimum {
			t.Fatalf("%s to %s: %v, need at least %v", step.from, step.to, elapsed, step.minimum)
		}
	}
}
