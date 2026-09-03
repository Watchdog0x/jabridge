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
	message            string
	err                error
	refreshDongleMenu  bool
	returnToMainMenu   bool
	clearSearchResults bool
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

	currentSelection  = 0
	menuState         = 0
	startMenuSelected = -1

	selectedItemsPairedDevices       = -1
	menuItemsPairedDevices           = [5]string{"Q Back", "1 Connect", "2 Disconnect", "3 Remove", "4 Clear"}
	selectedItemsSearchForNewDevices = -1
	menuItemsSearchForNewDevices     = [2]string{"Q Back", "1 Connect"}

	statusMessage string
	statusIsError bool
	statusUntil   time.Time
	flashUntil    time.Time

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
	batteryFullChar     = "◼"
	batteryEmptyChar    = "◻"
	batteryWidth        = 10
	lowBatteryThreshold = 20

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
		defer unix.SetNonblock(fd, false)
	}

	buf := make([]byte, 16)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := os.Stdin.Read(buf)
		if n > 0 {
			for _, event := range parseKeyEvents(buf[:n]) {
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

func parseKeyEvents(input []byte) []keyEvent {
	if len(input) >= 3 && input[0] == 0x1B && input[1] == '[' {
		switch input[2] {
		case 'A':
			return []keyEvent{keyUp}
		case 'B':
			return []keyEvent{keyDown}
		}
	}

	events := make([]keyEvent, 0, len(input))
	for _, b := range input {
		switch b {
		case 'w', 'W':
			events = append(events, keyUp)
		case 's', 'S':
			events = append(events, keyDown)
		case '\r', '\n':
			events = append(events, keyEnter)
		case 'q', 'Q':
			events = append(events, keyBack)
		case '1':
			events = append(events, keyAction1)
		case '2':
			events = append(events, keyAction2)
		case '3':
			events = append(events, keyAction3)
		case '4':
			events = append(events, keyAction4)
		}
	}
	return events
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
		menuState = screenStartMenu
		startMenuSelected = -1
		currentSelection = 0
		runUIAction(results, "Pairing stopped", func() error {
			return setDongleInBTPairing(false)
		})
	case screenPairedDevices, screenDongleSettings, screenSwitchDevice, screenFirmware:
		menuState = screenStartMenu
		startMenuSelected = -1
		currentSelection = 0
	}
	return false
}

func handleEnterKey(results chan<- actionResult) bool {
	switch menuState {
	case screenStartMenu:
		if currentSelection < 0 || currentSelection >= len(startMenu) {
			currentSelection = clampSelection(currentSelection, len(startMenu))
			return false
		}
		return activateStartMenuItem(startMenu[currentSelection], results)
	case screenDongleSettings:
		setStatus("Untested setting changes are locked", false)
	case screenFirmware:
		setStatus("Press 1 to download. Firmware install is locked.", false)
	}
	return false
}

func activateStartMenuItem(item menuItem, results chan<- actionResult) bool {
	startMenuSelected = currentSelection
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
		updateDongleSettignsMenu()
	case 4:
		menuState = screenFirmware
		refreshFirmwareView(results)
	case 3:
		menuState = screenSwitchDevice
	case 5:
		return true
	default:
		menuState = screenStartMenu
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

type actionOption func(*actionResult)

func withDongleMenuRefresh() actionOption {
	return func(result *actionResult) {
		result.refreshDongleMenu = true
	}
}

func withReturnToMainMenu() actionOption {
	return func(result *actionResult) {
		result.returnToMainMenu = true
	}
}

func runUIAction(results chan<- actionResult, successMessage string, action func() error, options ...actionOption) {
	go func() {
		result := actionResult{message: successMessage}
		for _, option := range options {
			option(&result)
		}
		result.err = action()

		select {
		case results <- result:
		default:
		}
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
	if dongle, exists := deviceManager[selectedDongle]; exists {
		return dongle, true
	}
	headset, exists := deviceManager[selectedHeadset]
	return headset, exists
}

func applyActionResult(result actionResult) {
	if result.err != nil {
		setStatus(result.err.Error(), true)
		return
	} else if result.message != "" {
		setStatus(result.message, false)
	}
	if result.refreshDongleMenu {
		updateDongleSettignsMenu()
	}
	if result.returnToMainMenu {
		menuState = screenStartMenu
		startMenuSelected = -1
		currentSelection = 0
	}
	if result.clearSearchResults {
		clearSearchResults()
	}
}

func handleUpKey() {
	currentSelection = clampSelection(currentSelection-1, currentMenuLength())
}

func handleDownKey() {
	currentSelection = clampSelection(currentSelection+1, currentMenuLength())
}

func currentMenuLength() int {
	switch menuState {
	case screenStartMenu:
		return len(startMenu)
	case screenSearch:
		return len(searchDeviceList.pairedDevices)
	case screenPairedDevices:
		if dongle, exists := deviceManager[selectedDongle]; exists && dongle.pairingList != nil {
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

func moveCursor(row, col int) {
	if row < 1 {
		row = 1
	}
	if col < 1 {
		col = 1
	}
	fmt.Printf("\033[%d;%dH", row, col)
}

func clearScreen() {
	fmt.Print("\033[0;40;97m\033[2J\033[H")
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
	moveCursor(topRow, leftCol)
	fmt.Printf("\033[97m%s%s%s\033[0;40;97m", leftCornerTop, line, rightCornerTop)

	for row := topRow + 1; row < bottomRow; row++ {
		moveCursor(row, leftCol)
		fmt.Printf("\033[97m%s\033[0;40;97m", verticalLine)
		moveCursor(row, rightCol)
		fmt.Printf("\033[97m%s\033[0;40;97m", verticalLine)
	}

	moveCursor(bottomRow, leftCol)
	fmt.Printf("\033[97m%s%s%s\033[0;40;97m", leftCornerBottom, line, rightCornerBottom)
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
	moveCursor(1, left)
	fmt.Printf("\033[1;96mJabridge\033[0;40;97m  %s", buildinfo.Version)
	mode := "READ-ONLY PREVIEW"
	if experimentalDeviceWritesEnabled() {
		mode = "DEVELOPER WRITES ENABLED"
	}
	moveCursor(1, max(left, right-displayWidth(mode)-2))
	if experimentalDeviceWritesEnabled() {
		fmt.Printf("\033[1;97;41m %s \033[0;40;97m", mode)
	} else {
		fmt.Printf("\033[1;30;103m %s \033[0;40;97m", mode)
	}

	moveCursor(2, left)
	dongle, exists := deviceManager[selectedDongle]
	if !exists {
		if firstScanComplete.Load() {
			if headset, headsetExists := deviceManager[selectedHeadset]; headsetExists {
				fmt.Printf("USB: %s", trimToWidth(headset.deviceName, max(10, width/3)))
			} else {
				fmt.Print("Dongle: Not connected")
			}
		} else {
			fmt.Printf("Dongle: Scanning %s", loading[loadingIndex])
			loadingIndex = (loadingIndex + 1) % len(loading)
		}
	} else {
		fmt.Printf("Dongle: %s", trimToWidth(dongle.deviceName, max(10, width/3)))
	}

	headset, exists := deviceManager[selectedHeadset]
	if !exists {
		label := "Headset: Scanning"
		if firstScanComplete.Load() {
			label = "Headset: Not connected"
		}
		moveCursor(2, max(left, right-displayWidth(label)))
		fmt.Printf("\033[97m%s\033[0;40;97m", label)
		return
	}
	if headset.batteryStatus == nil {
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
	emptySegments := batteryWidth - filledSegments

	color := "\033[32m"
	switch {
	case headset.batteryStatus.batteryLow || levelInPercent <= lowBatteryThreshold:
		color = "\033[31m"
	case levelInPercent <= 65:
		color = "\033[33m"
	}

	batteryBar := color +
		strings.Repeat(batteryFullChar, filledSegments) +
		strings.Repeat(batteryEmptyChar, emptySegments) +
		"\033[0;40;97m"

	label := fmt.Sprintf("Headset: %s  [%s] %d%%", headset.deviceName, batteryBar, levelInPercent)
	if headset.batteryStatus.charging {
		label = fmt.Sprintf("Headset: %s  [%s] %d%% charging", headset.deviceName, batteryBar, levelInPercent)
	}
	moveCursor(2, max(left, right-displayWidth(label)))
	fmt.Printf("\033[97m%s\033[0;40;97m", label)
}

func menu(_ int) {
	drawingBox()
	currentSelection = clampSelection(currentSelection, len(startMenu))
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
		drawCenteredStyled(6, "Looking for supported devices...", "\033[1;96m")
		return
	}
	dongle, hasDongle := deviceManager[selectedDongle]
	headset, hasHeadset := deviceManager[selectedHeadset]
	switch {
	case hasDongle && hasHeadset:
		drawCenteredStyled(6, "Headset connected", "\033[1;96m")
		drawCentered(7, trimToWidth(headset.deviceName, max(12, width-16)), false)
	case hasDongle:
		drawCenteredStyled(6, "Dongle ready", "\033[1;96m")
		drawCentered(7, fmt.Sprintf("%s  •  USB 0b0e:%04x", dongle.deviceName, dongle.productID), false)
		drawCentered(8, "No headset connected. Turn it on or connect it by USB.", false)
	case hasHeadset:
		drawCenteredStyled(6, "USB headset connected", "\033[1;96m")
		drawCentered(7, trimToWidth(headset.deviceName, max(12, width-16)), false)
	default:
		drawCenteredStyled(6, "No supported device found", "\033[1;93m")
		drawCentered(8, "Connect a Jabra headset or USB dongle.", false)
	}
}

func refreshSearchDeviceList() {
	if menuState != screenSearch || time.Now().Before(nextSearchRefresh) {
		return
	}
	nextSearchRefresh = time.Now().Add(time.Second)

	dongle, exists := deviceManager[selectedDongle]
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

	dongle, exists := deviceManager[selectedDongle]
	if !exists || dongle.pairingList == nil {
		drawCentered(5, "No dongle selected", true)
		return
	}

	if len(dongle.pairingList.pairedDevices) == 0 {
		drawCentered(5, "No paired devices remembered", false)
	} else {
		currentSelection = clampSelection(currentSelection, len(dongle.pairingList.pairedDevices))
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

func dongleSettigns() {
	drawingBox()
	drawCenteredStyled(6, "Dongle settings", "\033[1;96m")

	if len(dongleSettignsMenu) == 0 {
		drawCentered(8, "No dongle connected", false)
	} else {
		for i, item := range dongleSettignsMenu {
			row := 8 + i
			_, _, panelBottom := panelBounds()
			if row >= panelBottom {
				break
			}
			drawListItem(row, 10, item.label, false)
		}
	}

	drawActionBar([]string{"Q Back", "Read-only"}, -1)
}

func renderFirmware() {
	drawingBox()
	drawCenteredStyled(6, "Firmware", "\033[1;96m")
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
	lines = append(lines, "Firmware install:    Locked until hardware test")
	for _, line := range lines {
		drawListItem(row, left+4, line, false)
		row++
	}
	drawActionBar([]string{"Q Back", "1 Download", "Install locked"}, -1)
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

func drawCentered(row int, label string, selected bool) {
	col := (width - displayWidth(label)) / 2
	drawListItem(row, col, label, selected)
}

func drawCenteredStyled(row int, label, style string) {
	col := (width - displayWidth(label)) / 2
	moveCursor(row, col)
	fmt.Printf("%s%s\033[0;40;97m", style, label)
}

func drawListItem(row, col int, label string, selected bool) {
	if row < 1 || row >= height {
		return
	}
	left, right, _ := panelBounds()
	if col < left+3 {
		col = left + 3
	}
	label = trimToWidth(label, max(1, right-col-2))
	moveCursor(row, col)
	if selected {
		fmt.Printf("\033[1;30;106m  %s  \033[0;40;97m", label)
		return
	}
	fmt.Printf("\033[97m%s\033[0;40;97m", label)
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
		moveCursor(row, col)
		if i == selected {
			fmt.Printf("\033[1;30;106m %s \033[0;40;97m", item)
		} else {
			fmt.Printf("\033[1;30;107m %s \033[0;40;97m", item)
		}
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
	moveCursor(row, col)
	if statusIsError {
		fmt.Printf("\033[1;97;41m %s \033[0;40;97m", message)
		return
	}
	fmt.Printf("\033[1;96m%s\033[0;40;97m", message)
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
	moveCursor(1, 1)
	fmt.Print("Terminal too small. Resize to at least 30x8.")
}

func resetExpiredFlash() {
	if flashUntil.IsZero() || time.Now().Before(flashUntil) {
		return
	}
	selectedItemsPairedDevices = -1
	selectedItemsSearchForNewDevices = -1
	flashUntil = time.Time{}
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func startUi() {
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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

		clearScreen()
		if width < 30 || height < 8 {
			renderSmallTerminal()
			continue
		}
		header()

		switch menuState {
		case screenSearch:
			menuSearchForNewDevices()
		case screenPairedDevices:
			menuPairedDevices()
		case screenDongleSettings:
			dongleSettigns()
		case screenSwitchDevice:
			renderSwitchDevice()
		case screenFirmware:
			renderFirmware()
		default:
			menuState = screenStartMenu
			menu(width)
		}
		renderStatus()
	}
}
