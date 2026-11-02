package main

import (
	"fmt"
	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/history"
	"strings"
	"time"
)

// These values belong only to the UI event loop, like the settings editor.
var boundResetTarget *ipc.SettingTarget
var boundResetUntil time.Time
var boundResetReceipt *ipc.BoundResetResult

func clearBoundResetConfirmation() { boundResetTarget = nil; boundResetUntil = time.Time{} }
func confirmBoundReset(now time.Time, target *ipc.SettingTarget) bool {
	if target == nil {
		clearBoundResetConfirmation()
		return false
	}
	if boundResetTarget != nil && *boundResetTarget == *target && !now.After(boundResetUntil) {
		clearBoundResetConfirmation()
		return true
	}
	copy := *target
	boundResetTarget = &copy
	boundResetUntil = now.Add(factoryResetConfirmWindow)
	return false
}
func boundResetUIAllowed(device *jabra_DeviceInfo) bool {
	if device == nil {
		return false
	}
	if currentTUIBackend() != nil {
		return device.boundReset
	}
	return eligibleLink390Reset(device)
}
func boundResetConfirmationCurrent(now time.Time) bool {
	device, exists := selectedDongleSnapshot()
	if !exists || !boundResetUIAllowed(device) || boundResetTarget == nil || now.After(boundResetUntil) || validateSettingTarget(device, boundResetTarget) != nil {
		clearBoundResetConfirmation()
		return false
	}
	return true
}
func handleBoundResetAction(device *jabra_DeviceInfo, results chan<- actionResult) {
	if uiBusy() {
		return
	}
	if height < 20 || width < 40 {
		setStatus("Resize to at least 40x20 before resetting.", true)
		return
	}
	if settingTarget(device) == nil {
		setStatus("Reload the selected dongle before resetting.", true)
		return
	}
	if !confirmBoundReset(time.Now(), settingTarget(device)) {
		boundResetReceipt = nil
		setStatus("Read the reset warning before pressing 2 again.", false)
		return
	}
	target := *settingTarget(device)
	setStatus("Resetting dongle; waiting for it to return...", false)
	ctx := currentUIResultContext()
	activity := beginUIActivity("Waiting for dongle reset; this may take a minute")
	entry := tuiHistoryEvent("action")
	finish := history.Begin(entry)
	go func() {
		defer history.CapturePanic(entry)
		var result ipc.BoundResetResult
		var err error
		if currentTUIBackend() != nil {
			err = tuiIPCCall("device.reset.bound", map[string]any{"confirm": "RESET_LINK390_DONGLE", "target": target}, &result)
		} else {
			result, err = (&jabraAPIBridge{}).FactoryResetBound(target)
		}
		if err != nil && result.Message == "" {
			result.Outcome = "unknown"
			result.Message = "Service reply missing. Reset result unknown. Reconnect the dongle before retrying."
		}
		if err == nil && result.Outcome != "restart-verified" {
			err = fmt.Errorf("%s", result.Message)
		}
		finish(err)
		response := actionResult{activityID: activity, message: result.Message, err: err, boundResetResult: &result}
		sendUIResult(ctx, results, response)
	}()
}

func resetTextLines(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && displayWidth(line)+1+displayWidth(word) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
func renderBoundResetPanel() bool {
	pending := boundResetConfirmationCurrent(time.Now())
	if !pending && boundResetReceipt == nil {
		return false
	}
	left, right, bottom := panelBounds()
	width := max(1, right-left-8)
	title := "Reset Link 390"
	text := "Stop calls and audio first. Settings may return to defaults. Headsets may need pairing again. An address-only record can remain."
	if boundResetReceipt != nil {
		title = "Reset result"
		text = boundResetReceipt.Message
	}
	drawingBox()
	drawListHeading(title, 0, 0)
	if height < 20 {
		drawCentered(9, "Resize to at least 40x20.", false)
		drawSplitActionBar([]string{"Q Cancel"}, nil)
		return true
	}
	lines := resetTextLines(text, width)
	row := 8
	if len(lines) > bottom-row {
		row = 7
	}
	for _, line := range lines {
		if row >= bottom {
			break
		}
		screen.setText(row, left+4, line, styleText)
		row++
	}
	if boundResetReceipt == nil {
		drawSplitActionBar([]string{"Q Cancel"}, []string{"2 Reset"})
	} else {
		drawSplitActionBar([]string{"Q Back"}, nil)
	}
	return true
}

func handleBoundResetPanelKey(event keyEvent, results chan<- actionResult) bool {
	if menuState != screenDongleSettings {
		return false
	}
	pending := boundResetConfirmationCurrent(time.Now())
	if !pending && boundResetReceipt == nil {
		return false
	}
	if event == keyBack {
		clearBoundResetConfirmation()
		boundResetReceipt = nil
		setStatus("", false)
		return true
	}
	if pending && boundResetReceipt == nil && event == keyAction2 {
		if device, exists := selectedDongleSnapshot(); exists {
			handleBoundResetAction(device, results)
		}
	}
	return true
}
