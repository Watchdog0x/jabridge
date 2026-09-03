package pipewire

import (
	"testing"
)

// Fixture: minimal pw-dump JSON with a Jabra source, a Firefox stream, and a link between them.
var fixtureCallActive = []byte(`[
  {"id": 82, "type": "PipeWire:Interface:Node", "info": {
    "state": "running",
    "props": {"media.class": "Audio/Source", "node.name": "alsa_input.usb-0b0e_Jabra_Evolve2_85-00.mono-fallback"}
  }},
  {"id": 92, "type": "PipeWire:Interface:Node", "info": {
    "state": "running",
    "props": {"media.class": "Audio/Sink", "node.name": "alsa_output.usb-0b0e_Jabra_Evolve2_85-00.analog-stereo"}
  }},
  {"id": 200, "type": "PipeWire:Interface:Node", "info": {
    "state": "running",
    "props": {"media.class": "Stream/Output/Audio", "application.name": "Firefox", "media.role": "Communication"}
  }},
  {"id": 300, "type": "PipeWire:Interface:Link", "info": {
    "output-node-id": 200, "input-node-id": 82, "state": "active"
  }}
]`)

var fixtureNoCall = []byte(`[
  {"id": 82, "type": "PipeWire:Interface:Node", "info": {
    "state": "suspended",
    "props": {"media.class": "Audio/Source", "node.name": "alsa_input.usb-0b0e_Jabra_Evolve2_85-00.mono-fallback"}
  }},
  {"id": 92, "type": "PipeWire:Interface:Node", "info": {
    "state": "suspended",
    "props": {"media.class": "Audio/Sink", "node.name": "alsa_output.usb-0b0e_Jabra_Evolve2_85-00.analog-stereo"}
  }}
]`)

var fixtureMusicNotCall = []byte(`[
  {"id": 82, "type": "PipeWire:Interface:Node", "info": {
    "state": "running",
    "props": {"media.class": "Audio/Source", "node.name": "alsa_input.usb-0b0e_Jabra_Evolve2_85-00.mono-fallback"}
  }},
  {"id": 200, "type": "PipeWire:Interface:Node", "info": {
    "state": "running",
    "props": {"media.class": "Stream/Input/Audio", "application.name": "Spotify", "media.role": "Music"}
  }},
  {"id": 300, "type": "PipeWire:Interface:Link", "info": {
    "output-node-id": 200, "input-node-id": 82, "state": "active"
  }}
]`)

var fixtureTeamsCall = []byte(`[
  {"id": 73, "type": "PipeWire:Interface:Node", "info": {
    "state": "running",
    "props": {"media.class": "Audio/Source", "node.name": "alsa_input.usb-0b0e_Jabra_Link_380-00.mono-fallback"}
  }},
  {"id": 500, "type": "PipeWire:Interface:Node", "info": {
    "state": "running",
    "props": {"media.class": "Stream/Output/Audio", "application.name": "teams", "media.role": ""}
  }},
  {"id": 600, "type": "PipeWire:Interface:Link", "info": {
    "output-node-id": 500, "input-node-id": 73, "state": "active"
  }}
]`)

func TestParseSnapshot(t *testing.T) {
	snap, err := ParseSnapshot(fixtureCallActive)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Nodes) != 3 {
		t.Errorf("nodes = %d, want 3", len(snap.Nodes))
	}
	if len(snap.Links) != 1 {
		t.Errorf("links = %d, want 1", len(snap.Links))
	}
}

func TestJabraSourceNodes(t *testing.T) {
	snap, _ := ParseSnapshot(fixtureCallActive)
	sources := snap.JabraSourceNodes()
	if len(sources) != 1 {
		t.Fatalf("jabra sources = %d, want 1", len(sources))
	}
	if sources[0].ID != 82 {
		t.Errorf("source ID = %d, want 82", sources[0].ID)
	}
}

func TestJabraSinkNodes(t *testing.T) {
	snap, _ := ParseSnapshot(fixtureCallActive)
	sinks := snap.JabraSinkNodes()
	if len(sinks) != 1 {
		t.Fatalf("jabra sinks = %d, want 1", len(sinks))
	}
	if sinks[0].ID != 92 {
		t.Errorf("sink ID = %d, want 92", sinks[0].ID)
	}
}

func TestStreamNodes(t *testing.T) {
	snap, _ := ParseSnapshot(fixtureCallActive)
	streams := snap.StreamNodes()
	if len(streams) != 1 {
		t.Fatalf("streams = %d, want 1", len(streams))
	}
	if streams[0].Props.AppName != "Firefox" {
		t.Errorf("app = %q, want Firefox", streams[0].Props.AppName)
	}
}

func TestDetectCall_Active(t *testing.T) {
	snap, _ := ParseSnapshot(fixtureCallActive)
	state := DetectCall(snap)
	if !state.InCall {
		t.Fatal("expected InCall=true for active Firefox+Communication stream linked to Jabra")
	}
	if state.AppName != "Firefox" {
		t.Errorf("app = %q, want Firefox", state.AppName)
	}
}

func TestDetectCall_NoCall(t *testing.T) {
	snap, _ := ParseSnapshot(fixtureNoCall)
	state := DetectCall(snap)
	if state.InCall {
		t.Fatal("expected InCall=false when no streams exist")
	}
}

func TestDetectCall_MusicNotCall(t *testing.T) {
	snap, _ := ParseSnapshot(fixtureMusicNotCall)
	state := DetectCall(snap)
	if state.InCall {
		t.Fatal("expected InCall=false for Spotify music (not a communication app, role=Music)")
	}
}

func TestDetectCall_TeamsViaAppName(t *testing.T) {
	snap, _ := ParseSnapshot(fixtureTeamsCall)
	state := DetectCall(snap)
	if !state.InCall {
		t.Fatal("expected InCall=true for Teams stream linked to Jabra (matched by app name)")
	}
	if state.AppName != "teams" {
		t.Errorf("app = %q, want teams", state.AppName)
	}
}

func TestLinkedTo(t *testing.T) {
	snap, _ := ParseSnapshot(fixtureCallActive)
	if !snap.LinkedTo(200, 82) {
		t.Error("expected link between node 200 and 82")
	}
	if snap.LinkedTo(200, 92) {
		t.Error("unexpected link between node 200 and 92")
	}
}

func TestDetectCall_NoJabra(t *testing.T) {
	// No Jabra devices at all
	fixture := []byte(`[
		{"id": 100, "type": "PipeWire:Interface:Node", "info": {
			"state": "running",
			"props": {"media.class": "Stream/Output/Audio", "application.name": "Firefox", "media.role": "Communication"}
		}}
	]`)
	snap, _ := ParseSnapshot(fixture)
	state := DetectCall(snap)
	if state.InCall {
		t.Fatal("expected InCall=false when no Jabra source nodes exist")
	}
}
