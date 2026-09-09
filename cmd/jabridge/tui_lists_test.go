package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestListWindowShowsExactRemainingCounts(t *testing.T) {
	for _, test := range []struct{ selected, count, rows, start, end int }{{0, 24, 7, 0, 7}, {7, 24, 7, 2, 9}, {23, 24, 7, 17, 24}, {0, 3, 9, 0, 3}, {4, 5, 1, 4, 5}} {
		start, end := listWindow(test.selected, test.count, test.rows)
		if start != test.start || end != test.end {
			t.Fatal(test, start, end)
		}
		if test.count > test.rows {
			hint := listWindowHint(start, end, test.count, 80)
			if end < test.count && !strings.Contains(hint, fmt.Sprintf("%d more below", test.count-end)) {
				t.Fatal(hint)
			}
			if start > 0 && !strings.Contains(hint, fmt.Sprintf("%d more above", start)) {
				t.Fatal(hint)
			}
		}
	}
}

func TestTallTerminalsShowMoreSettingsAndHintsStayInside(t *testing.T) {
	withMenuState(t)
	var values []deviceSettingValue
	for i := 0; i < 35; i++ {
		values = append(values, deviceSettingValue{Remote: &remoteSettingValue{Label: fmt.Sprintf("Setting %02d", i), Value: "On", Editable: true, Choices: []string{"Off", "On"}}})
	}
	previous := 0
	for _, h := range []int{24, 40, 60} {
		f := newRenderTarget(t, 100, h)
		currentSelection = 0
		drawingBox()
		renderDeviceSettings([]menuItem{{label: infoLine("Device", "Demo")}}, values)
		left, right, bottom := panelBounds()
		shown := 0
		for row := 1; row <= h; row++ {
			if strings.Contains(rowText(f, row), "Setting ") {
				shown++
			}
		}
		if shown <= previous {
			t.Fatal("tall terminal did not show more settings", h, shown, previous)
		}
		previous = shown
		if shown < len(values) && !strings.Contains(rowText(f, 9), "more below") {
			t.Fatal("overflow is hidden", rowText(f, 9))
		}
		if shown == len(values) && strings.Contains(rowText(f, 9), "more below") {
			t.Fatal("phantom overflow when everything fits")
		}
		if f.cells[(bottom-1)*f.width+left-1].ch != '┗' || f.cells[(bottom-1)*f.width+right-1].ch != '┛' {
			t.Fatal("hint overwrote border")
		}
	}
}

func TestLabelValuePreservesStateOnNarrowScreen(t *testing.T) {
	withMenuState(t)
	for _, w := range []int{40, 50, 60, 80} {
		f := newRenderTarget(t, w, 24)
		drawLabelValue(10, strings.Repeat("Long label ", 10), "85% muted", true)
		row := rowText(f, 10)
		if !strings.Contains(row, "85% muted") {
			t.Fatal(row)
		}
	}
}

func TestSmallEditorKeepsCancelVisibleAndDoesNotEditBlind(t *testing.T) {
	setting := editorFixture(t, true)
	if err := openSettingEditor(settingScopeHeadset, setting, &jabra_DeviceInfo{deviceName: "Demo"}); err != nil {
		t.Fatal(err)
	}
	f := newRenderTarget(t, 30, 10)
	renderSettingEditor()
	all := ""
	for row := 1; row <= 10; row++ {
		all += rowText(f, row)
	}
	if !strings.Contains(all, "Esc Cancel") {
		t.Fatal(all)
	}
	before := activeSettingEditor.text
	handleSettingEditorKey(keyRuneBase+'x', make(chan actionResult, 1))
	if activeSettingEditor.text != before {
		t.Fatal("invisible text accepted")
	}
	handleSettingEditorKey(keyEscape, make(chan actionResult, 1))
	if activeSettingEditor != nil {
		t.Fatal("cancel stopped working")
	}
}
