package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type uiActivity struct {
	label   string
	started time.Time
}

var activityMu sync.Mutex
var activitySequence uint64
var activities = map[uint64]uiActivity{}
var animationFrame uint64 // advanced by the UI timer, never during rendering

var uiResultContext = context.Background() // captured by workers before starting

func currentUIResultContext() context.Context { return uiResultContext }

func sendUIResult(ctx context.Context, results chan<- actionResult, result actionResult) {
	select {
	case results <- result:
	case <-ctx.Done():
		endUIActivity(result.activityID)
	}
}

func beginUIActivity(label string) uint64 {
	activityMu.Lock()
	defer activityMu.Unlock()
	activitySequence++
	activities[activitySequence] = uiActivity{label: label, started: time.Now()}
	requestUIRedraw()
	return activitySequence
}

func endUIActivity(id uint64) {
	activityMu.Lock()
	delete(activities, id)
	activityMu.Unlock()
	requestUIRedraw()
}

func currentUIActivity() (uiActivity, bool) {
	activityMu.Lock()
	defer activityMu.Unlock()
	var newest uint64
	var activity uiActivity
	for id, candidate := range activities {
		if id > newest {
			newest, activity = id, candidate
		}
	}
	return activity, newest != 0
}

func uiBusy() bool { _, busy := currentUIActivity(); return busy }

func loadingGlyph() string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return frames[animationFrame%uint64(len(frames))]
}

// The status card has its own two rows above the controls. Never overwrite
// a setting, its help text, or the Back key, even on a small terminal.
func renderActivityStatus() {
	if height < 14 {
		return
	}
	left, right, bottom := panelBounds()
	col, available := left+2, right-left-3
	label, detail, badgeStyle := "Ready", "", styleStatusInfo
	if activity, busy := currentUIActivity(); busy {
		label, detail = loadingGlyph()+" Working", activity.label
		elapsed := int(time.Since(activity.started).Seconds())
		if elapsed >= 2 {
			detail += fmt.Sprintf("  ·  %ds", elapsed)
		}
	} else {
		statusMu.RLock()
		message, failed, expires, kind := statusMessage, statusIsError, statusUntil, statusKind
		statusMu.RUnlock()
		if message != "" && time.Now().Before(expires) {
			detail = message
			label = kind
			if label == "" {
				label = "Info"
			}
			if failed {
				if kind == "" || kind == "Info" {
					label = "Could not finish"
				}
				badgeStyle = styleStatusError
			} else if kind == "Saved" || kind == "Done" {
				badgeStyle = styleStatusSuccess
			}
		}
	}
	screen.setText(bottom+1, col, strings.Repeat(" ", available), styleStatusPanel)
	screen.setText(bottom+2, col, strings.Repeat(" ", available), styleStatusPanel)
	drawCenteredStyled(bottom+1, " "+trimToWidth(label, available-2)+" ", badgeStyle)
	if detail != "" {
		drawCenteredStyled(bottom+2, trimToWidth(detail, available-2), styleStatusPanel)
	}
}

func setResultStatus(message string, failed bool, kind string) {
	setStatus(message, failed)
	statusMu.Lock()
	statusKind = kind
	statusMu.Unlock()
}

const (
	styleStatusPanel   = styleBase
	styleStatusInfo    = styleHomeSelect
	styleStatusSuccess = styleHomeSelect
	styleStatusError   = styleAlert
)
