package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// withMenuState saves and restores the globals the menu and render tests poke.
func withMenuState(t *testing.T) {
	t.Helper()
	oldState := menuState
	oldSelection := currentSelection
	oldSelectionID := startMenuSelectionID
	oldMenu := startMenu
	oldWidth, oldHeight := width, height
	oldScreen := screen
	oldResetConfirmUntil := resetConfirmUntil
	oldForgetConfirmUntil := forgetConfirmUntil
	oldForgetConfirmAddr := forgetConfirmAddr
	oldFirmwareTargetID := firmwareTargetID
	oldFirmwareTargetIndex := firmwareTargetIndex
	oldFirmwareTargetItems := firmwareTargetItems
	t.Cleanup(func() {
		menuState = oldState
		currentSelection = oldSelection
		startMenuSelectionID = oldSelectionID
		startMenu = oldMenu
		width, height = oldWidth, oldHeight
		screen = oldScreen
		resetConfirmUntil = oldResetConfirmUntil
		forgetConfirmUntil = oldForgetConfirmUntil
		forgetConfirmAddr = oldForgetConfirmAddr
		firmwareTargetID = oldFirmwareTargetID
		firmwareTargetIndex = oldFirmwareTargetIndex
		firmwareTargetItems = oldFirmwareTargetItems
	})
}

// rowText returns the plain characters of a 1-based frame row.
func rowText(f *frame, row int) string {
	var b strings.Builder
	for col := 1; col <= f.width; col++ {
		b.WriteRune(f.cells[(row-1)*f.width+col-1].ch)
	}
	return b.String()
}

// textColumn returns the 1-based column where label starts on a frame row.
func textColumn(t *testing.T, f *frame, row int, label string) int {
	t.Helper()
	line := rowText(f, row)
	index := strings.Index(line, label)
	if index < 0 {
		t.Fatalf("row %d does not contain %q; row is %q", row, label, line)
	}
	return utf8.RuneCountInString(line[:index]) + 1
}

// newRenderTarget points the drawing code at a blank frame of a known size.
func newRenderTarget(t *testing.T, w, h int) *frame {
	t.Helper()
	width, height = w, h
	screen = newFrame(w, h)
	return screen
}

func TestStartMenuSelectionSurvivesAsyncInsertion(t *testing.T) {
	withMenuState(t)
	menuState = screenStartMenu
	startMenu = []menuItem{
		{id: 2, label: "Dongle settings"},
		{id: 4, label: "Firmware"},
		{id: 5, label: "Quit"},
	}
	currentSelection = 2
	rememberStartMenuSelection()

	// A background scan reports the dongle's pairing list, which inserts
	// "Remembered devices" above Quit and shifts every later index.
	startMenu = []menuItem{
		{id: 2, label: "Dongle settings"},
		{id: 4, label: "Firmware"},
		{id: 1, label: "Remembered devices (1)"},
		{id: 5, label: "Quit"},
	}
	syncStartMenuSelection()

	if currentSelection != 3 {
		t.Fatalf("selection index = %d, want 3", currentSelection)
	}
	if got := startMenu[currentSelection].id; got != 5 {
		t.Fatalf("insertion moved the highlight to item id %d, want Quit (id 5)", got)
	}
}

func TestStartMenuSelectionSurvivesAsyncRemoval(t *testing.T) {
	withMenuState(t)
	menuState = screenStartMenu
	startMenu = []menuItem{
		{id: 2, label: "Dongle settings"},
		{id: 1, label: "Remembered devices (1)"},
		{id: 4, label: "Firmware"},
		{id: 5, label: "Quit"},
	}
	currentSelection = 3
	rememberStartMenuSelection()

	// The pairing list disappears; Quit must keep the highlight.
	startMenu = []menuItem{
		{id: 2, label: "Dongle settings"},
		{id: 4, label: "Firmware"},
		{id: 5, label: "Quit"},
	}
	syncStartMenuSelection()

	if got := startMenu[currentSelection].id; got != 5 {
		t.Fatalf("removal moved the highlight to item id %d, want Quit (id 5)", got)
	}
}

func TestStartMenuSelectionFallsBackWhenItemDisappears(t *testing.T) {
	withMenuState(t)
	menuState = screenStartMenu
	startMenu = []menuItem{
		{id: 2, label: "Dongle settings"},
		{id: 4, label: "Firmware"},
		{id: 1, label: "Remembered devices (1)"},
		{id: 5, label: "Quit"},
	}
	currentSelection = 2
	rememberStartMenuSelection()

	startMenu = []menuItem{
		{id: 2, label: "Dongle settings"},
		{id: 4, label: "Firmware"},
		{id: 5, label: "Quit"},
	}
	syncStartMenuSelection()

	if currentSelection < 0 || currentSelection >= len(startMenu) {
		t.Fatalf("selection index %d is out of range for %d items", currentSelection, len(startMenu))
	}
	if startMenuSelectionID != startMenu[currentSelection].id {
		t.Fatalf("remembered id %d does not match the highlighted item id %d",
			startMenuSelectionID, startMenu[currentSelection].id)
	}
}

