package main

import (
	"bytes"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

type soundClientAPI struct {
	jabraAPIBridge
	calls chan string
}

func (a *soundClientAPI) GetSound() pipewire.SoundState {
	v, m := 40, false
	return pipewire.SoundState{Available: true, Nodes: []pipewire.SoundNode{{Target: pipewire.SoundTarget{ID: 10, Token: strings.Repeat("a", 64)}, Name: "Jabra audio", Kind: "microphone", Connection: "bluetooth", Editable: true, Volume: &v, Muted: &m}}}
}
func (a *soundClientAPI) ChangeSound(target pipewire.SoundTarget, action string, p int, m string) (pipewire.SoundNode, error) {
	a.calls <- action
	return a.GetSound().Nodes[0], nil
}
func TestSoundCLIUsesIPCWithoutPipeWireExecutables(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "sound.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	api := &soundClientAPI{calls: make(chan string, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			ipc.HandleConnection(conn, api)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := ipc.Dial(ctx, listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close(); <-done }()
	var out bytes.Buffer
	command, err := parseSoundCommand([]string{"mic", "mute", "on"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runSoundClient(client, command, &out); err != nil {
		t.Fatal(err)
	}
	if <-api.calls != "mute" || !strings.Contains(out.String(), "microphone") || !strings.Contains(out.String(), "bluetooth") {
		t.Fatal(out.String())
	}
}
func TestSoundCLIValidationAndMultiDeviceSelection(t *testing.T) {
	for _, args := range [][]string{{"volume", "101"}, {"volume", "NaN"}, {"mute", "maybe"}, {"input", "-1"}, {"mic", "volume", "-1"}, {"output", "1", "2"}} {
		if _, err := parseSoundCommand(args); err == nil {
			t.Fatal(args)
		}
	}
	nodes := []pipewire.SoundNode{{Target: pipewire.SoundTarget{ID: 1}, Kind: "output"}, {Target: pipewire.SoundTarget{ID: 2}, Kind: "output"}, {Target: pipewire.SoundTarget{ID: 3}, Kind: "microphone"}}
	if _, err := selectSoundNode(nodes, "output", 0); err == nil {
		t.Fatal("ambiguous output guessed")
	}
	if node, err := selectSoundNode(nodes, "microphone", 0); err != nil || node.Target.ID != 3 {
		t.Fatal(node, err)
	}
	if _, err := selectSoundNode(nodes, "microphone", 1); err == nil {
		t.Fatal("sink accepted as microphone")
	}
}
func TestSoundTUIVolumeEditsAreBoundAndCancelable(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, nil, -1, -1)
	oldState, oldTarget, oldVolume, oldToken := currentSoundView(), soundDetailTarget, soundEditVolume, soundSelectionToken
	defer func() {
		updateSoundView(oldState)
		soundDetailTarget, soundEditVolume, soundSelectionToken = oldTarget, oldVolume, oldToken
	}()
	state := (&soundClientAPI{}).GetSound()
	updateSoundView(state)
	menuState = screenSound
	currentSelection = 0
	results := make(chan actionResult, 1)
	handleSoundEnter(results)
	if menuState != screenSoundDetail {
		t.Fatal("detail did not open")
	}
	handleSoundEnter(results)
	if menuState != screenSoundVolume || soundEditVolume != 40 {
		t.Fatal("volume did not open")
	}
	for i := 0; i < 200; i++ {
		handleSoundVolumeKey(keyUp, results)
	}
	if soundEditVolume != 100 {
		t.Fatal(soundEditVolume)
	}
	handleSoundVolumeKey(keyBack, results)
	if len(results) != 0 || menuState != screenSoundDetail || *currentSoundView().Nodes[0].Volume != 40 {
		t.Fatal("cancel changed audio")
	}
	state.Nodes[0].Target.Token = strings.Repeat("b", 64)
	updateSoundView(state)
	if _, ok := soundTargetNode(soundDetailTarget); ok {
		t.Fatal("stale detail target survived reconnect")
	}
}
