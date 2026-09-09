package main

import (
	"fmt"
	"strings"
)

const (
	hintMove   = "↑/↓ Select"
	hintBack   = "Q Back"
	hintOpen   = "Enter Open"
	hintEdit   = "Enter Edit..."
	hintChange = "Enter Change now"
	hintSave   = "Enter Save"
	hintCancel = "Esc Cancel"
)

// Keep a row below the cursor while possible, rather than pinning every
// scrolled selection against the bottom edge with no clue what follows.
func listWindow(selection, count, rows int) (start, end int) {
	if count <= 0 || rows <= 0 {
		return 0, 0
	}
	selection = clampSelection(selection, count)
	margin := 0
	if rows >= 3 {
		margin = 1
	}
	start = max(0, selection-rows+1+margin)
	start = min(start, max(0, count-rows))
	return start, min(count, start+rows)
}

func listTitle(title string, selection, count int) string {
	if count > 0 {
		return fmt.Sprintf("%s  ·  %d of %d", title, clampSelection(selection, count)+1, count)
	}
	return title
}

func drawListHeading(title string, selection, count int) {
	left, right, _ := panelBounds()
	drawCenteredStyled(6, trimToWidth(listTitle(title, selection, count), right-left-4), styleTitle)
}

func listWindowHint(start, end, count, space int) string {
	if count <= 0 || end <= start || start == 0 && end == count {
		return ""
	}
	position := fmt.Sprintf("%d-%d of %d", start+1, end, count)
	var parts []string
	if start > 0 {
		parts = append(parts, fmt.Sprintf("%d more above", start))
	}
	if end < count {
		parts = append(parts, fmt.Sprintf("%d more below", count-end))
	}
	hint := position + "  |  " + strings.Join(parts, ", ")
	if displayWidth(hint) > space {
		hint = fmt.Sprintf("%s  ↑%d ↓%d more", position, start, count-end)
	}
	return trimToWidth(hint, space)
}

func drawListWindowHint(row, start, end, count int) {
	left, right, bottom := panelBounds()
	if row <= 4 || row >= bottom {
		return
	}
	text := listWindowHint(start, end, count, max(0, right-left-8))
	if text != "" {
		screen.setText(row, left+4, text, styleWarn)
	}
}

func drawLabelValue(row int, label, value string, selected bool) {
	left, right, _ := panelBounds()
	col := left + 4
	space := max(1, right-col-3)
	value = trimToWidth(value, max(1, space-8))
	label = trimToWidth(label, max(0, space-displayWidth(value)-2))
	text := label + strings.Repeat(" ", max(1, space-displayWidth(label)-displayWidth(value))) + value
	drawListItem(row, col, text, selected)
}

func drawSettingRow(row int, setting deviceSettingValue, selected bool) {
	value := setting.valueName()
	if !setting.editable() {
		value += " [read only]"
	} else if settingIsText(setting) || len(settingChoices(setting)) > 2 || setting.needsConfigMode() {
		value += " >"
	}
	drawLabelValue(row, setting.label(), value, selected)
}

func drawInfoRow(row int, text string) {
	label, value, found := strings.Cut(text, ":")
	left, right, _ := panelBounds()
	if !found {
		drawListItem(row, left+4, text, false)
		return
	}
	labelWidth := min(20, max(10, (right-left-8)/3))
	label = trimToWidth(label+":", labelWidth-1)
	line := label + strings.Repeat(" ", max(1, labelWidth-displayWidth(label))) + strings.TrimLeft(value, " ")
	drawListItem(row, left+4, line, false)
}

func compactActionHints(items []string, space int) []string {
	var compact []string
	for _, item := range items {
		switch item {
		case "↑/↓ Move", "Up/Down Select", "Up/Down Change", hintMove:
			item = "↑/↓"
		case "Q Back", "Q Quit":
			item = "Q"
		case "Esc/Q Cancel":
			item = "Esc/Q"
		case hintCancel:
			item = "Esc"
		case hintEdit:
			item = "Enter Edit"
		case hintChange:
			item = "Enter Change"
		case "Enter Next target":
			item = "Enter Next"
		}
		compact = append(compact, item)
	}
	if actionItemsWidth(compact) <= space {
		return compact
	}
	var keys []string
	for _, item := range compact {
		fields := strings.Fields(item)
		if len(fields) > 0 {
			key := fields[0]
			if strings.Contains(" Q Esc Esc/Q Q/Esc ↑/↓ Enter 1 2 3 4 ", " "+key+" ") {
				keys = append(keys, key)
			}
		}
	}
	return []string{strings.Join(keys, " ")}
}
