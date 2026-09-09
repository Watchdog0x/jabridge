package pipewire

import "testing"

func TestMeetingAppsNeedActiveJabraMicrophoneCapture(t *testing.T) {
	for _, app := range []string{"Teams", "Microsoft Teams", "Zoom", "ZOOM VoiceEngine", "Discord", "Google Chrome", "Chromium", "Firefox"} {
		snap := &Snapshot{Nodes: []Node{
			{ID: 1, State: "running", Props: NodeProps{MediaClass: "Audio/Source", NodeDescription: "Jabra Link 380"}},
			{ID: 2, State: "running", Props: NodeProps{MediaClass: "Stream/Input/Audio", AppName: app}},
		}, Links: []Link{{OutputNodeID: 1, InputNodeID: 2, State: "active"}}}
		if !DetectCall(snap).InCall {
			t.Fatal("active call not recognized", app)
		}
		snap.Nodes[1].State = "idle"
		if DetectCall(snap).InCall {
			t.Fatal("open/idle app counted as call", app)
		}
		snap.Nodes[1].State = "running"
		snap.Links[0].State = "paused"
		if DetectCall(snap).InCall {
			t.Fatal("inactive link counted", app)
		}
		snap.Links[0].State = "active"
		snap.Nodes[1].Props.MediaClass = "Stream/Output/Audio"
		if DetectCall(snap).InCall {
			t.Fatal("playback counted as mic capture", app)
		}
	}
}
func TestMusicAndUnrelatedMicrophonesDoNotStartCallLight(t *testing.T) {
	snap := &Snapshot{Nodes: []Node{{ID: 1, State: "running", Props: NodeProps{MediaClass: "Audio/Source", NodeDescription: "Laptop microphone"}}, {ID: 2, State: "running", Props: NodeProps{MediaClass: "Stream/Input/Audio", AppName: "Discord"}}}, Links: []Link{{OutputNodeID: 1, InputNodeID: 2, State: "active"}}}
	if DetectCall(snap).InCall {
		t.Fatal("unrelated microphone counted")
	}
	snap.Nodes[0].Props.NodeDescription = "Jabra Link 380"
	snap.Nodes[1].Props.MediaRole = "Music"
	if DetectCall(snap).InCall {
		t.Fatal("music counted")
	}
	snap.Nodes[1].Props.MediaRole = ""
	snap.Nodes[1].Props.AppName = "OBS"
	if DetectCall(snap).InCall {
		t.Fatal("recording-only app counted without communication role")
	}
}