func TestStartMenuSyncLeavesOtherScreensAlone(t *testing.T) {
	withMenuState(t)
	menuState = screenPairedDevices
	startMenu = []menuItem{{id: 2, label: "Dongle settings"}, {id: 5, label: "Quit"}}
	currentSelection = 4
	startMenuSelectionID = 5

	syncStartMenuSelection()

	if currentSelection != 4 {
		t.Fatalf("home-menu sync changed the paired-device selection to %d", currentSelection)
	}
}

func TestSelectedRowKeepsTextColumn(t *testing.T) {
	withMenuState(t)
	const row = 11
	const label = "Firmware"

	f := newRenderTarget(t, 80, 24)
	drawCentered(row, label, false)
	plain := textColumn(t, f, row, label)

	f.resize(width, height)
	drawCentered(row, label, true)
	highlighted := textColumn(t, f, row, label)

	if plain != highlighted {
		t.Fatalf("selected label starts at column %d but unselected at column %d", highlighted, plain)
	}
}

func TestMenuColumnsAreStableAcrossSelection(t *testing.T) {
	withMenuState(t)
	labels := []string{"Dongle settings", "Firmware", "Remembered devices (1)", "Quit"}
	const startRow = 11

	f := newRenderTarget(t, 80, 24)
	for i, label := range labels {
		drawCentered(startRow+i, label, false)
	}
	want := make([]int, len(labels))
	for i, label := range labels {
		want[i] = textColumn(t, f, startRow+i, label)
	}

	for selected := range labels {
		f.resize(width, height)
		for i, label := range labels {
			drawCentered(startRow+i, label, i == selected)
		}
		for i, label := range labels {
			if got := textColumn(t, f, startRow+i, label); got != want[i] {
				t.Fatalf("with row %d selected, %q moved from column %d to %d",
					selected, label, want[i], got)
			}
		}
	}
}

func TestSelectionHighlightSurroundsLabel(t *testing.T) {
	withMenuState(t)
	const row = 11
	const label = "Firmware"

	f := newRenderTarget(t, 80, 24)
	drawCentered(row, label, true)
	col := textColumn(t, f, row, label)

	first := col - selectionPadding
	last := col + utf8.RuneCountInString(label) + selectionPadding - 1
	for c := first; c <= last; c++ {
		if got := f.cells[(row-1)*f.width+c-1].style; got != styleSelected {
			t.Fatalf("column %d has style %q, want the selection style %q", c, got, styleSelected)
		}
	}
	if got := f.cells[(row-1)*f.width+first-2].style; got == styleSelected {
		t.Fatalf("highlight leaks past its left edge at column %d", first-1)
	}
	if got := f.cells[(row-1)*f.width+last].style; got == styleSelected {
		t.Fatalf("highlight leaks past its right edge at column %d", last+1)
	}
}

func TestSelectionHighlightStaysInsidePanel(t *testing.T) {
	withMenuState(t)
	f := newRenderTarget(t, 34, 24)
	left, right, _ := panelBounds()

	// A label wider than the panel must still leave room for the highlight.
	drawCentered(11, strings.Repeat("X", 200), true)

	line := rowText(f, 11)
	firstStyled, lastStyled := -1, -1
	for col := 1; col <= f.width; col++ {
		if f.cells[(11-1)*f.width+col-1].style == styleSelected {
			if firstStyled < 0 {
				firstStyled = col
			}
			lastStyled = col
		}
	}
	if firstStyled <= left || lastStyled >= right {
		t.Fatalf("highlight spans columns %d..%d, outside panel borders %d..%d (row %q)",
			firstStyled, lastStyled, left, right, line)
	}
}

