package main

import (
	"github.com/Watchdog0x/jabridge/daemon/buttons"
	"strings"
	"testing"
)

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
