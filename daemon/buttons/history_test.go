package buttons

import (
	"github.com/Watchdog0x/jabridge/internal/history"
	"testing"
	"time"
)

func TestRecognizedControlsAreRecordedWithRateLimit(t *testing.T) {
	var events []history.Event
	c := NewController(Config{Record: func(event history.Event) { events = append(events, event) }})
	when := time.Now()
	for i := 0; i < 80; i++ {
		c.observe(Observation{Source: Source{ID: "unit", PID: 0x0422, Connection: "usb"}, At: when, Edge: Edge{Control: Control{Name: "microphone-mute", Kind: "switch", Page: 0xb, Usage: 0x2f, Report: 3}, Pressed: i%2 == 0}})
	}
	if len(events) != 64 || c.State().HistoryOmitted != 16 {
		t.Fatal("history bound not enforced", len(events), c.State())
	}
	if events[0].ControlState != "active" || events[1].ControlState != "inactive" || events[0].HIDUsage != 0x2f {
		t.Fatal(events[:2])
	}
}
