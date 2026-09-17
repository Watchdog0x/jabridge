package main

import (
	"bytes"
	"github.com/Watchdog0x/jabridge/daemon/buttons"
	"github.com/Watchdog0x/jabridge/daemon/pipewire"
	"strings"
	"testing"
)

func TestPlaybackDiagnosticShowsProfileAndFlowWithoutPrivateNames(t *testing.T) {
	snapshot := &pipewire.Snapshot{
		Devices: map[int]pipewire.AudioDevice{7: {Known: true, Profile: pipewire.AudioProfile{Name: "output:analog-stereo+input:mono-fallback"}}},
		Nodes:   []pipewire.Node{{ID: 11, State: "running", Props: pipewire.NodeProps{VendorID: "0x0b0e", DeviceID: 7, NodeDescription: "Jabra PRIVATE", MediaClass: "Audio/Sink"}}},
		Links:   []pipewire.Link{{OutputNodeID: 8, InputNodeID: 11, State: "active"}},
	}
	var out bytes.Buffer
	writePlaybackDiagnostic(&out, snapshot)
	for _, want := range []string{"state=running", "profile=analog-stereo+microphone", "active-links=1", "not whether sound is audible"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("playback detail missing", out.String())
		}
	}
	if strings.Contains(out.String(), "PRIVATE") || safePlaybackProfile("PRIVATE_PROFILE") != "unknown" || safePlaybackState("PRIVATE") != "unknown" {
		t.Fatal("private playback data leaked", out.String())
	}
	if safePlaybackProfile("output:iec958-stereo") != "digital-stereo" {
		t.Fatal("digital profile hidden")
	}
}

func TestControlDiagnosticOmitsUnknownAndKeyboardFields(t *testing.T) {
	event := buttons.Event{PID: 0x0422, Edge: buttons.Edge{Control: buttons.Control{Page: 0xb, Usage: 0x2f, Name: "microphone-mute", Kind: "switch", Report: 3}, Pressed: true}}
	if text := safeControlEvent(event); !strings.Contains(text, "microphone-mute state=active") {
		t.Fatal(text)
	}
	event.Page = 7
	event.Usage = 65
	event.Name = "pause"
	if text := safeControlEvent(event); !strings.Contains(text, "omitted") || strings.Contains(text, "0041") {
		t.Fatal(text)
	}
	if safeMediaReason("PRIVATE") != "unknown" || safeAudioApp("PRIVATE_CALL_TITLE") != "other audio" {
		t.Fatal("private diagnostic value leaked")
	}
}
