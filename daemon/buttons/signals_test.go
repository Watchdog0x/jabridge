package buttons

import (
	"context"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/internal/firmware"
	"github.com/Watchdog0x/jabridge/internal/gnpevents"
	"github.com/Watchdog0x/jabridge/internal/history"
)

func signalLayout(id byte, size int) []firmware.HIDReport {
	f := firmware.HIDField{SizeBits: 8, Count: uint32(size - 1), UsagePage: 0xff00, Usages: []uint32{1}, Flags: 0x102}
	return []firmware.HIDReport{{ID: id, Kind: "input", Bytes: size, Fields: []firmware.HIDField{f}}, {ID: id, Kind: "output", Bytes: size, Fields: []firmware.HIDField{f}}}
}

func TestPassiveSignalsUseDescriptorAndNeverBecomeMediaEdges(t *testing.T) {
	for _, v := range []struct {
		id   byte
		size int
	}{{2, 33}, {5, 64}} {
		d := NewInputDecoder(0x0422, 3, signalLayout(v.id, v.size))
		if !d.ObservesGNP() || len(d.Controls) != 0 {
			t.Fatal("management-only source not recognized")
		}
		frame := make([]byte, v.size)
		copy(frame, []byte{v.id, 0, 8, 0, 9, 0x24, 4, 1, 7, 4})
		edges, signal := d.Decode(frame)
		if len(edges) != 0 || signal == nil || signal.Name != "play" || signal.Kind != "interaction" {
			t.Fatal(edges, signal)
		}
		frame[4] |= 0xc0
		if edges, signal = d.Decode(frame); len(edges) != 0 || signal != nil {
			t.Fatal("management reply became an event")
		}
	}
}

func TestSignalControllerDedupesStateAndClearsDisconnectedSources(t *testing.T) {
	var events []SignalEvent
	c := NewController(Config{Publish: func(name string, payload any) {
		if name == "device.signal" {
			events = append(events, payload.(SignalEvent))
		}
	}, Record: func(history.Event) {}, Media: func(context.Context, string) error { t.Fatal("signal controlled desktop media"); return nil }})
	source := Source{ID: "old", PID: 0x24c7, Connection: "usb", Ready: true}
	c.sources([]Source{source})
	if _, err := c.Configure("play-pause"); err != nil {
		t.Fatal(err)
	}
	s := gnpevents.Signal{Kind: "state", Name: "battery", Value: "73", Endpoint: 4, Charging: true}
	observation := Observation{Source: source, Signal: &s, At: time.Now()}
	c.observe(observation)
	c.observe(observation)
	if len(events) != 1 || c.State().SignalsObserved != 2 || len(c.actions) != 0 {
		t.Fatal(c.State())
	}
	s.Value = "72"
	c.observe(observation)
	if len(events) != 2 || len(c.State().LastSignals) != 1 {
		t.Fatal(c.State())
	}
	copy := c.State()
	copy.LastSignals[0].Value = "changed"
	if c.State().LastSignals[0].Value != "72" {
		t.Fatal("mutable signal state escaped")
	}
	s.Kind, s.Name, s.Value = "interaction", "play", "tap"
	c.observe(observation)
	c.observe(observation)
	if len(events) != 4 || len(c.actions) != 0 {
		t.Fatal("taps collapsed or triggered media")
	}
	c.sources(nil)
	if len(c.State().LastSignals) != 0 {
		t.Fatal("disconnected state retained")
	}
	c.observe(observation)
	if len(events) != 4 {
		t.Fatal("old source revived after disconnect")
	}
	source.ID = "new"
	c.sources([]Source{source})
	c.observe(observation)
	if len(events) != 4 {
		t.Fatal("old observation attributed to replacement")
	}
	observation.Source = source
	c.observe(observation)
	if len(events) != 5 {
		t.Fatal("replacement source not accepted")
	}
	source.Ready = false
	c.sources([]Source{source})
	if len(c.State().LastSignals) != 0 {
		t.Fatal("not-ready source retained state")
	}
}

func TestSignalControllerBoundsEndpointState(t *testing.T) {
	c := NewController(Config{Publish: func(string, any) {}, Record: func(history.Event) {}})
	source := Source{ID: "source", PID: 1, Ready: true}
	c.sources([]Source{source})
	for i := 0; i < 200; i++ {
		s := gnpevents.Signal{Kind: "state", Name: "battery", Value: "50", Endpoint: byte(i)}
		c.observe(Observation{Source: source, Signal: &s})
	}
	if len(c.State().LastSignals) != 128 || c.State().SignalsOmitted != 72 {
		t.Fatal(c.State())
	}
}

func TestPassiveFragmentRecovery(t *testing.T) {
	d := NewInputDecoder(0x0422, 3, signalLayout(2, 33))
	partial := make([]byte, 33)
	copy(partial, []byte{2, 0, 8, 0, 40, 0x7f, 1})
	if _, s := d.Decode(partial); s != nil {
		t.Fatal(s)
	}
	d.Decode([]byte{2}) // malformed continuation invalidates pending bytes
	good := make([]byte, 33)
	copy(good, []byte{2, 0, 8, 0, 8, 0x12, 2, 1, 73})
	if _, s := d.Decode(good); s == nil || s.Name != "battery" || s.Value != "73" {
		t.Fatal("failed to recover after malformed fragment", s)
	}
}

func TestSignalInvalidationClearsOnlyAffectedProducts(t *testing.T) {
	var notifications int
	c := NewController(Config{Publish: func(method string, _ any) {
		if method == "buttons.changed" {
			notifications++
		}
	}, Record: func(history.Event) {}})
	a := Source{ID: "link", PID: 0x24c7, Connection: "usb", Ready: true}
	b := Source{ID: "speak", PID: 0x0422, Connection: "usb", Ready: true}
	c.sources([]Source{a, b})
	s := gnpevents.Signal{Kind: "state", Name: "battery", Value: "73", Endpoint: 4}
	c.observe(Observation{Source: a, Signal: &s})
	c.observe(Observation{Source: b, Signal: &s})
	before := notifications
	c.InvalidateSignalsForProducts([]uint16{0x24c7})
	state := c.State()
	if len(state.LastSignals) != 1 || state.LastSignals[0].PID != 0x0422 || state.SignalsInvalidated != 1 || notifications != before+1 {
		t.Fatal(state, notifications)
	}
	c.InvalidateSignalsForProducts([]uint16{0x24c7})
	if notifications != before+1 {
		t.Fatal("empty invalidation published duplicate state")
	}
	c.observe(Observation{Source: a, Signal: &s})
	if len(c.State().LastSignals) != 2 {
		t.Fatal("fresh observations suppressed after invalidation")
	}
}