func TestFrameRenderAvoidsScreenClear(t *testing.T) {
	f := newFrame(20, 3)
	f.setText(1, 1, "hello", styleText)
	out := f.render()

	if strings.Contains(out, "\033[2J") {
		t.Fatalf("frame render clears the screen: %q", out)
	}
	if !strings.HasPrefix(out, "\033[H") {
		t.Fatalf("frame render does not home the cursor first: %q", out)
	}
	if got := strings.Count(out, "\033[K"); got != 3 {
		t.Fatalf("frame render erased %d row tails, want one per row (3): %q", got, out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("frame render dropped the painted text: %q", out)
	}
}

func TestFrameSetTextClipsToBounds(t *testing.T) {
	f := newFrame(6, 2)
	f.setText(1, 4, "abcdef", styleText) // runs off the right edge
	f.setText(2, -2, "wxyz", styleText)  // starts left of the frame
	f.setText(9, 1, "nope", styleText)   // below the frame

	if got := rowText(f, 1); got != "   abc" {
		t.Fatalf("row 1 = %q, want %q", got, "   abc")
	}
	if got := rowText(f, 2); got != "z     " {
		t.Fatalf("row 2 = %q, want %q", got, "z     ")
	}
}

func TestFrameResizeBlanksPreviousContent(t *testing.T) {
	f := newFrame(10, 2)
	f.setText(1, 1, "stale", styleSelected)
	f.resize(10, 2)
	if got := rowText(f, 1); got != strings.Repeat(" ", 10) {
		t.Fatalf("resize left stale content %q", got)
	}
	if got := f.cells[0].style; got != styleBase {
		t.Fatalf("resize left stale style %q", got)
	}
}

func TestFactoryResetRequiresTwoPressesInsideWindow(t *testing.T) {
	withMenuState(t)
	resetConfirmUntil = time.Time{}
	now := time.Unix(100, 0)

	if confirmFactoryReset(now) {
		t.Fatal("first factory-reset press confirmed the operation")
	}
	if resetConfirmUntil != now.Add(factoryResetConfirmWindow) {
		t.Fatalf("confirmation deadline = %v", resetConfirmUntil)
	}
	if !confirmFactoryReset(now.Add(time.Second)) {
		t.Fatal("second factory-reset press inside the window did not confirm")
	}
	if !resetConfirmUntil.IsZero() {
		t.Fatal("successful confirmation was not consumed")
	}
}

func TestFactoryResetConfirmationExpires(t *testing.T) {
	withMenuState(t)
	now := time.Unix(100, 0)
	resetConfirmUntil = now.Add(time.Second)

	if confirmFactoryReset(now.Add(2 * time.Second)) {
		t.Fatal("expired factory-reset confirmation was accepted")
	}
	if resetConfirmUntil != now.Add(2*time.Second).Add(factoryResetConfirmWindow) {
		t.Fatal("expired confirmation was not re-armed")
	}
}

func TestForgetRememberedDeviceRequiresSameDeviceTwice(t *testing.T) {
	withMenuState(t)
	now := time.Unix(200, 0)
	first := [6]byte{1, 2, 3, 4, 5, 6}
	second := [6]byte{6, 5, 4, 3, 2, 1}
	if confirmForgetRememberedDevice(now, first) {
		t.Fatal("first forget press confirmed")
	}
	if confirmForgetRememberedDevice(now.Add(time.Second), second) {
		t.Fatal("different remembered device reused confirmation")
	}
	if !confirmForgetRememberedDevice(now.Add(2*time.Second), second) {
		t.Fatal("second press for the same remembered device did not confirm")
	}
}

func TestSplitActionBarPlacesActionsOnRight(t *testing.T) {
	withMenuState(t)
	f := newRenderTarget(t, 100, 24)
	drawSplitActionBar([]string{"Q Back"}, []string{"Move", "Enter Change"})
	_, _, bottom := panelBounds()
	row := rowText(f, bottom+2)
	back := strings.Index(row, "Q Back")
	move := strings.Index(row, "Move")
	enter := strings.Index(row, "Enter Change")
	if back < 0 || move < 0 || enter < 0 {
		t.Fatalf("action row is missing labels: %q", row)
	}
	if back >= move || move >= enter || move <= len(row)/2 {
		t.Fatalf("actions are not split left/right: %q", row)
	}
}

func TestDeviceSettingsScrollKeepsLargeListSelectionVisible(t *testing.T) {
	withMenuState(t)
	f := newRenderTarget(t, 80, 18)
	currentSelection = 24
	values := make([]deviceSettingValue, 0, 25)
	for index := 0; index < 25; index++ {
		value := boolSettingValue{
			Definition: boolSettingDefinition{Key: fmt.Sprintf("setting-%02d", index), Label: fmt.Sprintf("Setting %02d", index)},
			Value:      index%2 == 0,
			Editable:   true,
		}
		values = append(values, deviceSettingValue{Boolean: &value})
	}
	renderDeviceSettings([]menuItem{{id: -1, label: "Device: Test headset"}}, values)

	var rendered strings.Builder
	for row := 1; row <= f.height; row++ {
		rendered.WriteString(rowText(f, row))
	}
	if !strings.Contains(rendered.String(), "Setting 24") {
		t.Fatalf("selected setting was not kept visible: %q", rendered.String())
	}
	if strings.Contains(rendered.String(), "Setting 00") {
		t.Fatalf("large settings list did not scroll: %q", rendered.String())
	}
}

func TestFirmwareTargetCyclesBetweenDongleAndHeadset(t *testing.T) {
	withMenuState(t)
	firmwareTargetItems = []switchDeviceItem{
		{RegistryID: 4, Device: &jabra_DeviceInfo{deviceName: "Link 380", isDongle: true}},
		{RegistryID: 7, Device: &jabra_DeviceInfo{deviceName: "Headset"}},
	}
	firmwareTargetIndex = 0
	firmwareTargetID = 4

	if !advanceFirmwareTarget() || firmwareTargetIndex != 1 || firmwareTargetID != 7 {
		t.Fatalf("first target advance = index %d id %d", firmwareTargetIndex, firmwareTargetID)
	}
	if !advanceFirmwareTarget() || firmwareTargetIndex != 0 || firmwareTargetID != 4 {
		t.Fatalf("wrapped target advance = index %d id %d", firmwareTargetIndex, firmwareTargetID)
	}
}

func TestFirmwareInstallStatus(t *testing.T) {
	tests := []struct {
		name string
		view firmwareViewState
		want string
	}{
		{
			name: "already current",
			view: firmwareViewState{currentVersion: "1.16.0", latestVersion: "1.16.0"},
			want: "Not needed",
		},
		{
			name: "download first",
			view: firmwareViewState{currentVersion: "1.15.0", latestVersion: "1.16.0"},
			want: "Download the update first",
		},
		{
			name: "needs recovery test",
			view: firmwareViewState{currentVersion: "1.15.0", latestVersion: "1.16.0", downloadedPath: "firmware.zip"},
			want: "Recovery test required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := firmwareInstallStatus(test.view); !strings.Contains(got, test.want) {
				t.Fatalf("firmwareInstallStatus() = %q, want substring %q", got, test.want)
			}
		})
	}
}

