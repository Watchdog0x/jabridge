package pipewire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type soundFixture struct {
	mu           sync.Mutex
	snap         Snapshot
	volumes      map[int]SoundVolume
	writes       [][]string
	down         bool
	snapCalls    int
	recycleAt    int
	ignoreWrites bool
}

func newSoundFixture() *soundFixture {
	return &soundFixture{snap: Snapshot{Cookie: "123", DefaultSink: "PRIVATE_SINK", DefaultSource: "PRIVATE_MIC", Nodes: []Node{
		{ID: 1, Props: NodeProps{MediaClass: "Audio/Sink", NodeName: "PRIVATE_SINK", NodeDescription: "Jabra Link 380 PRIVATE_SERIAL", DeviceAPI: "alsa", DeviceBus: "usb", VendorID: "0x0b0e", ObjectSerial: "11"}},
		{ID: 2, Props: NodeProps{MediaClass: "Audio/Source", NodeName: "PRIVATE_MIC", NodeDescription: "Jabra Link 380", DeviceAPI: "alsa", DeviceBus: "usb", VendorID: "0x0b0e", ObjectSerial: "12"}},
		{ID: 3, Props: NodeProps{MediaClass: "Audio/Sink", NodeName: "bluez_output.PRIVATE_MAC", NodeDescription: "Jabra Evolve2 65", DeviceAPI: "bluez5", ObjectSerial: "13"}},
		{ID: 4, Props: NodeProps{MediaClass: "Audio/Sink", NodeName: "unrelated", NodeDescription: "Other vendor", VendorID: "0x1022", ObjectSerial: "14"}},
	}}, volumes: map[int]SoundVolume{1: {Percent: 40}, 2: {Percent: 60}, 3: {Percent: 50}}}
}
func (f *soundFixture) snapshot(ctx context.Context) (*Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.down {
		return nil, errors.New("PRIVATE_BACKEND_ERROR")
	}
	f.snapCalls++
	if f.snapCalls == f.recycleAt {
		f.snap.Nodes[0].Props.ObjectSerial = "999"
	}
	copy := f.snap
	copy.Nodes = append([]Node(nil), f.snap.Nodes...)
	return &copy, nil
}
func (f *soundFixture) command(ctx context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(args) < 2 {
		return "", errors.New("missing args")
	}
	id, err := strconv.Atoi(args[1])
	if err != nil {
		return "", err
	}
	v, ok := f.volumes[id]
	if !ok {
		return "", errors.New("unknown ID")
	}
	if args[0] == "get-volume" {
		mute := ""
		if v.Muted {
			mute = " [MUTED]"
		}
		return fmt.Sprintf("Volume: %.2f%s", float64(v.Percent)/100, mute), nil
	}
	f.writes = append(f.writes, append([]string(nil), args...))
	if f.ignoreWrites {
		return "", nil
	}
	switch args[0] {
	case "set-volume":
		v.Percent, err = strconv.Atoi(strings.TrimSuffix(args[2], "%"))
		if err != nil {
			return "", err
		}
	case "set-mute":
		v.Muted = args[2] == "1"
	case "set-default":
		for _, node := range f.snap.Nodes {
			if node.ID == id {
				if node.Props.MediaClass == "Audio/Sink" {
					f.snap.DefaultSink = node.Props.NodeName
				} else {
					f.snap.DefaultSource = node.Props.NodeName
				}
			}
		}
	default:
		return "", errors.New("unexpected command")
	}
	f.volumes[id] = v
	return "", nil
}
func fixtureController(f *soundFixture, changed func(SoundState)) *SoundController {
	return NewSoundController(SoundBackend{Snapshot: f.snapshot, Command: f.command}, changed, nil)
}

