package main

import (
	"errors"
	"testing"
	"time"
)

func TestRedrawDuringFirstFrameRemainsPending(t *testing.T) {
	for _, stage := range []string{"compose", "write"} {
		t.Run(stage, func(t *testing.T) {
			before := uiRevision.Load()
			painted, err := paintTUIRevision(func() *frame {
				if stage == "compose" {
					requestUIRedraw()
				}
				return &frame{}
			}, func(*frame) error {
				if stage == "write" {
					requestUIRedraw()
				}
				return nil
			})
			if err != nil || painted != before || uiRevision.Load() == painted {
				t.Fatal("redraw during initial paint was marked as already displayed")
			}
		})
	}
	failure := errors.New("terminal disconnected")
	if _, err := paintTUIRevision(func() *frame { return &frame{} }, func(*frame) error { return failure }); !errors.Is(err, failure) {
		t.Fatal("output error was hidden")
	}
}

func TestFrameClockDoesNotCatchUpWithBurst(t *testing.T) {
	start := time.Unix(10, 0)
	clock := newTUIFrameClock(start)
	if clock.due(start) || !clock.due(start.Add(tuiFrameInterval)) {
		t.Fatal("wrong first deadline")
	}
	// An event-loop stall can leave an old timer ready. Once the delayed
	// frame starts, a queued tick cannot cause another immediate paint.
	started := start.Add(7 * tuiFrameInterval)
	clock.advance(started)
	for _, delay := range []time.Duration{0, time.Millisecond, tuiFrameInterval - 1} {
		if clock.due(started.Add(delay)) {
			t.Fatal("stale timer caused a catch-up frame", delay)
		}
	}
	if !clock.due(started.Add(tuiFrameInterval)) {
		t.Fatal("next frame is not eligible")
	}
	if got := clock.animation(start.Add(10 * tuiFrameInterval)); got != 10 {
		t.Fatal("animation did not advance with elapsed time", got)
	}
}
