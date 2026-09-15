package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

// Keep update and device startup in the same visual flow. Work owns its own
// result, and must finish before this screen returns or the app can restart.
func runTUIStartupTask(title, detail string, task func(context.Context) error) error {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return task(context.Background())
	}
	if _, err := fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?25l"); err != nil {
		return err
	}
	defer func() { _, _ = fmt.Fprint(os.Stdout, "\x1b[0m\x1b[?25h\x1b[?1049l") }()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() { defer close(done); result <- task(ctx) }()
	defer func() { cancel(); <-done }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	tick := 0
	draw := func() error {
		w, h, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || w < 1 || h < 1 {
			w, h = 80, 24
		}
		_, err = os.Stdout.WriteString(startupTaskFrame(w, h, title, detail, tick).render())
		return err
	}
	if err := draw(); err != nil {
		return err
	}
	for {
		select {
		case err := <-result:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			tick++
			if err := draw(); err != nil {
				return err
			}
		}
	}
}

func startupTaskFrame(w, h int, title, detail string, tick int) *frame {
	f := &frame{baseStyle: styleHomeBase}
	f.resize(w, h)
	cardWidth := min(70, max(12, w-8))
	left := max(1, (w-cardWidth)/2+1)
	top := max(1, (h-12)/2+1)
	right := left + cardWidth - 1
	drawThemedBox(f, top, left, right, top+11, "JABRIDGE")
	col, inner := left+4, max(1, cardWidth-8)
	f.setText(top+2, col, trimToWidth(title, inner), styleHomeWhite)
	f.setText(top+4, col, trimToWidth(detail, inner), styleHomeMuted)
	bar := min(44, inner)
	f.setText(top+6, col, strings.Repeat("─", bar), styleHomeBorder)
	// This is activity, not an invented download percentage.
	length := min(6, bar)
	position := tick % max(1, bar-length+1)
	f.setText(top+6, col+position, strings.Repeat("━", length), styleHomeTitle)
	f.setText(top+8, col, trimToWidth("Please keep this window open.", inner), styleHomeMuted)
	return f
}
