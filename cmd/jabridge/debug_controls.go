package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Watchdog0x/jabridge/daemon/buttons"
	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

// Keep only standard audio-profile labels. Custom card, stream and profile
// names may contain user data and do not belong in a shareable report.
func safePlaybackProfile(name string) string {
	switch name {
	case "output:analog-stereo":
		return "analog-stereo"
	case "output:analog-stereo+input:mono-fallback":
		return "analog-stereo+microphone"
	case "output:iec958-stereo":
		return "digital-stereo"
	case "output:iec958-stereo+input:mono-fallback":
		return "digital-stereo+microphone"
	case "off", "pro-audio":
		return name
	default:
		return "unknown"
	}
}

func safePlaybackState(state string) string {
	switch state {
	case "running", "idle", "suspended", "creating", "error":
		return state
	default:
		return "unknown"
	}
}

func writePlaybackDiagnostic(out *bytes.Buffer, snapshot *pipewire.Snapshot) {
	if snapshot == nil {
		return
	}
	for _, sink := range snapshot.JabraSinkNodes() {
		profile := "unknown"
		if device, ok := snapshot.Devices[sink.Props.DeviceID]; ok && device.Known {
			profile = safePlaybackProfile(device.Profile.Name)
		}
		incoming, active := 0, 0
		for _, link := range snapshot.Links {
			if link.InputNodeID == sink.ID {
				incoming++
				if link.State == "active" {
					active++
				}
			}
		}
		fmt.Fprintf(out, "Playback output %d: state=%s; profile=%s; incoming-links=%d; active-links=%d\n", sink.ID, safePlaybackState(sink.State), profile, incoming, active)
	}
	fmt.Fprintln(out, "An active playback link shows the software route, not whether sound is audible.")
}

func safeControlEvent(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "payload omitted"
	}
	var event buttons.Event
	if json.Unmarshal(data, &event) != nil {
		return "payload omitted"
	}
	if !strings.Contains("|play|pause|play-pause|stop|next|previous|mute|volume-up|volume-down|microphone-mute|hook-switch|", "|"+event.Name+"|") || event.Name == "" {
		return "unrecognized control; payload omitted"
	}
	if expected := buttons.ControlName(event.Page, event.Usage); expected == "" || expected != event.Name {
		return "unrecognized control fields; payload omitted"
	}
	state := "released"
	if event.Pressed {
		state = "pressed"
	}
	if event.Kind == "switch" {
		state = "inactive"
		if event.Pressed {
			state = "active"
		}
	}
	return fmt.Sprintf("usb=%04x control=%s state=%s report=%d page=%04x usage=%04x initial=%t sequence=%d", event.PID, event.Name, state, event.Report, event.Page, event.Usage, event.Initial, event.Sequence)
}

func safeAudioMode(mode string) string {
	if mode == "music" || mode == "calls" {
		return mode
	}
	return "unknown"
}

func safeMediaReason(reason string) string {
	for _, known := range []string{"audio-or-device-policy", "mode-changed", "source-disconnected", "call-signal-active", "old-event", "command-delivered", "media-player-unavailable", "already-in-requested-state"} {
		if reason == known {
			return reason
		}
	}
	return "unknown"
}
func safeAudioModes(modes []string) []string {
	var out []string
	for _, mode := range modes {
		if mode == "music" || mode == "calls" {
			out = append(out, mode)
		}
	}
	return out
}

func writeAudioFlowDiagnostic(out *bytes.Buffer, snapshot *pipewire.Snapshot) {
	for _, source := range snapshot.JabraSourceNodes() {
		clients := 0
		for _, link := range snapshot.Links {
			if link.OutputNodeID != source.ID || link.State != "active" {
				continue
			}
			for _, node := range snapshot.Nodes {
				if node.ID == link.InputNodeID && node.Props.MediaClass == "Stream/Input/Audio" {
					clients++
					fmt.Fprintf(out, "Capture route: Jabra input %d -> %s client; running=%t\n", source.ID, safeAudioApp(node.Props.AppName), strings.EqualFold(node.State, "running"))
				}
			}
		}
		fmt.Fprintf(out, "Jabra input %d: running=%t direct capture clients=%d\n", source.ID, strings.EqualFold(source.State, "running"), clients)
	}
	fmt.Fprintln(out, "Indirect/virtual audio routing may need separate checks. No app titles, audio samples or Bluetooth addresses are included.")
}

func safeAudioApp(name string) string {
	name = strings.ToLower(name)
	for _, app := range []string{"teams", "zoom", "discord", "firefox", "chrome", "chromium", "obs", "audacity"} {
		if strings.Contains(name, app) {
			return app
		}
	}
	return "other audio"
}
