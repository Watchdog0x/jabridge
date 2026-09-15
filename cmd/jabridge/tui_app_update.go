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

// This owns the terminal only for the decision. Its reader and raw mode are
// fully stopped before an installer or the normal TUI can take over stdin.
func confirmTUIAppUpdate(current, available string) (bool, error) {
	settings, err := enableRawMode()
	if err != nil {
		return false, fmt.Errorf("open update screen: %w", err)
	}
	defer restoreTerminal(settings)
	if _, err := fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?25l\x1b[?2004h"); err != nil {
		return false, err
	}
	defer func() { _, _ = fmt.Fprint(os.Stdout, "\x1b[?2004l\x1b[0m\x1b[?25h\x1b[?1049l") }()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	keys := make(chan keyEvent, 32)
	done := make(chan struct{})
	go func() { defer close(done); startKeysPressedListener(ctx, keys) }()
	defer func() { cancel(); <-done }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	yes := false
	lastWidth, lastHeight := 0, 0
	draw := func() error {
		w, h, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || w < 1 || h < 1 {
			w, h = 80, 24
		}
		lastWidth, lastHeight = w, h
		_, err = os.Stdout.WriteString(appUpdateFrame(w, h, current, available, yes).render())
		return err
	}
	if err := draw(); err != nil {
		return false, err
	}
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case event := <-keys:
			decided, accepted := handleAppUpdateKey(event, &yes)
			if decided {
				return accepted, nil
			}
			if err := draw(); err != nil {
				return false, err
			}
		case <-ticker.C:
			w, h, err := term.GetSize(int(os.Stdout.Fd()))
			if err == nil && (w != lastWidth || h != lastHeight) {
				if err := draw(); err != nil {
					return false, err
				}
			}
		}
	}
}

func handleAppUpdateKey(event keyEvent, yes *bool) (decided, accepted bool) {
	switch event {
	case keyRuneBase + 'y', keyRuneBase + 'Y':
		return true, true
	case keyRuneBase + 'n', keyRuneBase + 'N', keyEscape:
		return true, false
	}
	switch navigationKey(event) {
	case keyUp:
		*yes = true
	case keyDown:
		*yes = false
	case keyEnter:
		return true, *yes
	case keyBack:
		return true, false
	}
	return false, false
}

func appUpdateFrame(w, h int, current, available string, yes bool) *frame {
	f := &frame{baseStyle: styleHomeBase}
	f.resize(w, h)
	if w < 64 || h < 22 {
		f.setText(2, 3, trimToWidth("JABRIDGE UPDATE", max(0, w-4)), styleHomeTitle)
		f.setText(4, 3, trimToWidth(current+" → "+available, max(0, w-4)), styleHomeWhite)
		updateChoice(f, 6, 3, max(0, w-5), "Y  Yes, update", yes)
		updateChoice(f, 8, 3, max(0, w-5), "N  Not now", !yes)
		f.setText(min(h, 10), 3, trimToWidth("Y / N   Enter choose   Esc skip", max(0, w-4)), styleHomeMuted)
		return f
	}
	cardWidth := min(70, w-8)
	left := (w-cardWidth)/2 + 1
	right := left + cardWidth - 1
	top := max(1, (h-20)/2+1)
	drawThemedBox(f, top, left, right, top+19, "APP UPDATE")
	col, inner := left+4, cardWidth-8
	f.setText(top+2, col, "A new version is ready.", styleHomeWhite)
	f.setText(top+3, col, "Get the latest fixes and improvements.", styleHomeMuted)
	half := (inner - 4) / 2
	for row := top + 5; row <= top+7; row++ {
		f.setText(row, col, strings.Repeat(" ", half), styleHomeCard)
		f.setText(row, col+half+4, strings.Repeat(" ", half), styleHomeCard)
	}
	f.setText(top+5, col+2, "INSTALLED", styleHomeCard)
	f.setText(top+5, col+half+6, "AVAILABLE", styleHomeCard)
	f.setText(top+6, col+2, trimToWidth(current, half-4), styleHomeCard)
	f.setText(top+6, col+half+1, "→", styleHomeTitle)
	f.setText(top+6, col+half+6, trimToWidth(available, half-4), styleHomeCard)
	f.setText(top+9, col, "Update Jabridge now?", styleHomeBase)
	updateChoice(f, top+11, col, inner, "Y  Yes, update now", yes)
	f.setText(top+12, col+3, "Download, verify and restart", styleHomeMuted)
	updateChoice(f, top+14, col, inner, "N  Not now", !yes)
	f.setText(top+15, col+3, "Continue with your current version", styleHomeMuted)
	f.setText(top+17, col, "↑/↓ Choose   Enter Confirm   Esc Not now", styleHomeMuted)
	return f
}

func updateChoice(f *frame, row, col, size int, label string, selected bool) {
	style, marker := styleHomeCard, " "
	if selected {
		style, marker = styleHomeSelect, "›"
	}
	f.setText(row, col, strings.Repeat(" ", max(0, size)), style)
	f.setText(row, col+1, marker+" "+trimToWidth(label, max(0, size-4)), style)
}
