package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/Watchdog0x/jabridge/internal/buildinfo"
)

// All information values start in the same column. Selection padding is
// handled by drawListItem and never becomes part of the field text.
func infoLine(label, value string) string { return fmt.Sprintf("%-20s%s", label+":", value) }

func homeSummaryRow() int {
	if height < 28 {
		return 6
	}
	_, _, bottom := panelBounds()
	rows := min(len(startMenu), max(1, bottom-11)) + 5
	return max(6, (5+bottom-rows)/2)
}

func drawHomeDetail(row int, label string, selected bool) {
	if height >= 18 {
		drawCentered(row, label, selected)
	}
}

func headerPair(row, left, right int, leftText, rightText, rightStyle string) {
	available := max(0, right-left)
	if rightText == "" {
		screen.setText(row, left, trimToWidth(leftText, available), styleText)
		return
	}
	rightBudget := max(available/2, available-min(16, displayWidth(leftText))-2)
	rightText = trimToWidth(rightText, max(0, rightBudget))
	leftText = trimToWidth(leftText, max(0, available-displayWidth(rightText)-2))
	screen.setText(row, left, leftText, styleText)
	screen.setText(row, right-displayWidth(rightText), rightText, rightStyle)
}

func header() {
	left, right, _ := panelBounds()
	available := right - left
	app := "Jabridge  " + buildinfo.Version
	if experimentalDeviceWritesEnabled() {
		badge := " EXPERIMENTAL WRITES "
		if displayWidth(app)+displayWidth(badge)+2 > available {
			badge = " EXPERIMENTAL "
		}
		app = trimToWidth(app, max(0, available-displayWidth(badge)-2))
		screen.setText(1, right-displayWidth(badge), badge, styleAlert)
	} else {
		app = trimToWidth(app, max(0, available))
	}
	screen.setText(1, left, app, styleTitle)

	dongle, dongleExists := selectedDongleSnapshot()
	headset, headsetExists := selectedHeadsetSnapshot()
	connection := "USB: not connected"
	if !firstScanComplete.Load() {
		connection = "Scanning devices..."
	} else if headsetExists && headset.deviceConnection == deviceConnectionType_USB {
		connection = "Connection: USB"
	} else if dongleExists {
		connection = "Dongle: " + dongle.deviceName
	}
	control := ""
	if headsetExists {
		for _, part := range headset.controlParts {
			if part.Role == "controller" {
				control = "Link Call Control: not detected"
				if part.Ready {
					control = "Link Call Control: connected"
				}
				break
			}
		}
	}
	if width < 72 && control != "" {
		connection = "USB"
		control = "Call control: not ready"
		if hasController(headset) {
			control = "Call control: ready"
		}
	}
	headerPair(2, left, right, connection, control, styleText)
	drawHeadsetStatus(left, right)
}

// Give the headset and its batteries a separate row, so neither overwrites
// the USB/dongle/Link Call Control identity above it.
func drawHeadsetStatus(left, right int) {
	const row = 3
	available := max(0, right-left)
	headset, exists := selectedHeadsetSnapshot()
	if !exists || controllerWithoutHeadset(headset) {
		label := "Headset: not detected"
		if !firstScanComplete.Load() {
			label = "Headset: scanning"
		}
		screen.setText(row, left, trimToWidth(label, available), styleText)
		return
	}
	label := "Headset: " + headset.deviceName
	battery := headset.batteryStatus
	if battery == nil || battery.levelInPercent > 100 {
		screen.setText(row, left, trimToWidth(label, available), styleText)
		return
	}
	if len(battery.components) > 1 {
		var parts []string
		for _, component := range battery.components {
			if component.levelInPercent <= 100 {
				value := fmt.Sprintf("%s %d%%", component.label, component.levelInPercent)
				if component.charging {
					value += "+"
				}
				parts = append(parts, value)
			}
		}
		if len(parts) > 0 {
			suffix := trimToWidth(strings.Join(parts, "  "), available)
			budget := max(0, available-displayWidth(suffix)-2)
			screen.setText(row, left, trimToWidth(label, budget), styleText)
			screen.setText(row, right-displayWidth(suffix), suffix, styleBattOK)
			return
		}
	}
	level := battery.levelInPercent
	style := styleBattOK
	if battery.batteryLow || level <= lowBatteryThreshold {
		style = styleBattLow
	} else if level <= 65 {
		style = styleBattWarn
	}
	suffix := fmt.Sprintf("%d%%", level)
	if battery.charging {
		suffix += " charging"
	}
	bar := ""
	if available >= 40 {
		filled := int(math.Round(float64(level) / 100 * batteryWidth))
		bar = "[" + strings.Repeat(batteryFullChar, filled) + strings.Repeat(batteryEmptyChar, batteryWidth-filled) + "] "
	}
	status := bar + suffix
	label = trimToWidth(label, max(0, available-displayWidth(status)-2))
	screen.setText(row, left, label, styleText)
	screen.setText(row, right-displayWidth(status), status, style)
}
