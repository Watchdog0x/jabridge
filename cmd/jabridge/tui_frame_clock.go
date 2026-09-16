package main

import (
	"io"
	"os"
	"time"
)

// Check this deadline after every event. A busy key/result channel cannot
// starve painting, and an old timer notification cannot cause a burst.
type tuiFrameClock struct{ origin, next time.Time }

func newTUIFrameClock(now time.Time) tuiFrameClock {
	return tuiFrameClock{origin: now, next: now.Add(tuiFrameInterval)}
}
func (c tuiFrameClock) due(now time.Time) bool { return !now.Before(c.next) }
func (c tuiFrameClock) animation(now time.Time) int {
	return max(0, int(now.Sub(c.origin)/tuiFrameInterval))
}
func (c *tuiFrameClock) advance(now time.Time) { c.next = now.Add(tuiFrameInterval) }

// Start the next interval when output begins. Time spent writing a large
// frame already counts toward it; adding another interval after that write
// would unnecessarily slow large or busy terminals.
func (c *tuiFrameClock) writeFrame(f *frame) error {
	text := f.render()
	c.advance(time.Now())
	return writeTerminalFrame(text)
}

func writeTerminalFrame(text string) error {
	written, err := os.Stdout.WriteString(text)
	if err == nil && written != len(text) {
		return io.ErrShortWrite
	}
	return err
}

func (c tuiFrameClock) remaining() time.Duration { return max(0, time.Until(c.next)) }

// Snapshot before composing or writing. A worker may change visible state
// during either operation; that newer revision must remain pending afterward.
func paintTUIRevision(compose func() *frame, write func(*frame) error) (uint64, error) {
	revision := uiRevision.Load()
	return revision, write(compose())
}