func TestSoundListsOutputMicrophoneAndBluetoothWithoutPrivateNames(t *testing.T) {
	f := newSoundFixture()
	c := fixtureController(f, nil)
	c.Refresh(context.Background())
	state := c.State()
	if !state.Available || len(state.Nodes) != 3 {
		t.Fatal(state)
	}
	if !state.Nodes[0].Editable || state.Nodes[1].Kind != "microphone" || !state.Nodes[1].Default {
		t.Fatal(state)
	}
	bt := state.Nodes[2]
	if bt.Connection != "bluetooth" || bt.Editable || !strings.Contains(bt.Note, "Bluetooth audio only") {
		t.Fatal(bt)
	}
	data, err := json.Marshal(state)
	if err != nil || strings.Contains(string(data), "PRIVATE") || strings.Contains(string(data), "bluez_output") {
		t.Fatal(string(data), err)
	}
	*state.Nodes[0].Volume = 0
	if *c.State().Nodes[0].Volume != 40 {
		t.Fatal("caller changed cached state")
	}
}

func TestSoundWritesAreBoundValidatedAndReadBack(t *testing.T) {
	f := newSoundFixture()
	c := fixtureController(f, nil)
	c.Refresh(context.Background())
	state := c.State()
	output, mic := state.Nodes[0].Target, state.Nodes[1].Target
	changed, err := c.Change(context.Background(), output, "volume", 39, "")
	if err != nil || changed.Volume == nil || *changed.Volume != 39 {
		t.Fatal(changed, err)
	}
	if got := strings.Join(f.writes[0], " "); got != "set-volume 1 39% --limit 1.0" {
		t.Fatal(got)
	}
	changed, err = c.Change(context.Background(), mic, "mute", 0, "toggle")
	if err != nil || changed.Muted == nil || !*changed.Muted {
		t.Fatal(changed, err)
	}
	if _, err = c.Change(context.Background(), mic, "default", 0, ""); err != nil {
		t.Fatal(err)
	}
	count := len(f.writes)
	for _, p := range []int{-1, 101} {
		if _, err := c.Change(context.Background(), output, "volume", p, ""); err == nil {
			t.Fatal("invalid volume accepted")
		}
	}
	if _, err := c.Change(context.Background(), state.Nodes[2].Target, "mute", 0, "on"); err == nil {
		t.Fatal("unverified Bluetooth identity written")
	}
	if len(f.writes) != count {
		t.Fatal("invalid requests wrote hardware")
	}
}

func TestSoundRejectsRecycledIDBeforeMutation(t *testing.T) {
	f := newSoundFixture()
	c := fixtureController(f, nil)
	c.Refresh(context.Background())
	target := c.State().Nodes[0].Target
	f.recycleAt = f.snapCalls + 2
	if _, err := c.Change(context.Background(), target, "volume", 10, ""); err == nil {
		t.Fatal("recycled node accepted")
	}
	if len(f.writes) != 0 {
		t.Fatal(f.writes)
	}
}

func TestSoundGraphLossAndServerRestartInvalidateTargets(t *testing.T) {
	f := newSoundFixture()
	var events []SoundState
	c := fixtureController(f, func(s SoundState) { events = append(events, s) })
	c.Refresh(context.Background())
	target := c.State().Nodes[0].Target
	c.Refresh(context.Background())
	if len(events) != 1 {
		t.Fatal("unchanged state emitted event")
	}
	f.down = true
	c.Refresh(context.Background())
	if c.State().Available || len(c.State().Nodes) != 0 {
		t.Fatal("stale nodes after graph loss")
	}
	f.down = false
	c.Refresh(context.Background())
	if !c.State().Available || len(events) != 3 {
		t.Fatal(events)
	}
	if _, err := c.Change(context.Background(), target, "mute", 0, "on"); err == nil {
		t.Fatal("old token survived disconnect")
	}
	target = c.State().Nodes[0].Target
	f.snap.Cookie = "456"
	if _, err := c.Change(context.Background(), target, "volume", 0, ""); err == nil {
		t.Fatal("old server cookie survived restart")
	}
	if len(f.writes) != 0 {
		t.Fatal(f.writes)
	}
}

