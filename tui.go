package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Watchdog0x/jabridge/internal/buildinfo"
	firmwaretool "github.com/Watchdog0x/jabridge/internal/firmware"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type keyEvent int

const (
	keyNone keyEvent = iota
	keyUp
	keyDown
	keyEnter
	keyBack
	keyAction1
	keyAction2
	keyAction3
	keyAction4
)

type actionResult struct {
	message               string
	err                   error
	returnToMainMenu      bool
	clearSearchResults    bool
	refreshDongleSettings bool
}

var (
	loading      = [10]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	loadingIndex = 0

	verticalLine      = "┃"
	leftCornerTop     = "┏"
	rightCornerTop    = "┓"
	leftCornerBottom  = "┗"
	rightCornerBottom = "┛"

	width, height = 0, 0

	currentSelection = 0
	menuState        = 0

	// startMenuSelectionID remembers which logical home-screen action is
	// highlighted. The home menu is rebuilt from device state on every frame,
	// so an index alone would slide onto a different action as soon as an
	// asynchronous scan inserts or removes an entry.
	startMenuSelectionID = -1

	// screen is the off-screen buffer every render pass paints into.
	screen = newFrame(0, 0)

	selectedItemsSearchForNewDevices = -1
	menuItemsSearchForNewDevices     = [2]string{"Q Back", "1 Connect"}

	statusMessage     string
	statusIsError     bool
	statusUntil       time.Time
	flashUntil        time.Time
	resetConfirmUntil time.Time

	nextSearchRefresh time.Time
	uiRevision        atomic.Uint64
	firmwareViewMu    sync.RWMutex
	firmwareView      firmwareViewState
)

type firmwareViewState struct {
	loading        bool
	deviceName     string
	currentVersion string
	latestVersion  string
	downloadedPath string
}

const (
	batteryFullChar           = "◼"
	batteryEmptyChar          = "◻"
	batteryWidth              = 10
	lowBatteryThreshold       = 20
	factoryResetConfirmWindow = 10 * time.Second

	screenStartMenu = iota
	screenSearch
	screenPairedDevices
	screenDongleSettings
	screenSwitchDevice
	screenFirmware
)

func enableRawMode() (*unix.Termios, error) {
	fd := int(os.Stdin.Fd())

	oldSettings, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}

	newSettings := *oldSettings
	newSettings.Lflag &^= unix.ECHO | unix.ICANON
	newSettings.Iflag &^= unix.ICRNL
	newSettings.Cc[unix.VMIN] = 1
	newSettings.Cc[unix.VTIME] = 0

	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &newSettings); err != nil {
		return nil, err
	}

	return oldSettings, nil
}

func restoreTerminal(oldSettings *unix.Termios) {
	if oldSettings != nil {
		_ = unix.IoctlSetTermios(int(os.Stdin.Fd()), unix.TCSETS, oldSettings)
	}
}

