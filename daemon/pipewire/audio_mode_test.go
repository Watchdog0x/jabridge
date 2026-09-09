package pipewire

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func modeFixture(t *testing.T) (*SoundController, *soundFixture) {
	t.Helper()
	f := newSoundFixture()
	for i := 0; i < 2; i++ {
		f.snap.Nodes[i].Props.DeviceID = 50
		f.snap.Nodes[i].Props.Channels = "2"
		f.snap.Nodes[i].State = "suspended"
	}
	f.snap.Nodes[1].Props.Channels = "1"
	profiles := []AudioProfile{{Index: 7, Name: "output:analog-stereo+input:mono-fallback", Available: "yes", Priority: 6501}, {Index: 9, Name: "output:analog-stereo", Available: "yes", Priority: 6500}}
	f.snap.Devices = map[int]AudioDevice{50: {ID: 50, Props: NodeProps{ObjectSerial: "500", VendorID: "0x0b0e", DeviceBus: "usb", NodeName: "PRIVATE_CARD"}, Known: true, Profile: profiles[0], Profiles: profiles}}
	mic := f.snap.Nodes[1]
	backend := SoundBackend{Snapshot: f.snapshot, Command: func(ctx context.Context, args ...string) (string, error) {
		if args[0] != "set-profile" {
			return f.command(ctx, args...)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.writes = append(f.writes, append([]string{}, args...))
		if args[1] != "50" {
			return "", errors.New("wrong device")
		}
		device := f.snap.Devices[50]
		switch args[2] {
		case "9":
			device.Profile = profiles[1]
			var kept []Node
			for _, node := range f.snap.Nodes {
				if node.ID != mic.ID {
					kept = append(kept, node)
				}
			}
			f.snap.Nodes = kept
		case "7":
			device.Profile = profiles[0]
			f.snap.Nodes = append(f.snap.Nodes, mic)
		default:
			return "", errors.New("profile index was guessed")
		}
		f.snap.Devices[50] = device
		return "", nil
	}}
	c := NewSoundController(backend, nil, nil)
	c.Refresh(context.Background())
	return c, f
}

func TestMusicAndCallsUseAdvertisedIndicesAndReadBack(t *testing.T) {
	c, f := modeFixture(t)
	target := c.State().Nodes[0].Target
	for _, mode := range []string{"music", "calls"} {
		state, err := c.ChangeMode(context.Background(), target, mode)
		if err != nil || state.Nodes[0].AudioMode != mode {
			t.Fatal(state, err)
		}
		if state.Nodes[0].Channels != 2 {
			t.Fatal("stereo output not reported")
		}
		mics := 0
		for _, node := range state.Nodes {
			if node.DeviceID == 50 && node.Kind == "microphone" {
				mics++
				if node.Channels != 1 {
					t.Fatal("wrong microphone channel count")
				}
			}
		}
		if mode == "music" && mics != 0 || mode == "calls" && mics != 1 {
			t.Fatal("microphone mode readback mismatch")
		}
	}
	if len(f.writes) != 2 || f.writes[0][2] != "9" || f.writes[1][2] != "7" {
		t.Fatal(f.writes)
	}
	encoded, _ := json.Marshal(c.State())
	if strings.Contains(string(encoded), "PRIVATE") {
		t.Fatal("audio mode leaked a private name")
	}
}

func TestModeRejectsRecycledTargetAndUnsupportedProfiles(t *testing.T) {
	c, f := modeFixture(t)
	target := c.State().Nodes[0].Target
	f.recycleAt = f.snapCalls + 2
	if _, err := c.ChangeMode(context.Background(), target, "music"); err == nil {
		t.Fatal("recycled ID accepted")
	}
	if len(f.writes) != 0 {
		t.Fatal("wrote after device replacement")
	}
	device := f.snap.Devices[50]
	device.Profiles[1].Available = "no"
	if _, err := selectAudioProfile(device, "music"); err == nil {
		t.Fatal("unavailable profile selected")
	}
	if _, err := selectAudioProfile(device, "anything"); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestSnapshotRetainsProfileMetadataWithoutExposingPrivateNames(t *testing.T) {
	data := []byte(`[{"id":0,"type":"PipeWire:Interface:Core","info":{"cookie":42}},{"id":50,"type":"PipeWire:Interface:Device","info":{"props":{"object.serial":10,"device.name":"PRIVATE","device.vendor.id":"0x0b0e"},"params":{"Profile":[{"index":7,"name":"output:analog-stereo+input:mono-fallback"}],"EnumProfile":[{"index":9,"name":"output:analog-stereo","available":"unknown"}]}}}]`)
	snapshot, err := ParseSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	device := snapshot.Devices[50]
	if !device.Known || device.Profile.Index != 7 || len(device.Profiles) != 1 {
		t.Fatal(device)
	}
}