func TestSoundFailureCannotClaimSuccess(t *testing.T) {
	f := newSoundFixture()
	c := fixtureController(f, nil)
	c.Refresh(context.Background())
	f.ignoreWrites = true
	if _, err := c.Change(context.Background(), c.State().Nodes[0].Target, "volume", 10, ""); err == nil {
		t.Fatal("unmatched readback accepted")
	}
	if len(f.writes) != 1 {
		t.Fatal("write was blindly retried", f.writes)
	}
}

func TestSoundClampingDoesNotHideFailedGainLimit(t *testing.T) {
	f := newSoundFixture()
	f.volumes[1] = SoundVolume{Percent: 150}
	f.ignoreWrites = true
	c := fixtureController(f, nil)
	c.Refresh(context.Background())
	node := c.State().Nodes[0]
	if !node.AboveLimit || *node.Volume != 100 {
		t.Fatal(node)
	}
	if _, err := c.Change(context.Background(), node.Target, "volume", 100, ""); err == nil {
		t.Fatal("amplified volume falsely verified as 100 percent")
	}
}
func TestSoundDefaultNoopDoesNotRewriteDesktopPreference(t *testing.T) {
	f := newSoundFixture()
	c := fixtureController(f, nil)
	c.Refresh(context.Background())
	target := c.State().Nodes[0].Target
	if _, err := c.Change(context.Background(), target, "default", 0, ""); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 0 {
		t.Fatal("already selected default rewritten")
	}
	f.snap.DefaultSink = "another-output"
	if _, err := c.Change(context.Background(), target, "default", 0, ""); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 1 || f.writes[0][0] != "set-default" {
		t.Fatal(f.writes)
	}
}

func TestSoundRejectsMissingIdentityAndNonAudioObjects(t *testing.T) {
	f := newSoundFixture()
	f.snap.Cookie = ""
	f.snap.Nodes = append(f.snap.Nodes, Node{ID: 99, Props: NodeProps{MediaClass: "Stream/Output/Audio", NodeDescription: "Jabra fake stream", VendorID: "0b0e"}})
	c := fixtureController(f, nil)
	c.Refresh(context.Background())
	if len(c.State().Nodes) != 3 {
		t.Fatal("stream included")
	}
	for _, node := range c.State().Nodes {
		if node.Editable || node.Target.Token != "" {
			t.Fatal(node)
		}
	}
}

func TestSoundVolumeParserRejectsInvalidNumbers(t *testing.T) {
	for _, text := range []string{"Volume: NaN", "Volume: +Inf", "Volume: -0.1", "no volume"} {
		if _, err := ParseSoundVolume(text); err == nil {
			t.Fatal(text)
		}
	}
	v, err := ParseSoundVolume("Volume: 1.50 [MUTED]")
	if err != nil || v.Percent != 100 || !v.Muted {
		t.Fatal(v, err)
	}
}

func TestAudioSnapshotUsesParentIdentityAndPrivateDefaultMetadata(t *testing.T) {
	raw := `[
{"id":0,"type":"PipeWire:Interface:Core","info":{"cookie":42}},
{"id":20,"type":"PipeWire:Interface:Device","info":{"props":{"device.api":"alsa","device.bus":"usb","device.vendor.id":"0x0b0e","device.description":"Jabra Link 380"}}},
{"id":10,"type":"PipeWire:Interface:Node","info":{"props":{"device.id":20,"object.serial":"11","media.class":"Audio/Sink","node.name":"PRIVATE_NAME"}}},
{"id":30,"type":"PipeWire:Interface:Metadata","props":{"metadata.name":"default"},"metadata":[{"subject":0,"key":"default.audio.sink","value":{"name":"PRIVATE_NAME"}}]}
]`
	snap, err := ParseSnapshot([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := NewSoundController(SoundBackend{}, nil, nil)
	node := c.describe(snap, snap.Nodes[0])
	if !node.Editable || !node.Default || node.Name != "Jabra Link 380" || node.Connection != "usb" {
		t.Fatal(node)
	}
	data, _ := json.Marshal(node)
	if strings.Contains(string(data), "PRIVATE") {
		t.Fatal(string(data))
	}
}