func startKeysPressedListener(ctx context.Context, keyEvents chan<- keyEvent) {
	fd := int(os.Stdin.Fd())
	nonblocking := unix.SetNonblock(fd, true) == nil
	if nonblocking {
		defer func() { _ = unix.SetNonblock(fd, false) }()
	}

	buf := make([]byte, 16)
	decoder := &keyDecoder{}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := os.Stdin.Read(buf)
		if n > 0 {
			for _, event := range decoder.feed(buf[:n]) {
				select {
				case keyEvents <- event:
				case <-ctx.Done():
					return
				default:
				}
			}
			continue
		}

		if err != nil {
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

type keyDecoder struct {
	pending []byte
}

// feed accepts arbitrary terminal read chunks. Escape sequences may be split
// across reads or several keys may arrive together, so it retains incomplete
// bytes and consumes every complete event in order.
func (d *keyDecoder) feed(input []byte) []keyEvent {
	d.pending = append(d.pending, input...)
	events := make([]keyEvent, 0, len(d.pending))
	for len(d.pending) > 0 {
		if d.pending[0] == 0x1b {
			if len(d.pending) < 2 {
				break
			}
			if d.pending[1] != '[' {
				d.pending = d.pending[1:]
				continue
			}
			if len(d.pending) < 3 {
				break
			}
			switch d.pending[2] {
			case 'A':
				events = append(events, keyUp)
			case 'B':
				events = append(events, keyDown)
			}
			d.pending = d.pending[3:]
			continue
		}

		if event := basicKeyEvent(d.pending[0]); event != keyNone {
			events = append(events, event)
		}
		d.pending = d.pending[1:]
	}
	return events
}

func parseKeyEvents(input []byte) []keyEvent {
	decoder := &keyDecoder{}
	return decoder.feed(input)
}

func basicKeyEvent(b byte) keyEvent {
	switch b {
	case 'w', 'W':
		return keyUp
	case 's', 'S':
		return keyDown
	case '\r', '\n':
		return keyEnter
	case 'q', 'Q':
		return keyBack
	case '1':
		return keyAction1
	case '2':
		return keyAction2
	case '3':
		return keyAction3
	case '4':
		return keyAction4
	}
	return keyNone
}

func handleKeyEvent(event keyEvent, results chan<- actionResult) bool {
	switch event {
	case keyUp:
		handleUpKey()
	case keyDown:
		handleDownKey()
	case keyBack:
		return handleBackKey(results)
	case keyEnter:
		return handleEnterKey(results)
	case keyAction1, keyAction2, keyAction3, keyAction4:
		handleActionKey(event, results)
	}
	return false
}

func handleBackKey(results chan<- actionResult) bool {
	switch menuState {
	case screenStartMenu:
		return true
	case screenSearch:
		returnToStartMenu()
		runUIAction(results, "Pairing stopped", func() error {
			return setDongleInBTPairing(false)
		})
	case screenPairedDevices, screenDongleSettings, screenSwitchDevice, screenFirmware:
		resetConfirmUntil = time.Time{}
		returnToStartMenu()
	}
	return false
}

func handleEnterKey(results chan<- actionResult) bool {
	switch menuState {
	case screenStartMenu:
		if currentSelection < 0 || currentSelection >= len(startMenu) {
			currentSelection = clampSelection(currentSelection, len(startMenu))
			rememberStartMenuSelection()
			return false
		}
		return activateStartMenuItem(startMenu[currentSelection], results)
	case screenDongleSettings:
		if experimentalDeviceWritesEnabled() {
			setStatus("Press 1 to toggle auto pairing or 2 twice to factory reset", false)
		} else {
			setStatus("Settings are read-only; hardware-write testing is tracked in issue #36", false)
		}
	case screenFirmware:
		setStatus(firmwareActionHint(), false)
	}
	return false
}

func activateStartMenuItem(item menuItem, results chan<- actionResult) bool {
	currentSelection = 0

	switch item.id {
	case 0:
		menuState = screenSearch
		clearSearchResults()
		setStatus("Searching for devices...", false)
		runUIAction(results, "Device search started", searchForNewDevices)
	case 1:
		menuState = screenPairedDevices
	case 2:
		menuState = screenDongleSettings
		updateDongleSettings()
	case 4:
		menuState = screenFirmware
		refreshFirmwareView(results)
	case 3:
		menuState = screenSwitchDevice
	case 5:
		return true
	default:
		returnToStartMenu()
	}
	return false
}

func handleActionKey(event keyEvent, results chan<- actionResult) {
	switch menuState {
	case screenSearch:
		if event != keyAction1 {
			return
		}
		selectedItemsSearchForNewDevices = 1
		flashUntil = time.Now().Add(200 * time.Millisecond)
		if len(searchDeviceList.pairedDevices) == 0 {
			setStatus("No searched device selected", true)
			return
		}
		selection := currentSelection
		setStatus("Connecting searched device...", false)
		runUIAction(results, "Device connect command sent", func() error {
			return connectNewDevice(uint16(selection))
		}, withReturnToMainMenu())
	case screenPairedDevices:
		setStatus("Pairing changes are locked in this preview", true)
	case screenDongleSettings:
		handleDongleSettingsAction(event, results)
	case screenFirmware:
		if event != keyAction1 {
			return
		}
		device, exists := selectedFirmwareDevice()
		if !exists {
			setStatus("No supported device connected", true)
			return
		}
		setStatus("Downloading firmware file...", false)
		runUIAction(results, "Firmware downloaded to ./firmware", func() error {
			result, err := firmwaretool.DownloadLatest(device.productID, "./firmware")
			if err != nil {
				return err
			}
			firmwareViewMu.Lock()
			firmwareView.downloadedPath = result.Path
			firmwareViewMu.Unlock()
			requestUIRedraw()
			return nil
		})
	}
}

func handleDongleSettingsAction(event keyEvent, results chan<- actionResult) {
	dongle, exists := selectedDongleSnapshot()
	if !exists {
		setStatus("No dongle connected", true)
		return
	}
	if !supportsExperimentalDongleWrites(dongle.productID) {
		setStatus(fmt.Sprintf("Writes are not supported for dongle PID 0x%04x", dongle.productID), true)
		return
	}
	if !experimentalDeviceWritesEnabled() {
		setStatus("Read-only mode; hardware-write testing is tracked in issue #36", true)
		return
	}

	switch event {
	case keyAction1:
		resetConfirmUntil = time.Time{}
		setStatus("Changing auto-pairing setting...", false)
		runUIAction(results, "Auto-pairing setting changed", func() error {
			current, err := getAutoPairing()
			if err != nil {
				return err
			}
			return setAutoPairing(!current)
		}, withDongleSettingsRefresh())
	case keyAction2:
		if !confirmFactoryReset(time.Now()) {
			setStatus("WARNING: reset erases remembered headsets; press 2 again within 10 seconds", true)
			return
		}
		deviceID := dongle.deviceID
		setStatus("Sending factory-reset command...", false)
		runUIAction(results, "Factory-reset command accepted; waiting for dongle reconnect", func() error {
			return factoryReset(deviceID)
		})
	}
}

// confirmFactoryReset implements a deliberate two-press confirmation. The
// first press arms a short window; the second press consumes it and allows the
// caller to perform the destructive operation.
func confirmFactoryReset(now time.Time) bool {
	if !resetConfirmUntil.IsZero() && !now.After(resetConfirmUntil) {
		resetConfirmUntil = time.Time{}
		return true
	}
	resetConfirmUntil = now.Add(factoryResetConfirmWindow)
	requestUIRedraw()
	return false
}

type actionOption func(*actionResult)

func withReturnToMainMenu() actionOption {
	return func(result *actionResult) {
		result.returnToMainMenu = true
	}
}

func withDongleSettingsRefresh() actionOption {
	return func(result *actionResult) {
		result.refreshDongleSettings = true
	}
}

func runUIAction(results chan<- actionResult, successMessage string, action func() error, options ...actionOption) {
	go func() {
		result := actionResult{message: successMessage}
		for _, option := range options {
			option(&result)
		}
		result.err = action()

		results <- result
	}()
}

func refreshFirmwareView(results chan<- actionResult) {
	selected, exists := selectedFirmwareDevice()
	if !exists {
		firmwareViewMu.Lock()
		firmwareView = firmwareViewState{}
		firmwareViewMu.Unlock()
		setStatus("No supported device connected", true)
		return
	}
	device := *selected
	firmwareViewMu.Lock()
	firmwareView = firmwareViewState{loading: true, deviceName: device.deviceName}
	firmwareViewMu.Unlock()
	requestUIRedraw()

	runUIAction(results, "Firmware information ready", func() error {
		current, currentErr := readFirmwareVersion(&device)
		latest, latestErr := firmwaretool.LatestForPID(device.productID)

		firmwareViewMu.Lock()
		firmwareView.loading = false
		firmwareView.currentVersion = current
		firmwareView.latestVersion = latest.Version
		firmwareViewMu.Unlock()
		requestUIRedraw()

		if currentErr != nil && latestErr != nil {
			return fmt.Errorf("device read: %v; online check: %v", currentErr, latestErr)
		}
		if currentErr != nil {
			return fmt.Errorf("device firmware read: %w", currentErr)
		}
		if latestErr != nil {
			return fmt.Errorf("latest firmware check: %w", latestErr)
		}
		return nil
	})
}

func selectedFirmwareDevice() (*jabra_DeviceInfo, bool) {
	if dongle, exists := selectedDongleSnapshot(); exists {
		return dongle, true
	}
	return selectedHeadsetSnapshot()
}

func applyActionResult(result actionResult) {
	if result.err != nil {
		setStatus(result.err.Error(), true)
		return
	} else if result.message != "" {
		setStatus(result.message, false)
	}
	if result.returnToMainMenu {
		returnToStartMenu()
	}
	if result.clearSearchResults {
		clearSearchResults()
	}
	if result.refreshDongleSettings && menuState == screenDongleSettings {
		updateDongleSettings()
	}
}

func handleUpKey() {
	currentSelection = clampSelection(currentSelection-1, currentMenuLength())
	rememberStartMenuSelection()
}

func handleDownKey() {
	currentSelection = clampSelection(currentSelection+1, currentMenuLength())
	rememberStartMenuSelection()
}

// rememberStartMenuSelection records the id of the home-screen item under the
// cursor so a later menu rebuild can put the highlight back on the same action.
func rememberStartMenuSelection() {
	if menuState != screenStartMenu {
		return
	}
	if currentSelection >= 0 && currentSelection < len(startMenu) {
		startMenuSelectionID = startMenu[currentSelection].id
		return
	}
	startMenuSelectionID = -1
}

// syncStartMenuSelection re-resolves currentSelection against the freshly
// rebuilt home menu. The remembered item keeps the highlight wherever it moved
// to; only if it disappeared entirely do we fall back to the nearest index.
func syncStartMenuSelection() {
	if menuState != screenStartMenu {
		return
	}
	if len(startMenu) == 0 {
		currentSelection = 0
		startMenuSelectionID = -1
		return
	}
	if index := indexOfStartMenuID(startMenuSelectionID); index >= 0 {
		currentSelection = index
		return
	}
	currentSelection = clampSelection(currentSelection, len(startMenu))
	startMenuSelectionID = startMenu[currentSelection].id
}

// indexOfStartMenuID returns the position of id in the home menu, or -1.
func indexOfStartMenuID(id int) int {
	if id < 0 {
		return -1
	}
	for i, item := range startMenu {
		if item.id == id {
			return i
		}
	}
	return -1
}

// returnToStartMenu goes home and puts the highlight on the first action.
func returnToStartMenu() {
	menuState = screenStartMenu
	currentSelection = 0
	startMenuSelectionID = -1
	syncStartMenuSelection()
}

func currentMenuLength() int {
	switch menuState {
	case screenStartMenu:
		return len(startMenu)
	case screenSearch:
		return len(searchDeviceList.pairedDevices)
	case screenPairedDevices:
		if dongle, exists := selectedDongleSnapshot(); exists && dongle.pairingList != nil {
			return len(dongle.pairingList.pairedDevices)
		}
	case screenDongleSettings:
		return 0
	case screenFirmware:
		return 0
	}
	return 0
}

func clampSelection(selection, count int) int {
	if count <= 0 {
		return 0
	}
	if selection < 0 {
		return 0
	}
	if selection >= count {
		return count - 1
	}
	return selection
}

// SGR parameter lists used by the UI. Every style carries an explicit
// background so a cell is self-describing and the frame never depends on
// whatever attributes the terminal happened to be left in.
const (
	styleBase     = "0;40;97"
	styleText     = "0;40;97"
	styleBorder   = "0;40;97"
	styleTitle    = "1;40;96"
	styleWarn     = "1;40;93"
	styleSelected = "1;30;106"
	styleAction   = "1;30;107"
	styleBadge    = "1;30;103"
	styleAlert    = "1;97;41"
	styleBattOK   = "0;40;32"
	styleBattWarn = "0;40;33"
	styleBattLow  = "0;40;31"
)

// cell is one character position of a composed frame.
type cell struct {
	ch    rune
	style string
}

// frame is an off-screen character buffer. A render pass paints a complete
// frame and flushFrame emits it in a single write, so the terminal is never
// shown a cleared or half-drawn screen.
type frame struct {
	width  int
	height int
	cells  []cell
}

func newFrame(width, height int) *frame {
	f := &frame{}
	f.resize(width, height)
	return f
}

// resize prepares the buffer for a frame of the given size and blanks it.
func (f *frame) resize(width, height int) {
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	if f.width != width || f.height != height {
		f.width, f.height = width, height
		f.cells = make([]cell, width*height)
	}
	for i := range f.cells {
		f.cells[i] = cell{ch: ' ', style: styleBase}
	}
}

// setText paints text with its first character at the 1-based (row, col)
// position. Anything outside the frame is clipped. text must be plain text:
// styling is carried by style, never by escapes embedded in the string.
func (f *frame) setText(row, col int, text, style string) {
	if row < 1 || row > f.height {
		return
	}
	if style == "" {
		style = styleBase
	}
	base := (row - 1) * f.width
	for _, r := range text {
		if col > f.width {
			return
		}
		if col >= 1 {
			f.cells[base+col-1] = cell{ch: r, style: style}
		}
		col++
	}
}

// render serializes the frame as one escape-sequence string. It homes the
// cursor and rewrites each row in place, erasing the tail of the row with
// the base style instead of clearing the screen first. That keeps updates
// free of the flash a full clear produces.
func (f *frame) render() string {
	var b strings.Builder
	b.Grow(len(f.cells) + f.height*16 + 8)
	for row := 0; row < f.height; row++ {
		if row == 0 {
			b.WriteString("\033[H")
		} else {
			fmt.Fprintf(&b, "\033[%d;1H", row+1)
		}

		line := f.cells[row*f.width : (row+1)*f.width]
		// Trailing default-styled blanks are covered by the erase below.
		end := f.width
		for end > 0 && line[end-1].ch == ' ' && line[end-1].style == styleBase {
			end--
		}

		style := ""
		for i := 0; i < end; i++ {
			if line[i].style != style {
				style = line[i].style
				b.WriteString("\033[" + style + "m")
			}
			b.WriteRune(line[i].ch)
		}
		if style != styleBase {
			b.WriteString("\033[" + styleBase + "m")
		}
		b.WriteString("\033[K")
	}
	return b.String()
}

// flushFrame writes the composed frame to the terminal in one syscall.
func flushFrame(f *frame) {
	_, _ = os.Stdout.WriteString(f.render())
}

// clearScreen is only used once when entering the alternate screen.
func clearScreen() {
	fmt.Print("\033[" + styleBase + "m\033[2J\033[H")
}

func getScreenSize() {
	getWidth, getHeight, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		width, height = 80, 24
		return
	}
	width, height = getWidth, getHeight
}

func drawingBox() {
	if width < 18 || height < 8 {
		return
	}

	topRow := 4
	leftCol, rightCol, bottomRow := panelBounds()
	innerWidth := rightCol - leftCol - 1
	if innerWidth < 1 || bottomRow <= topRow {
		return
	}

	line := strings.Repeat("━", innerWidth)
	screen.setText(topRow, leftCol, leftCornerTop+line+rightCornerTop, styleBorder)

	for row := topRow + 1; row < bottomRow; row++ {
		screen.setText(row, leftCol, verticalLine, styleBorder)
		screen.setText(row, rightCol, verticalLine, styleBorder)
	}

	screen.setText(bottomRow, leftCol, leftCornerBottom+line+rightCornerBottom, styleBorder)
}

func panelBounds() (left, right, bottom int) {
	panelWidth := width - 10
	if panelWidth > 88 {
		panelWidth = 88
	}
	if panelWidth < 20 {
		panelWidth = 20
	}
	left = (width - panelWidth) / 2
	right = left + panelWidth
	bottom = height - 4
	if bottom > 20 {
		bottom = 20
	}
	return left, right, bottom
}

func header() {
	left, right, _ := panelBounds()
	const title = "Jabridge"
	screen.setText(1, left, title, styleTitle)
	screen.setText(1, left+displayWidth(title)+2, buildinfo.Version, styleText)

	mode := "READ-ONLY PREVIEW"
	modeStyle := styleBadge
	if experimentalDeviceWritesEnabled() {
		mode = "DEVELOPER WRITES ENABLED"
		modeStyle = styleAlert
	}
	screen.setText(1, max(left, right-displayWidth(mode)-2), " "+mode+" ", modeStyle)

	dongle, exists := selectedDongleSnapshot()
	switch {
	case exists:
		screen.setText(2, left, "Dongle: "+trimToWidth(dongle.deviceName, max(10, width/3)), styleText)
	case !firstScanComplete.Load():
		screen.setText(2, left, "Dongle: Scanning "+loading[loadingIndex], styleText)
		loadingIndex = (loadingIndex + 1) % len(loading)
	default:
		if headset, headsetExists := selectedHeadsetSnapshot(); headsetExists {
			screen.setText(2, left, "USB: "+trimToWidth(headset.deviceName, max(10, width/3)), styleText)
		} else {
			screen.setText(2, left, "Dongle: Not connected", styleText)
		}
	}

	drawHeadsetStatus(left, right)
}

// drawHeadsetStatus paints the right half of the second header row: the
// headset name and, when available, a coloured battery gauge. The gauge is
// laid out as three plain-text segments so the frame keeps one style per cell.
func drawHeadsetStatus(left, right int) {
	headset, exists := selectedHeadsetSnapshot()
	if !exists {
		label := "Headset: Scanning"
		if firstScanComplete.Load() {
			label = "Headset: Not connected"
		}
		screen.setText(2, max(left, right-displayWidth(label)), label, styleText)
		return
	}

	if headset.batteryStatus == nil || headset.batteryStatus.levelInPercent > 100 {
		label := fmt.Sprintf("Headset: %s  Battery unavailable", headset.deviceName)
		screen.setText(2, max(left, right-displayWidth(label)), label, styleText)
		return
	}

	levelInPercent := headset.batteryStatus.levelInPercent
	filledSegments := int(math.Round(float64(levelInPercent) / 100 * batteryWidth))
	if filledSegments < 0 {
		filledSegments = 0
	}
	if filledSegments > batteryWidth {
		filledSegments = batteryWidth
	}
	bar := strings.Repeat(batteryFullChar, filledSegments) +
		strings.Repeat(batteryEmptyChar, batteryWidth-filledSegments)

	barStyle := styleBattOK
	switch {
	case headset.batteryStatus.batteryLow || levelInPercent <= lowBatteryThreshold:
		barStyle = styleBattLow
	case levelInPercent <= 65:
		barStyle = styleBattWarn
	}

	prefix := fmt.Sprintf("Headset: %s  [", headset.deviceName)
	suffix := fmt.Sprintf("] %d%%", levelInPercent)
	if headset.batteryStatus.charging {
		suffix += " charging"
	}

	col := max(left, right-(displayWidth(prefix)+batteryWidth+displayWidth(suffix)))
	screen.setText(2, col, prefix, styleText)
	screen.setText(2, col+displayWidth(prefix), bar, barStyle)
	screen.setText(2, col+displayWidth(prefix)+batteryWidth, suffix, styleText)
}

func menu() {
	drawingBox()
	renderHomeSummary()

	if len(startMenu) == 0 {
		drawCentered(10, "No menu items available", false)
		return
	}

	startRow := 11
	if height < 18 {
		startRow = 7
	}
	for i, option := range startMenu {
		row := startRow + i
		_, _, panelBottom := panelBounds()
		if row >= panelBottom {
			break
		}
		drawCentered(row, option.label, i == currentSelection)
	}
	drawActionBar([]string{"↑/↓ Move", "Enter Open", "Q Quit"}, -1)
}

func renderHomeSummary() {
	if !firstScanComplete.Load() {
		drawCenteredStyled(6, "Looking for supported devices...", styleTitle)
		return
	}
	dongle, hasDongle := selectedDongleSnapshot()
	headset, hasHeadset := selectedHeadsetSnapshot()
	switch {
	case hasDongle && hasHeadset:
		drawCenteredStyled(6, "Headset connected", styleTitle)
		drawCentered(7, trimToWidth(headset.deviceName, max(12, width-16)), false)
	case hasDongle:
		drawCenteredStyled(6, "Dongle ready", styleTitle)
		drawCentered(7, fmt.Sprintf("%s  •  USB 0b0e:%04x", dongle.deviceName, dongle.productID), false)
		drawCentered(8, "No headset connected. Turn it on or connect it by USB.", false)
	case hasHeadset:
		drawCenteredStyled(6, "USB headset connected", styleTitle)
		drawCentered(7, trimToWidth(headset.deviceName, max(12, width-16)), false)
	default:
		drawCenteredStyled(6, "No supported device found", styleWarn)
		drawCentered(8, "Connect a Jabra headset or USB dongle.", false)
	}
}

func refreshSearchDeviceList() {
	if menuState != screenSearch || time.Now().Before(nextSearchRefresh) {
		return
	}
	nextSearchRefresh = time.Now().Add(time.Second)

	dongle, exists := selectedDongleSnapshot()
	if !exists {
		return
	}
	update := getSearchDeviceList(dongle.deviceID)
	if update == nil {
		return
	}
	searchDeviceList.count = update.count
	searchDeviceList.listType = update.listType
	searchDeviceList.pairedDevices = update.pairedDevices
	currentSelection = clampSelection(currentSelection, len(searchDeviceList.pairedDevices))
	requestUIRedraw()
}

func clearSearchResults() {
	searchDeviceList.count = 0
	searchDeviceList.listType = searchResult
	searchDeviceList.pairedDevices = nil
	nextSearchRefresh = time.Time{}
}

func menuSearchForNewDevices() {
	drawingBox()

	if len(searchDeviceList.pairedDevices) == 0 {
		drawCentered(5, "Searching...", false)
	} else {
		for i, pairedDevice := range searchDeviceList.pairedDevices {
			row := 5 + i
			_, _, panelBottom := panelBounds()
			if row >= panelBottom {
				break
			}
			device := fmt.Sprintf("%d %s", i+1, pairedDevice.deviceName)
			drawListItem(row, 10, device, i == currentSelection)
		}
	}

	drawActionBar(menuItemsSearchForNewDevices[:], selectedItemsSearchForNewDevices)
}

func menuPairedDevices() {
	drawingBox()

	dongle, exists := selectedDongleSnapshot()
	if !exists || dongle.pairingList == nil {
		drawCentered(5, "No dongle selected", true)
		return
	}

	if len(dongle.pairingList.pairedDevices) == 0 {
		drawCentered(5, "No paired devices remembered", false)
	} else {
		for i, pairedDevice := range dongle.pairingList.pairedDevices {
			row := 5 + i
			_, _, panelBottom := panelBounds()
			if row >= panelBottom {
				break
			}
			device := fmt.Sprintf("%d %s", i+1, pairedDevice.deviceName)
			if pairedDevice.isConnected {
				device += " (Connected)"
			}
			drawListItem(row, 10, device, i == currentSelection)
		}
	}

	drawActionBar([]string{"Q Back", "Read-only"}, -1)
}

func renderDongleSettings() {
	drawingBox()
	drawCenteredStyled(6, "Dongle settings", styleTitle)

	if len(dongleSettingsLines) == 0 {
		drawCentered(8, "No dongle connected", false)
	} else {
		for i, item := range dongleSettingsLines {
			row := 8 + i
			_, _, panelBottom := panelBounds()
			if row >= panelBottom {
				break
			}
			drawListItem(row, 10, item.label, false)
		}
	}

	actions := []string{"Q Back", "Read-only"}
	if dongle, exists := selectedDongleSnapshot(); exists &&
		experimentalDeviceWritesEnabled() && supportsExperimentalDongleWrites(dongle.productID) {
		actions = []string{"Q Back", "1 Toggle auto pairing", "2 Factory reset"}
		if !resetConfirmUntil.IsZero() && time.Now().Before(resetConfirmUntil) {
			actions = []string{"Q Cancel", "2 CONFIRM factory reset"}
		}
	}
	drawActionBar(actions, -1)
}

func renderFirmware() {
	drawingBox()
	drawCenteredStyled(6, "Firmware", styleTitle)
	firmwareViewMu.RLock()
	view := firmwareView
	firmwareViewMu.RUnlock()
	if view.loading {
		drawCentered(9, "Checking device and latest release...", false)
		drawActionBar([]string{"Q Back"}, -1)
		return
	}
	left, _, _ := panelBounds()
	row := 8
	lines := []string{
		fmt.Sprintf("Device:             %s", valueOrUnknown(view.deviceName)),
		fmt.Sprintf("Installed:          %s", valueOrUnknown(view.currentVersion)),
		fmt.Sprintf("Latest available:   %s", valueOrUnknown(view.latestVersion)),
	}
	if view.currentVersion != "" && view.latestVersion != "" {
		state := "Update available"
		if view.currentVersion == view.latestVersion {
			state = "Up to date"
		}
		lines = append(lines, fmt.Sprintf("Status:              %s", state))
	}
	if view.downloadedPath != "" {
		lines = append(lines, fmt.Sprintf("Downloaded:          %s", view.downloadedPath))
	}
	lines = append(lines, firmwareInstallStatus(view))
	for _, line := range lines {
		drawListItem(row, left+4, line, false)
		row++
	}
	actions := []string{"Q Back", "1 Download"}
	if view.currentVersion != "" && view.currentVersion == view.latestVersion {
		actions = append(actions, "Already up to date")
	} else {
		actions = append(actions, "Install test: issue #36")
	}
	drawActionBar(actions, -1)
}

func firmwareInstallStatus(view firmwareViewState) string {
	switch {
	case view.currentVersion != "" && view.currentVersion == view.latestVersion:
		return "Install:            Not needed (already up to date)"
	case view.downloadedPath == "":
		return "Install:            Download the update first"
	default:
		return "Install:            Recovery test required (issue #36)"
	}
}

func firmwareActionHint() string {
	firmwareViewMu.RLock()
	view := firmwareView
	firmwareViewMu.RUnlock()
	if view.currentVersion != "" && view.currentVersion == view.latestVersion {
		return "Firmware is already up to date; press 1 only to download a copy"
	}
	return "Press 1 to download; installation testing is tracked in issue #36"
}

func valueOrUnknown(value string) string {
	if value == "" {
		return "Unknown"
	}
	return value
}

func renderSwitchDevice() {
	drawingBox()
	drawCentered(5, "Switch Device", false)
	drawCentered(7, "Device switching is not implemented yet", true)
	drawActionBar([]string{"Q Back"}, -1)
}

// selectionPadding is how far the selected-row highlight extends on each side
// of the label. It is painted around the label rather than inserted before it,
// so selecting a row never moves its text.
const selectionPadding = 2

func drawCentered(row int, label string, selected bool) {
	drawListItem(row, labelColumnFor(label), label, selected)
}

func drawCenteredStyled(row int, label, style string) {
	screen.setText(row, labelColumnFor(label), label, style)
}

// labelColumnFor returns the column a centred label starts at. It depends only
// on the label, never on whether the row is selected.
func labelColumnFor(label string) int {
	return (width - displayWidth(label)) / 2
}

// drawListItem paints label with its first character at column col. col is the
// column of the text itself in both states: the selection highlight is drawn
// into the padding columns on either side, so a row keeps exactly the same
// text column whether or not it is selected.
func drawListItem(row, col int, label string, selected bool) {
	if row < 1 || row > height {
		return
	}
	left, right, _ := panelBounds()
	// Keep the highlight, not just the text, inside the panel border.
	if col < left+1+selectionPadding {
		col = left + 1 + selectionPadding
	}
	label = trimToWidth(label, max(1, right-selectionPadding-col))
	if !selected {
		screen.setText(row, col, label, styleText)
		return
	}
	pad := strings.Repeat(" ", selectionPadding)
	screen.setText(row, col-selectionPadding, pad+label+pad, styleSelected)
}

func drawActionBar(items []string, selected int) {
	if height < 8 {
		return
	}
	left, right, bottom := panelBounds()
	row := bottom + 2
	if row >= height {
		row = height - 1
	}
	col := left + 2
	for i, item := range items {
		if col >= right-2 {
			break
		}
		item = trimToWidth(item, max(1, right-col-2))
		style := styleAction
		if i == selected {
			style = styleSelected
		}
		screen.setText(row, col, " "+item+" ", style)
		col += displayWidth(item) + 4
	}
}

func renderStatus() {
	if statusMessage == "" || time.Now().After(statusUntil) || height < 8 {
		return
	}

	left, right, bottom := panelBounds()
	row := bottom + 3
	if row > height {
		row = height
	}
	col := left + 2
	message := trimToWidth(statusMessage, max(1, right-col-2))
	if statusIsError {
		screen.setText(row, col, " "+message+" ", styleAlert)
		return
	}
	screen.setText(row, col, message, styleTitle)
}

func setStatus(message string, isError bool) {
	statusMessage = message
	statusIsError = isError
	statusUntil = time.Now().Add(4 * time.Second)
	requestUIRedraw()
}

func requestUIRedraw() {
	uiRevision.Add(1)
}

func renderSmallTerminal() {
	screen.setText(1, 1, "Terminal too small. Resize to at least 30x8.", styleText)
}

func resetExpiredFlash() {
	if flashUntil.IsZero() || time.Now().Before(flashUntil) {
		return
	}
	selectedItemsSearchForNewDevices = -1
	flashUntil = time.Time{}
	requestUIRedraw()
}

func resetExpiredFactoryConfirmation() {
	if resetConfirmUntil.IsZero() || time.Now().Before(resetConfirmUntil) {
		return
	}
	resetConfirmUntil = time.Time{}
	requestUIRedraw()
}

func clearExpiredStatus() {
	if statusMessage == "" || time.Now().Before(statusUntil) {
		return
	}
	statusMessage = ""
	statusUntil = time.Time{}
	requestUIRedraw()
}

func displayWidth(s string) int {
	return utf8.RuneCountInString(stripANSI(s))
}

func trimToWidth(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == 0x1b:
			inEscape = true
		case inEscape && s[i] == 'm':
			inEscape = false
		case !inEscape:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// updateSelectionState rebuilds the home menu from the current device state
// and settles the selection before anything is drawn. Keeping this out of the
// render pass means composing a frame never moves the highlight.
func updateSelectionState() {
	switch menuState {
	case screenSearch, screenPairedDevices, screenDongleSettings, screenSwitchDevice, screenFirmware:
	default:
		menuState = screenStartMenu
	}
	updateStartMenu()
	syncStartMenuSelection()
	currentSelection = clampSelection(currentSelection, currentMenuLength())
}

// composeFrame paints the entire UI into the off-screen buffer and returns it.
// It only reads state, so the same state always yields the same frame.
func composeFrame() *frame {
	screen.resize(width, height)
	if width < 30 || height < 8 {
		renderSmallTerminal()
		return screen
	}

	header()
	switch menuState {
	case screenSearch:
		menuSearchForNewDevices()
	case screenPairedDevices:
		menuPairedDevices()
	case screenDongleSettings:
		renderDongleSettings()
	case screenSwitchDevice:
		renderSwitchDevice()
	case screenFirmware:
		renderFirmware()
	default:
		menu()
	}
	renderStatus()
	return screen
}

func startUi(parent context.Context) {
	ctx, stopSignals := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	keyEvents := make(chan keyEvent, 32)
	actionResults := make(chan actionResult, 8)
	go startKeysPressedListener(ctx, keyEvents)

	// Poll for slow device and terminal changes, but redraw only when state
	// changes. This keeps the TUI quiet and avoids wasting CPU.
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	lastRevision := ^uint64(0)
	lastWidth, lastHeight := -1, -1

	for {
		forceRedraw := false
		select {
		case <-ctx.Done():
			return
		case event := <-keyEvents:
			if handleKeyEvent(event, actionResults) {
				return
			}
			forceRedraw = true
		case result := <-actionResults:
			applyActionResult(result)
			forceRedraw = true
		case <-ticker.C:
			resetExpiredFlash()
			resetExpiredFactoryConfirmation()
			clearExpiredStatus()
			refreshSearchDeviceList()
			if !firstScanComplete.Load() {
				forceRedraw = true
			}
		}

		getScreenSize()
		revision := uiRevision.Load()
		if !forceRedraw && revision == lastRevision && width == lastWidth && height == lastHeight {
			continue
		}
		lastRevision = revision
		lastWidth, lastHeight = width, height

		updateSelectionState()
		flushFrame(composeFrame())
	}
}