func TestRootQQuitsTUI(t *testing.T) {
	previous := menuState
	menuState = screenStartMenu
	t.Cleanup(func() { menuState = previous })

	if !handleKeyEvent(keyBack, make(chan actionResult, 1)) {
		t.Fatal("q on the home screen did not quit")
	}
}

func TestQReturnsFromDetails(t *testing.T) {
	previousState := menuState
	previousSelection := currentSelection
	menuState = screenDongleSettings
	currentSelection = 3
	t.Cleanup(func() {
		menuState = previousState
		currentSelection = previousSelection
	})

	if handleKeyEvent(keyBack, make(chan actionResult, 1)) {
		t.Fatal("q on the details screen quit instead of going home")
	}
	if menuState != screenStartMenu || currentSelection != 0 {
		t.Fatalf("q returned to state=%d selection=%d", menuState, currentSelection)
	}
}

func TestParseBasicNavigationKeys(t *testing.T) {
	events := parseKeyEvents([]byte{'w', 's', '\r', 'q'})
	want := []keyEvent{keyUp, keyDown, keyEnter, keyBack}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d", len(events), len(want))
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("event %d = %v, want %v", i, events[i], want[i])
		}
	}
}

func TestKeyDecoderKeepsBatchedKeysAfterArrow(t *testing.T) {
	decoder := &keyDecoder{}
	got := decoder.feed([]byte{'\x1b', '[', 'B', '\r', 'q'})
	want := []keyEvent{keyDown, keyEnter, keyBack}
	if len(got) != len(want) {
		t.Fatalf("got %d events (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestKeyDecoderJoinsSplitArrowSequence(t *testing.T) {
	decoder := &keyDecoder{}
	if got := decoder.feed([]byte{'\x1b'}); len(got) != 0 {
		t.Fatalf("partial escape emitted events: %v", got)
	}
	if got := decoder.feed([]byte{'['}); len(got) != 0 {
		t.Fatalf("partial control sequence emitted events: %v", got)
	}
	got := decoder.feed([]byte{'A', 's'})
	want := []keyEvent{keyUp, keyDown}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("split sequence decoded as %v, want %v", got, want)
	}
}

func TestUIActionResultIsNotDropped(t *testing.T) {
	results := make(chan actionResult)
	runUIAction(results, "done", func() error { return nil })
	select {
	case result := <-results:
		if result.message != "done" || result.err != nil {
			t.Fatalf("unexpected result: %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("UI action result was dropped")
	}
}
