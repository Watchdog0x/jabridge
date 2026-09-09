package buttons

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestControllerDefaultsOffDeduplicatesAndChecksCallGuard(t *testing.T) {
	var calls atomic.Int32
	var allowed atomic.Bool
	allowed.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := NewController(Config{Monitor: func(ctx context.Context, _ func([]Source), _ func(Observation)) { <-ctx.Done() }, AllowMusic: func(Source) bool { return allowed.Load() }, PlayPause: func(context.Context) error { calls.Add(1); return nil }})
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("monitor did not join")
		}
	})
	source := Source{ID: "source", PID: 0x24c7, Ready: true}
	c.sources([]Source{source})
	press := Observation{Source: source, Edge: Edge{Control: Control{Name: "play-pause", MusicEligible: true}, Pressed: true}, At: time.Now()}
	c.observe(press)
	if len(c.actions) != 0 || calls.Load() != 0 {
		t.Fatal("music enabled by default")
	}
	if _, err := c.Configure("play-pause"); err != nil {
		t.Fatal(err)
	}
	c.observe(press)
	c.observe(press)
	waitFor(t, func() bool { return c.State().Actions == 1 })
	if calls.Load() != 1 {
		t.Fatal("duplicate press sent")
	}
	allowed.Store(false)
	press.At = press.At.Add(500 * time.Millisecond)
	c.observe(press)
	waitFor(t, func() bool { return c.State().Suppressed >= 2 })
	if calls.Load() != 1 {
		t.Fatal("call guard ignored")
	}
	if _, err := c.Configure("invalid"); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if _, err := c.Configure("off"); err != nil {
		t.Fatal(err)
	}
	if NewController(Config{}).State().Mode != "off" {
		t.Fatal("mode persists unexpectedly")
	}
}

func TestOffCancelsQueuedActionAndDisconnectCancelsStaleSource(t *testing.T) {
	var calls atomic.Int32
	c := NewController(Config{Monitor: func(ctx context.Context, _ func([]Source), _ func(Observation)) { <-ctx.Done() }, AllowMusic: func(Source) bool { return true }, PlayPause: func(context.Context) error { calls.Add(1); return nil }})
	source := Source{ID: "old", PID: 0x24c7, Ready: true}
	c.sources([]Source{source})
	if _, err := c.Configure("play-pause"); err != nil {
		t.Fatal(err)
	}
	c.observe(Observation{Source: source, Edge: Edge{Control: Control{Name: "play-pause", MusicEligible: true}, Pressed: true}, At: time.Now()})
	if _, err := c.Configure("off"); err != nil {
		t.Fatal(err)
	}
	c.sources(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	waitFor(t, func() bool { return c.State().Suppressed == 1 })
	cancel()
	<-done
	if calls.Load() != 0 {
		t.Fatal("queued command survived off/disconnect")
	}
}

func TestStateDoesNotExposeMutableSourceMemory(t *testing.T) {
	c := NewController(Config{})
	c.sources([]Source{{ID: "a", Controls: []Control{{Name: "pause"}}}})
	s := c.State()
	s.Sources[0].Controls[0].Name = "changed"
	if c.State().Sources[0].Controls[0].Name != "pause" {
		t.Fatal("state aliases internal data")
	}
}

func TestCallSignalBlocksMediaEvenWithQuietPipeWire(t *testing.T) {
	var calls atomic.Int32
	c := NewController(Config{Monitor: func(ctx context.Context, _ func([]Source), _ func(Observation)) { <-ctx.Done() }, AllowMusic: func(Source) bool { return true }, PlayPause: func(context.Context) error { calls.Add(1); return nil }})
	source := Source{ID: "source", Ready: true}
	c.sources([]Source{source})
	if _, err := c.Configure("play-pause"); err != nil {
		t.Fatal(err)
	}
	c.observe(Observation{Source: source, At: time.Now(), Edge: Edge{Control: Control{Name: "hook-switch", Kind: "switch"}, Pressed: true}})
	c.observe(Observation{Source: source, At: time.Now(), Edge: Edge{Control: Control{Name: "pause", MusicEligible: true}, Pressed: true}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	waitFor(t, func() bool { return c.State().LastAction != nil })
	cancel()
	<-done
	if calls.Load() != 0 || c.State().LastAction.Reason != "call-signal-active" {
		t.Fatal(c.State())
	}
}
