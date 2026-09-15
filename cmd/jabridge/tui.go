package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/firmware"
	"github.com/Watchdog0x/jabridge/internal/history"
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
	keyEscape
	keyBackspace
	keyClearText
)

const keyRuneBase keyEvent = 0x110000
const keyPasteBase keyEvent = 0x220000

type actionResult struct {
	installPlan            *firmware.PreparedInstall
	activityID             uint64
	message                string
	err                    error
	returnToMainMenu       bool
	clearSearchResults     bool
	refreshDongleSettings  bool
	refreshHeadsetSettings bool
	settingsLoad           *settingsLoadResult
	firmwareRequest        uint64
}

type settingsLoadResult struct {
	generation uint64
	scope      settingScope
	lines      []menuItem
	values     []deviceSettingValue
}

var (
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
	startMenuSelectionID    = -1
	switchDeviceSelectionID = -1
	switchDeviceItems       = []switchDeviceItem{}
	firmwareTargetID        = -1
	firmwareTargetIndex     = 0
	firmwareTargetItems     = []switchDeviceItem{}

	// screen is the off-screen buffer every render pass paints into.
	screen = newFrame(0, 0)

	menuItemsSearchForNewDevices = [3]string{"Q Back", "↑/↓ Select", "Enter Connect"}

	statusMu           sync.RWMutex
	statusMessage      string
	statusIsError      bool
	statusUntil        time.Time
	statusKind         string
	resetConfirmUntil  time.Time
	forgetConfirmUntil time.Time
	forgetConfirmKey   string

	nextSearchRefresh         time.Time
	uiRevision                atomic.Uint64
	firmwareViewMu            sync.RWMutex
	firmwareView              firmwareViewState
	dongleSettingsLoading     bool
	headsetSettingsLoading    bool
	headsetSettingsScope      = settingScopeHeadset
	headsetSettingsGeneration uint64
)

type firmwareViewState struct {
	request           uint64
	targetKey         string
	targetPID         uint16
	targetRegistryID  int
	loading           bool
	deviceName        string
	currentVersion    string
	latestVersion     string
	downloadedPath    string
	downloadedVersion string
	currentError      string
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
	screenHeadsetSettings
	screenSwitchDevice
	screenFirmware
	screenSound
	screenSoundDetail
	screenSoundVolume
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
	} else {
		return // never leave an uninterruptible stdin reader during handoff
	}

	buf := make([]byte, 16)
	decoder := &keyDecoder{rawText: true}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := unix.Read(fd, buf)
		if n > 0 {
			for _, event := range decoder.feed(buf[:n]) {
				select {
				case keyEvents <- event:
				case <-ctx.Done():
					return
				}
			}
			continue
		}

		if err != nil {
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
				if event := decoder.flushEscape(time.Now()); event != keyNone {
					select {
					case keyEvents <- event:
					case <-ctx.Done():
						return
					}
				}
				time.Sleep(20 * time.Millisecond)
				continue
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

type keyDecoder struct {
	pending     []byte
	rawText     bool
	pasting     bool
	escapeSince time.Time
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
				if d.escapeSince.IsZero() {
					d.escapeSince = time.Now()
				}
				break
			}
			d.escapeSince = time.Time{}
			if d.pending[1] != '[' {
				d.pending = d.pending[2:]
				continue
			}
			end := 2
			for end < len(d.pending) && (d.pending[end] < 0x40 || d.pending[end] > 0x7e) {
				end++
			}
			if end == len(d.pending) {
				if len(d.pending) > 64 {
					d.pending = nil
				}
				break
			}
			parameters := string(d.pending[2:end])
			switch d.pending[end] {
			case 'A':
				events = append(events, keyUp)
			case 'B':
				events = append(events, keyDown)
			case '~':
				if parameters == "200" {
					d.pasting = true
				}
				if parameters == "201" {
					d.pasting = false
				}
			case 'u':
				fields := strings.Split(parameters, ";")
				code, parseErr := strconv.Atoi(strings.Split(fields[0], ":")[0])
				released := len(fields) > 1 && strings.HasSuffix(fields[1], ":3")
				if parseErr == nil && !released && code >= 0 && code <= utf8.MaxRune {
					if event := d.runeEvent(rune(code)); event != keyNone {
						events = append(events, event)
					}
				}
			}
			d.pending = d.pending[end+1:]
			continue
		}
		if !utf8.FullRune(d.pending) {
			break
		}
		r, size := utf8.DecodeRune(d.pending)
		if r == utf8.RuneError && size == 1 {
			d.pending = d.pending[1:]
			continue
		}
		if event := d.runeEvent(r); event != keyNone {
			events = append(events, event)
		}
		d.pending = d.pending[size:]
	}
	return events
}

func (d *keyDecoder) runeEvent(r rune) keyEvent {
	if d.rawText && r >= 32 && r != 127 {
		if d.pasting {
			return keyPasteBase + keyEvent(r)
		}
		return keyRuneBase + keyEvent(r)
	}
	if d.pasting {
		return keyNone
	}
	if r == 8 || r == 127 {
		return keyBackspace
	}
	if r == 21 {
		return keyClearText
	}
	if r == 27 {
		return keyEscape
	}
	if r < 128 {
		return basicKeyEvent(byte(r))
	}
	return keyNone
}

func (d *keyDecoder) flushEscape(now time.Time) keyEvent {
	if len(d.pending) == 1 && d.pending[0] == 27 && !d.escapeSince.IsZero() && now.Sub(d.escapeSince) >= 60*time.Millisecond {
		d.pending, d.escapeSince = nil, time.Time{}
		return keyEscape
	}
	return keyNone
}

func navigationKey(event keyEvent) keyEvent {
	if event >= keyPasteBase {
		return keyNone
	}
	if event >= keyRuneBase {
		r := event - keyRuneBase
		if r < 128 {
			return basicKeyEvent(byte(r))
		}
		return keyNone
	}
	if event == keyEscape {
		return keyBack
	}
	return event
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
	if width < 40 || height < 16 {
		return navigationKey(event) == keyBack
	}
	if menuState == screenSoundDetail && soundMusicConfirm {
		switch navigationKey(event) {
		case keyBack:
			soundMusicConfirm = false
		case keyEnter:
			soundModeAction(results, "music")
		}
		return false
	}
	if menuState == screenSoundVolume {
		handleSoundVolumeKey(event, results)
		return false
	}
	if activeSettingEditor != nil {
		handleSettingEditorKey(event, results)
		return false
	}

	if handleFirmwareKey(event, results) {
		return false
	}
	if uiBusy() && (navigationKey(event) == keyEnter || navigationKey(event) >= keyAction1 && navigationKey(event) <= keyAction4) {
		return false
	}
	event = navigationKey(event)
	if event == keyNone {
		return false
	}
	before := menuState
	entry := tuiHistoryEvent("key")
	entry.Input = map[keyEvent]string{keyUp: "up", keyDown: "down", keyEnter: "enter", keyBack: "back", keyAction1: "action-1", keyAction2: "action-2", keyAction3: "action-3", keyAction4: "action-4"}[event]
	entry.Phase = "observed"
	// Navigation is sampled; action keys are always retained.
	if event != keyUp && event != keyDown || time.Since(lastHistoryNavigation) > 250*time.Millisecond {
		history.Record(entry)
		lastHistoryNavigation = time.Now()
	}
	defer func() {
		if menuState != before {
			next := tuiHistoryEvent("screen")
			next.Phase = "observed"
			history.Record(next)
		}
	}()
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
	case screenSoundDetail, screenSoundVolume:
		menuState = screenSound
		currentSelection = 0
		syncSoundSelection()
		return false
	case screenSound:
		returnToStartMenu()
		return false
	case screenStartMenu:
		return true
	case screenSearch:
		returnToStartMenu()
		runUIAction(results, "Search stopped", func() error {
			if currentTUIBackend() != nil {
				return runIPCAction("bt.search.stop", nil)
			}
			return stopNativeSearch()
		})
	case screenPairedDevices, screenDongleSettings, screenHeadsetSettings, screenSwitchDevice, screenFirmware:
		resetConfirmUntil = time.Time{}
		forgetConfirmUntil = time.Time{}
		forgetConfirmKey = ""
		switchDeviceSelectionID = -1
		returnToStartMenu()
	}
	return false
}

func handleEnterKey(results chan<- actionResult) bool {
	switch menuState {
	case screenSound, screenSoundDetail:
		handleSoundEnter(results)
		return false
	case screenStartMenu:
		if currentSelection < 0 || currentSelection >= len(startMenu) {
			currentSelection = clampSelection(currentSelection, len(startMenu))
			rememberStartMenuSelection()
			return false
		}
		return activateStartMenuItem(startMenu[currentSelection], results)
	case screenSearch:
		if len(searchDeviceList.pairedDevices) == 0 && searchViewState.State != "searching" && searchViewState.State != "starting" {
			return activateStartMenuItem(menuItem{id: 0}, results)
		}
		handleActionKey(keyAction1, results)
	case screenPairedDevices:
		handleRememberedDeviceAction(keyAction1, results)
	case screenDongleSettings:
		toggleSelectedSetting(settingScopeDongle, results)
	case screenHeadsetSettings:
		toggleSelectedSetting(headsetSettingsScope, results)
	case screenSwitchDevice:
		if currentSelection < 0 || currentSelection >= len(switchDeviceItems) {
			setStatus("No connected device selected", true)
			return false
		}
		registryID := switchDeviceItems[currentSelection].RegistryID
		if currentTUIBackend() != nil {
			if err := runIPCAction("device.select", map[string]uint16{"id": uint16(registryID)}); err != nil {
				setStatus(err.Error(), true)
				return false
			}
			name, _, _, err := selectRegistryDeviceState(registryID)
			if err != nil {
				setStatus(err.Error(), true)
				return false
			}
			setStatus("Now using "+name, false)
			returnToStartMenu()
			return false
		}
		name, err := selectRegistryDevice(registryID)
		if err != nil {
			setStatus(err.Error(), true)
			return false
		}
		setStatus("Now using "+name, false)
		returnToStartMenu()
	case screenFirmware:
		startFirmwareUpdate(results)
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
		runUIAction(results, "Device search started", func() error {
			if currentTUIBackend() != nil {
				return runIPCAction("bt.search", nil)
			}
			return searchForNewDevices()
		})
	case 1:
		menuState = screenPairedDevices
	case 2:
		menuState = screenDongleSettings
		startSettingsLoad(results, settingScopeDongle)
	case 6:
		menuState = screenHeadsetSettings
		headsetSettingsScope = settingScopeHeadset
		headsetSettingsLoading = false
		headsetSettingsLines, headsetSettingsValues = nil, nil
		startSettingsLoad(results, settingScopeHeadset)
	case 7:
		menuState = screenHeadsetSettings
		headsetSettingsScope = settingScopeController
		headsetSettingsLoading = false
		headsetSettingsLines, headsetSettingsValues = nil, nil
		startSettingsLoad(results, settingScopeController)
	case 8:
		menuState = screenSound
		currentSelection = 0
		soundSelectionToken = ""
		syncSoundSelection()
	case 4:
		menuState = screenFirmware
		refreshFirmwareTargets()
		refreshFirmwareView(results)
	case 3:
		menuState = screenSwitchDevice
		refreshSwitchDeviceItems()
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
		if len(searchDeviceList.pairedDevices) == 0 {
			setStatus("No searched device selected", true)
			return
		}
		selection := currentSelection
		session := searchViewState.Session
		setStatus("Connecting searched device...", false)
		runUIAction(results, "Headset connection confirmed", func() error {
			if currentTUIBackend() != nil {
				return runIPCAction("bt.search.connect", map[string]any{"index": selection, "session": session})
			}
			return connectNewDevice(uint16(selection))
		}, withReturnToMainMenu())
	case screenPairedDevices:
		handleRememberedDeviceAction(event, results)
	case screenDongleSettings:
		handleDongleSettingsAction(event, results)
	case screenHeadsetSettings:
		if event == keyAction1 {
			toggleSelectedSetting(headsetSettingsScope, results)
		}
	case screenFirmware:
		selectFirmwareTargetNumber(firmwareNumber(event), results)
	}
}

func handleRememberedDeviceAction(event keyEvent, results chan<- actionResult) {
	device, err := rememberedDeviceAt(currentSelection)
	if err != nil {
		setStatus(err.Error(), true)
		return
	}
	switch event {
	case keyAction1:
		forgetConfirmUntil = time.Time{}
		forgetConfirmKey = ""
		selection := currentSelection
		if device.isConnected {
			setStatus("Disconnecting "+device.deviceName+"...", false)
			runUIAction(results, device.deviceName+" disconnected", func() error {
				if currentTUIBackend() != nil {
					return runIPCAction("bt.disconnect", map[string]int{"index": selection})
				}
				return disconnectRememberedDevice(selection)
			})
			return
		}
		setStatus("Connecting "+device.deviceName+"...", false)
		runUIAction(results, device.deviceName+" connected", func() error {
			if currentTUIBackend() != nil {
				return runIPCAction("bt.connect", map[string]int{"index": selection})
			}
			return connectRememberedDevice(selection)
		})
	case keyAction2:
		confirmationKey := fmt.Sprintf("%d:%s", currentSelection, device.deviceName)
		if !confirmForgetRememberedDevice(time.Now(), confirmationKey) {
			setStatus("WARNING: press 2 again within 10 seconds to forget "+device.deviceName, true)
			return
		}
		selection := currentSelection
		setStatus("Forgetting "+device.deviceName+"...", false)
		runUIAction(results, device.deviceName+" removed from remembered devices", func() error {
			if currentTUIBackend() != nil {
				return runIPCAction("bt.forget", map[string]int{"index": selection})
			}
			return forgetRememberedDevice(selection)
		})
	}
}

func confirmForgetRememberedDevice(now time.Time, key string) bool {
	if key != "" && key == forgetConfirmKey &&
		!forgetConfirmUntil.IsZero() && !now.After(forgetConfirmUntil) {
		forgetConfirmUntil = time.Time{}
		forgetConfirmKey = ""
		return true
	}
	forgetConfirmKey = key
	forgetConfirmUntil = now.Add(factoryResetConfirmWindow)
	requestUIRedraw()
	return false
}

func handleDongleSettingsAction(event keyEvent, results chan<- actionResult) {
	dongle, exists := selectedDongleSnapshot()
	if !exists {
		setStatus("No dongle connected", true)
		return
	}
	switch event {
	case keyAction1:
		resetConfirmUntil = time.Time{}
		toggleSelectedSetting(settingScopeDongle, results)
	case keyAction2:
		if !supportsExperimentalDongleWrites(dongle.productID) {
			setStatus(fmt.Sprintf("Factory reset is not supported for dongle PID 0x%04x", dongle.productID), true)
			return
		}
		if !confirmFactoryReset(time.Now()) {
			setStatus("WARNING: reset erases remembered headsets; press 2 again within 10 seconds", true)
			return
		}
		deviceID := dongle.deviceID
		setStatus("Sending factory-reset command...", false)
		runUIAction(results, "Factory-reset command accepted; waiting for dongle reconnect", func() error {
			if currentTUIBackend() != nil {
				return runIPCAction("device.reset", map[string]string{"confirm": "ERASE_REMEMBERED_HEADSETS"})
			}
			return factoryResetConfirmed(deviceID)
		})
	}
}

func selectedDeviceSetting(scope settingScope) (deviceSettingValue, bool) {
	values := dongleSettingsValues
	if scope != settingScopeDongle {
		values = headsetSettingsValues
	}
	if currentSelection < 0 || currentSelection >= len(values) {
		return deviceSettingValue{}, false
	}
	return values[currentSelection], true
}

func selectedSettingsDevice(scope settingScope) (*jabra_DeviceInfo, bool) {
	if scope == settingScopeDongle {
		return selectedDongleSnapshot()
	}
	if scope == settingScopeController {
		device, exists := selectedHeadsetSnapshot()
		return device, exists && hasController(device)
	}
	return selectedHeadsetSnapshot()
}

func toggleSelectedSetting(scope settingScope, results chan<- actionResult) {
	setting, exists := selectedDeviceSetting(scope)
	if !exists {
		setStatus("No supported setting selected", true)
		return
	}
	if !setting.editable() {
		if setting.needsConfigMode() {
			setStatus("This setting needs configuration mode and is read-only for now", true)
		} else {
			setStatus("This setting is read-only", true)
		}
		return
	}
	device, exists := selectedSettingsDevice(scope)
	if !exists {
		setStatus("Device disconnected", true)
		return
	}
	if settingIsText(setting) || len(settingChoices(setting)) > 2 || setting.needsConfigMode() {
		if err := openSettingEditor(scope, setting, device); err != nil {
			setStatus(err.Error(), true)
		}
		return
	}
	wanted, err := setting.nextValueName()
	if err != nil {
		setStatus(err.Error(), true)
		return
	}
	setStatus(fmt.Sprintf("Changing %s to %s...", setting.label(), wanted), false)
	runUIAction(results, fmt.Sprintf("%s is now %s", setting.label(), wanted), func() error {
		if setting.Remote != nil {
			return setIPCSetting(setting, wanted)
		}
		return writeNextDeviceSetting(device, setting)
	}, withSettingsRefresh(scope))
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

func withSettingsRefresh(scope settingScope) actionOption {
	if scope == settingScopeDongle {
		return withDongleSettingsRefresh()
	}
	return func(result *actionResult) {
		result.refreshHeadsetSettings = true
	}
}

func startSettingsLoad(results chan<- actionResult, scope settingScope) {
	var generation uint64
	if scope == settingScopeDongle {
		if dongleSettingsLoading {
			return
		}
		dongleSettingsLoading = true
	} else {
		if headsetSettingsLoading {
			return
		}
		headsetSettingsLoading = true
		headsetSettingsGeneration++
		generation = headsetSettingsGeneration
	}
	requestUIRedraw()
	activityID := beginUIActivity("Reading device settings")
	entry := tuiHistoryEvent("load-settings")
	finish := history.Begin(entry)
	ctx := currentUIResultContext()
	go func() {
		defer history.CapturePanic(entry)
		var (
			lines  []menuItem
			values []deviceSettingValue
			err    error
		)
		if scope == settingScopeDongle {
			lines, values, err = loadDongleSettings()
		} else {
			lines, values, err = loadHeadsetSettingsScope(scope)
		}
		finish(err)
		sendUIResult(ctx, results, actionResult{
			activityID: activityID,
			err:        err,
			settingsLoad: &settingsLoadResult{
				scope: scope, lines: lines, values: values, generation: generation,
			},
		})
	}()
}

func runUIAction(results chan<- actionResult, successMessage string, action func() error, options ...actionOption) {
	result := actionResult{message: successMessage}
	for _, option := range options {
		option(&result)
	}
	// Metadata refreshes can overlap when the user selects another target.
	// Device changes must not be queued by repeated Enter presses.
	if result.firmwareRequest == 0 {
		if uiBusy() {
			return
		}
		statusMu.RLock()
		label := statusMessage
		statusMu.RUnlock()
		if label == "" {
			label = "Waiting for device"
		}
		result.activityID = beginUIActivity(label)
	}

	entry := tuiHistoryEvent("action")
	finish := history.Begin(entry)
	ctx := currentUIResultContext()
	go func() {
		defer history.CapturePanic(entry)

		result.err = action()

		finish(result.err)

		sendUIResult(ctx, results, result)
	}()
}

func refreshFirmwareView(results chan<- actionResult) {
	refreshFirmwareTargets()
	target, exists := selectedFirmwareTarget()
	if !exists {
		firmwareViewMu.Lock()
		firmwareView = firmwareViewState{}
		firmwareViewMu.Unlock()
		setStatus("No supported device connected", true)
		return
	}
	device := *target.Device
	request, key := beginFirmwareView(target)
	requestUIRedraw()

	runUIAction(results, "Firmware information ready", func() error {
		current, currentErr := firmwareVersionForTUI(&device)
		latest, latestErr := latestFirmwareForTUI(&device)

		if !applyFirmwareInfo(request, key, current, latest.Version, currentErr) {
			return nil
		}
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
	}, withFirmwareRequest(request))
}

func refreshFirmwareTargets() {
	items := switchableDevices()
	firmwareTargetItems = items
	if len(items) == 0 {
		firmwareTargetID = -1
		firmwareTargetIndex = 0
		return
	}
	for index, item := range items {
		if item.RegistryID == firmwareTargetID {
			firmwareTargetIndex = index
			return
		}
	}
	firmwareTargetIndex = clampSelection(firmwareTargetIndex, len(items))
	firmwareTargetID = items[firmwareTargetIndex].RegistryID
}

func selectedFirmwareTarget() (switchDeviceItem, bool) {
	if firmwareTargetIndex < 0 || firmwareTargetIndex >= len(firmwareTargetItems) {
		return switchDeviceItem{}, false
	}
	return firmwareTargetItems[firmwareTargetIndex], true
}

func advanceFirmwareTarget() bool {
	if len(firmwareTargetItems) <= 1 {
		return false
	}
	firmwareTargetIndex = (firmwareTargetIndex + 1) % len(firmwareTargetItems)
	firmwareTargetID = firmwareTargetItems[firmwareTargetIndex].RegistryID
	return true
}

func applyActionResult(result actionResult, results chan<- actionResult) {
	endUIActivity(result.activityID)
	if result.firmwareRequest != 0 {
		firmwareViewMu.RLock()
		matches := firmwareView.request == result.firmwareRequest
		firmwareViewMu.RUnlock()
		if !matches {
			return
		}
	}
	if result.installPlan != nil && result.err == nil && menuState == screenFirmware {
		target, exists := selectedFirmwareTarget()
		firmwareViewMu.RLock()
		key := firmwareView.targetKey
		firmwareViewMu.RUnlock()
		if !exists || firmwareTargetBinding(target) != key {
			setStatus("Device changed; select it again before updating", true)
			return
		}
		pendingFirmwareInstall = result.installPlan
		return
	}
	if result.settingsLoad != nil {
		load := result.settingsLoad
		if load.scope != settingScopeDongle && (load.scope != headsetSettingsScope || load.generation != headsetSettingsGeneration) {
			return
		}
		if load.scope == settingScopeDongle {
			dongleSettingsLoading = false
			if result.err == nil {
				dongleSettingsLines = load.lines
				dongleSettingsValues = load.values
			}
		} else {
			headsetSettingsLoading = false
			if result.err == nil {
				headsetSettingsLines = load.lines
				headsetSettingsValues = load.values
			}
		}
		currentSelection = clampSelection(currentSelection, currentMenuLength())
		requestUIRedraw()
	}
	if result.err != nil {
		kind := "Could not finish"
		if result.refreshHeadsetSettings || result.refreshDongleSettings {
			kind = "Could not save"
		}
		if result.settingsLoad != nil {
			kind = "Could not read"
		}
		setResultStatus(result.err.Error(), true, kind)
		return
	} else if result.message != "" {
		kind := "Done"
		if result.refreshHeadsetSettings || result.refreshDongleSettings {
			kind = "Saved"
		}
		setResultStatus(result.message, false, kind)
	}
	if result.returnToMainMenu {
		returnToStartMenu()
	}
	if result.clearSearchResults {
		clearSearchResults()
	}
	if result.refreshDongleSettings && menuState == screenDongleSettings {
		startSettingsLoad(results, settingScopeDongle)
	}
	if result.refreshHeadsetSettings && menuState == screenHeadsetSettings {
		startSettingsLoad(results, headsetSettingsScope)
	}
}

func handleUpKey() {
	currentSelection = clampSelection(currentSelection-1, currentMenuLength())
	rememberStartMenuSelection()
	rememberSwitchDeviceSelection()
	rememberSoundSelection()
}

func handleDownKey() {
	currentSelection = clampSelection(currentSelection+1, currentMenuLength())
	rememberStartMenuSelection()
	rememberSwitchDeviceSelection()
	rememberSoundSelection()
}

func rememberSwitchDeviceSelection() {
	if menuState != screenSwitchDevice || currentSelection < 0 || currentSelection >= len(switchDeviceItems) {
		return
	}
	switchDeviceSelectionID = switchDeviceItems[currentSelection].RegistryID
}

func refreshSwitchDeviceItems() {
	if switchDeviceSelectionID < 0 && currentSelection >= 0 && currentSelection < len(switchDeviceItems) {
		switchDeviceSelectionID = switchDeviceItems[currentSelection].RegistryID
	}
	switchDeviceItems = switchableDevices()
	if len(switchDeviceItems) == 0 {
		currentSelection = 0
		switchDeviceSelectionID = -1
		return
	}
	for index, item := range switchDeviceItems {
		if item.RegistryID == switchDeviceSelectionID {
			currentSelection = index
			return
		}
	}
	currentSelection = clampSelection(currentSelection, len(switchDeviceItems))
	switchDeviceSelectionID = switchDeviceItems[currentSelection].RegistryID
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
	case screenSound:
		return len(currentSoundView().Nodes)
	case screenSoundDetail:
		if node, ok := soundTargetNode(soundDetailTarget); ok {
			return len(soundDetailItems(node))
		}
		return 0
	case screenStartMenu:
		return len(startMenu)
	case screenSearch:
		return len(searchDeviceList.pairedDevices)
	case screenPairedDevices:
		if dongle, exists := selectedDongleSnapshot(); exists && dongle.pairingList != nil {
			return len(dongle.pairingList.pairedDevices)
		}
	case screenDongleSettings:
		return len(dongleSettingsValues)
	case screenHeadsetSettings:
		return len(headsetSettingsValues)
	case screenSwitchDevice:
		return len(switchDeviceItems)
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
	styleBase     = styleHomeBase
	styleText     = styleHomeBase
	styleBorder   = styleHomeBorder
	styleTitle    = styleHomeTitle
	styleWarn     = styleHomeWarn
	styleSelected = styleHomeSelect
	styleAction   = styleHomeCard
	styleAlert    = "1;38;2;255;245;245;48;2;143;37;52"
	styleBattOK   = styleHomeTitle
	styleBattWarn = styleHomeWarn
	styleBattLow  = "1;38;2;255;135;150;48;2;14;22;32"
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
	baseStyle                                string
	width                                    int
	height                                   int
	cells                                    []cell
	clip                                     bool
	clipLeft, clipRight, clipTop, clipBottom int
}

func newFrame(width, height int) *frame {
	f := &frame{}
	f.resize(width, height)
	return f
}

// resize prepares the buffer for a frame of the given size and blanks it.
func (f *frame) resize(width, height int) {
	if f.baseStyle == "" {
		f.baseStyle = styleBase
	}
	f.clip = false
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
		f.cells[i] = cell{ch: ' ', style: f.baseStyle}
	}
}

// setText paints text with its first character at the 1-based (row, col)
// position. Anything outside the frame is clipped. text must be plain text:
// styling is carried by style, never by escapes embedded in the string.
func (f *frame) setText(row, col int, text, style string) {
	if row < 1 || row > f.height {
		return
	}
	if f.clip && (row < f.clipTop || row > f.clipBottom) {
		return
	}
	if style == "" {
		style = f.baseStyle
	}
	base := (row - 1) * f.width
	for _, r := range text {
		if col > f.width {
			return
		}
		if col >= 1 && (!f.clip || col >= f.clipLeft && col <= f.clipRight) {
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
		for end > 0 && line[end-1].ch == ' ' && line[end-1].style == f.baseStyle {
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
		if style != f.baseStyle {
			b.WriteString("\033[" + f.baseStyle + "m")
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
	restore := screen.withoutClip()
	defer restore()
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
	bottom = height - 6
	if height < 14 {
		bottom = height - 4
	}
	return left, right, bottom
}

func menu() {
	drawingHomeBox()
	renderHomeSummary()

	if len(startMenu) == 0 {
		drawCentered(10, "No menu items available", false)
		return
	}

	startRow := 11
	if height >= 28 {
		startRow = homeSummaryRow() + 5
	}
	if height < 18 {
		startRow = 8
	}
	_, _, panelBottom := panelBounds()
	visible := max(1, panelBottom-startRow)
	start, end := listWindow(currentSelection, len(startMenu), visible)
	drawListWindowHint(startRow-1, start, end, len(startMenu))
	for i := start; i < end; i++ {
		option := startMenu[i]
		row := startRow + i - start
		if row >= panelBottom {
			break
		}
		left, right, _ := panelBounds()
		menuWidth := min(48, right-left-6)
		label := trimToWidth(option.label, menuWidth-6)
		style := styleText
		if i == currentSelection {
			style = styleSelected
		}
		col := left + (right-left-menuWidth)/2
		screen.setText(row, col, strings.Repeat(" ", menuWidth), style)
		screen.setText(row, col+3, label, style)
		if i == currentSelection {
			screen.setText(row, col, "›", style)
			screen.setText(row, col+menuWidth-2, "→", style)
		}
	}
	drawActionBar([]string{"↑/↓ Move", "Enter Open", "Q Quit"}, -1)
}

func renderHomeSummary() {
	row := homeSummaryRow()
	if !firstScanComplete.Load() {
		drawCenteredStyled(row, "Looking for supported devices...", styleTitle)
		return
	}
	dongle, hasDongle := selectedDongleSnapshot()
	headset, hasHeadset := selectedHeadsetSnapshot()
	switch {
	case !hasDongle && !hasHeadset && len(currentSoundView().Nodes) > 0:
		drawCenteredStyled(row, "Jabra audio detected", styleTitle)
		drawHomeDetail(row+2, "Open Sound. Headset settings need USB or Jabra Link.", false)
	case hasHeadset && controllerWithoutHeadset(headset):
		drawCenteredStyled(row, "Link Call Control detected", styleTitle)
		drawHomeDetail(row+1, "Headset not detected yet.", false)
		drawHomeDetail(row+2, "Connect the headset for its settings.", false)
	case hasDongle && hasHeadset:
		drawCenteredStyled(row, "Headset connected", styleTitle)
		drawHomeDetail(row+1, trimToWidth(headset.deviceName, max(12, width-16)), false)
	case hasDongle:
		drawCenteredStyled(row, "Dongle ready", styleTitle)
		drawHomeDetail(row+1, fmt.Sprintf("%s  •  USB 0b0e:%04x", dongle.deviceName, dongle.productID), false)
		drawHomeDetail(row+2, "No headset connected. Turn it on or connect it by USB.", false)
	case hasHeadset:
		drawCenteredStyled(row, "USB headset connected", styleTitle)
		drawHomeDetail(row+1, trimToWidth(headset.deviceName, max(12, width-16)), false)
	default:
		drawCenteredStyled(row, "No supported device found", styleWarn)
		drawHomeDetail(row+2, "Connect a Jabra headset or USB dongle.", false)
	}
}

func refreshSearchDeviceList() {
	if menuState != screenSearch || time.Now().Before(nextSearchRefresh) {
		return
	}
	nextSearchRefresh = time.Now().Add(time.Second)

	var update *pairingList
	if currentTUIBackend() != nil {
		var state ipc.SearchState
		if err := tuiIPCCall("bt.search.status", nil, &state); err != nil {
			searchViewState = ipc.SearchState{State: "failed", Error: err.Error()}
			return
		}
		searchViewState = state
		update = &pairingList{count: uint16(len(state.Devices)), listType: searchResult}
		for _, result := range state.Devices {
			update.pairedDevices = append(update.pairedDevices, pairedDevice{deviceName: result.Name, isConnected: result.Connected})
		}
	} else {
		dongle, exists := selectedDongleSnapshot()
		if !exists {
			return
		}
		update = getSearchDeviceList(dongle.deviceID)
	}
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
	searchViewState = ipc.SearchState{State: "starting"}
	searchDeviceList.count = 0
	searchDeviceList.listType = searchResult
	searchDeviceList.pairedDevices = nil
	nextSearchRefresh = time.Time{}
}

func menuSearchForNewDevices() {
	drawingBox()
	drawListHeading("Find headset", currentSelection, len(searchDeviceList.pairedDevices))
	if len(searchDeviceList.pairedDevices) == 0 {
		if searchViewState.State == "starting" || searchViewState.State == "searching" {
			drawCentered(8, "Searching for headsets...", false)
			drawCentered(10, "Put your headset in pairing mode.", false)
		} else if searchViewState.State == "timed_out" {
			drawCentered(8, "Search timed out", false)
			drawCentered(10, "No completion reply received. Try again.", false)
		} else if searchViewState.Error != "" {
			drawCentered(8, "Search could not finish", false)
			drawCentered(10, searchViewState.Error, false)
		} else {
			drawCentered(8, "No headsets found", false)
			drawCentered(10, "Put your headset in pairing mode and try again.", false)
		}
	} else {
		drawPairingRows(searchDeviceList.pairedDevices)
	}

	if len(searchDeviceList.pairedDevices) == 0 {
		right := []string{}
		if searchViewState.State != "starting" && searchViewState.State != "searching" {
			right = append(right, "Enter Search again")
		}
		drawSplitActionBar([]string{hintBack}, right)
	} else {
		drawSplitActionBar(menuItemsSearchForNewDevices[:1], menuItemsSearchForNewDevices[1:])
	}
}

func menuPairedDevices() {
	drawingBox()

	dongle, exists := selectedDongleSnapshot()
	if !exists || dongle.pairingList == nil {
		drawCentered(8, "No dongle selected", false)
		drawSplitActionBar([]string{hintBack}, nil)
		return
	}
	drawListHeading("Remembered headsets", currentSelection, len(dongle.pairingList.pairedDevices))

	if len(dongle.pairingList.pairedDevices) == 0 {
		drawCentered(8, "No paired devices remembered", false)
	} else {
		drawPairingRows(dongle.pairingList.pairedDevices)
	}

	primaryAction := "Enter Connect"
	if device, err := rememberedDeviceAt(currentSelection); err == nil && device.isConnected {
		primaryAction = "Enter Disconnect"
	}
	actions := []string{"Q Back", "↑/↓ Select", primaryAction, "2 Forget"}
	if !forgetConfirmUntil.IsZero() && time.Now().Before(forgetConfirmUntil) {
		actions = []string{"Q Cancel", "2 CONFIRM forget"}
	}
	drawSplitActionBar(actions[:1], actions[1:])
}

func renderDongleSettings() {
	drawingBox()
	drawListHeading("Dongle settings", currentSelection, len(dongleSettingsValues))
	if dongleSettingsLoading && len(dongleSettingsLines) == 0 {
		drawCentered(9, "Loading settings...", false)
		drawSplitActionBar([]string{"Q Back"}, nil)
		return
	}
	lastRow := renderDeviceSettings(dongleSettingsLines, dongleSettingsValues)

	left, _, panelBottom := panelBounds()
	if lastRow < panelBottom {
		factoryLabel := "Factory reset: not supported on this dongle"
		if dongle, exists := selectedDongleSnapshot(); exists && supportsExperimentalDongleWrites(dongle.productID) {
			factoryLabel = "Factory reset: press 2 twice (erases remembered headsets)"
		}
		drawListItem(lastRow, left+4, factoryLabel, false)
	}

	actions := settingsActionBar(dongleSettingsValues)
	if dongle, exists := selectedDongleSnapshot(); exists && supportsExperimentalDongleWrites(dongle.productID) {
		actions = append(actions, "2 Factory reset")
		if !resetConfirmUntil.IsZero() && time.Now().Before(resetConfirmUntil) {
			actions = []string{"Q Cancel", "2 CONFIRM factory reset"}
		}
	}
	drawSplitActionBar(actions[:1], actions[1:])
}

func renderHeadsetSettings() {
	drawingBox()
	title := "Headset settings"
	if headsetSettingsScope == settingScopeController {
		title = "Link Call Control settings"
	}
	drawListHeading(title, currentSelection, len(headsetSettingsValues))
	if headsetSettingsLoading && len(headsetSettingsLines) == 0 {
		drawCentered(9, "Loading settings...", false)
		drawSplitActionBar([]string{"Q Back"}, nil)
		return
	}
	renderDeviceSettings(headsetSettingsLines, headsetSettingsValues)
	actions := settingsActionBar(headsetSettingsValues)
	drawSplitActionBar(actions[:1], actions[1:])
}

func renderDeviceSettings(lines []menuItem, values []deviceSettingValue) int {
	left, _, panelBottom := panelBounds()
	row := 8
	if height < 22 && len(lines) > 0 {
		lines = lines[:1]
		row = 7
	}
	if len(lines) == 0 {
		drawCentered(row, "No device connected", false)
		return row + 1
	}
	for _, item := range lines {
		if row >= panelBottom {
			return row
		}
		drawInfoRow(row, item.label)
		row++
	}
	if len(values) == 0 {
		if row < panelBottom {
			drawListItem(row+1, left+4, "No supported settings found", false)
		}
		return row + 2
	}
	row++
	listBottom := panelBottom
	for _, setting := range values {
		if setting.help() != "" && panelBottom-row >= 3 {
			listBottom--
			break
		}
	}
	visibleRows := listBottom - row
	start, end := listWindow(currentSelection, len(values), visibleRows)
	drawListWindowHint(row-1, start, end, len(values))
	for i := start; i < end; i++ {
		setting := values[i]
		if row >= listBottom {
			break
		}
		drawSettingRow(row, setting, i == currentSelection)
		row++
	}
	if listBottom < panelBottom {
		if selected, ok := selectedDeviceSettingForValues(values); ok && selected.help() != "" {
			drawListItem(listBottom, left+4, selected.help(), false)
		}
	}
	return row
}

func settingsActionBar(values []deviceSettingValue) []string {
	actions := []string{hintBack}
	if len(values) == 0 {
		return append(actions, "No settings")
	}
	actions = append(actions, hintMove)
	if setting, exists := selectedDeviceSettingForValues(values); exists && setting.editable() {
		if settingIsText(setting) || len(settingChoices(setting)) > 2 || setting.needsConfigMode() {
			return append(actions, hintEdit)
		}
		return append(actions, hintChange)
	}
	return append(actions, "Read only")
}

func selectedDeviceSettingForValues(values []deviceSettingValue) (deviceSettingValue, bool) {
	if currentSelection < 0 || currentSelection >= len(values) {
		return deviceSettingValue{}, false
	}
	return values[currentSelection], true
}

func renderFirmware() {
	drawingBox()
	drawCenteredStyled(6, "Firmware", styleTitle)
	target, exists := selectedFirmwareTarget()
	if !exists {
		drawCentered(9, "No firmware target connected", false)
		drawSplitActionBar([]string{"Q Back"}, nil)
		return
	}
	firmwareViewMu.RLock()
	view := firmwareView
	firmwareViewMu.RUnlock()
	row := drawFirmwareTargets()
	actions := []string{"1-9 Device", "↑/↓ Select", "Enter Update"}
	if len(firmwareTargetItems) >= 10 {
		actions[0] = "1-9 / 0 Device"
	}

	if view.loading || view.targetKey != firmwareTargetBinding(target) {
		drawCentered(row+1, loadingGlyph()+" Checking device and latest release...", false)
		drawSplitActionBar([]string{"Q Back"}, actions[:2])
		return
	}
	_, _, bottom := panelBounds()
	installed := valueOrUnknown(view.currentVersion)
	if view.currentVersion == "" && strings.Contains(strings.ToLower(view.currentError), "permission denied") {
		installed = "Setup needed: run jabridge setup"
	}
	lines := []string{
		infoLine("Target", fmt.Sprintf("%d of %d", firmwareTargetIndex+1, max(1, len(firmwareTargetItems)))),
		infoLine("Device", valueOrUnknown(view.deviceName)),
		infoLine("USB ID", fmt.Sprintf("0b0e:%04x", view.targetPID)),
		infoLine("Installed", installed),
		infoLine("Latest available", valueOrUnknown(view.latestVersion)),
	}
	if view.currentVersion != "" && view.latestVersion != "" {
		state := "Update available"
		if view.currentVersion == view.latestVersion {
			state = "Up to date"
		}
		lines = append(lines, infoLine("Status", state))
	}
	if view.downloadedPath != "" {
		lines = append(lines, infoLine("Downloaded", view.downloadedPath))
	}
	lines = append(lines, firmwareInstallStatus(view))
	for _, line := range lines {
		if row >= bottom {
			break
		}
		drawInfoRow(row, line)
		row++
	}
	if view.currentVersion != "" && view.currentVersion == view.latestVersion {
		actions[2] = "Up to date"
	}
	if uiBusy() {
		drawSplitActionBar(nil, []string{"Preparing update..."})
		return
	}
	drawSplitActionBar([]string{"Q Back"}, actions)
}

func firmwareInstallStatus(view firmwareViewState) string {

	switch {
	case view.currentVersion != "" && view.currentVersion == view.latestVersion:
		return infoLine("Install", "Not needed (already up to date)")
	case view.downloadedPath == "":
		return infoLine("Install", "Enter: download, check, then confirm")
	default:
		return infoLine("Install/retry", "Enter: open installer confirmation")
	}
}

func valueOrUnknown(value string) string {
	if value == "" {
		return "Unknown"
	}
	return value
}

func renderSwitchDevice() {
	drawingBox()
	drawListHeading("Switch device", currentSelection, len(switchDeviceItems))
	if len(switchDeviceItems) == 0 {
		drawCentered(9, "No connected devices", false)
		drawActionBar([]string{"Q Back"}, -1)
		return
	}
	left, _, panelBottom := panelBounds()
	row := 9
	visibleRows := panelBottom - row
	start, end := listWindow(currentSelection, len(switchDeviceItems), visibleRows)
	drawListWindowHint(row-1, start, end, len(switchDeviceItems))
	for index := start; index < end && row < panelBottom; index++ {
		drawListItem(row, left+4, switchDeviceLabel(switchDeviceItems[index]), index == currentSelection)
		row++
	}
	drawActionBar([]string{"Q Back", "↑/↓ Select", "Enter Use"}, -1)
}

// selectionPadding is how far the selected-row highlight extends on each side
// of the label. It is painted around the label rather than inserted before it,
// so selecting a row never moves its text.
const selectionPadding = 2

func drawCentered(row int, label string, selected bool) {
	left, right, _ := panelBounds()
	label = trimToWidth(label, max(1, right-left-8))
	drawListItem(row, labelColumnFor(label), label, selected)
}

func drawCenteredStyled(row int, label, style string) {
	left, right, _ := panelBounds()
	label = trimToWidth(label, max(1, right-left-4))
	screen.setText(row, labelColumnFor(label), label, style)
}

// labelColumnFor returns the column a centred label starts at. It depends only
// on the label, never on whether the row is selected.
func labelColumnFor(label string) int {
	left, right, _ := panelBounds()
	return (left + right - displayWidth(label) + 1) / 2
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
	restore := screen.withoutClip()
	defer restore()
	if height < 8 {
		return
	}
	left, right, bottom := panelBounds()
	row := bottom + 4
	if height < 14 {
		row = bottom + 2
	}
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

func drawSplitActionBar(leftItems, rightItems []string) {
	restore := screen.withoutClip()
	defer restore()
	if height < 8 {
		return
	}
	left, right, bottom := panelBounds()
	row := bottom + 4
	if height < 14 {
		row = bottom + 2
	}
	if row >= height {
		row = height - 1
	}
	leftWidth := actionItemsWidth(leftItems)
	rightWidth := actionItemsWidth(rightItems)
	leftCol := left + 2
	rightCol := right - 2 - rightWidth
	if rightCol <= leftCol+leftWidth+1 {
		items := append(append([]string(nil), leftItems...), rightItems...)
		drawActionBar(compactActionHints(items, right-left-4), -1)
		return
	}
	drawActionItems(row, leftCol, leftItems)
	drawActionItems(row, rightCol, rightItems)
}

func actionItemsWidth(items []string) int {
	width := 0
	for index, item := range items {
		if index > 0 {
			width += 2
		}
		width += displayWidth(item) + 2
	}
	return width
}

func drawActionItems(row, col int, items []string) {
	for index, item := range items {
		if index > 0 {
			col += 2
		}
		screen.setText(row, col, " "+item+" ", styleAction)
		col += displayWidth(item) + 2
	}
}

func renderStatus() {
	renderActivityStatus()
}

func setStatus(message string, isError bool) {
	if isError {
		history.Record(history.Event{Component: "tui", Action: "message", Phase: "error", Error: history.Classify(errors.New(message))})
	}
	statusMu.Lock()
	statusMessage = message
	statusIsError = isError
	statusKind = "Info"
	statusUntil = time.Now().Add(7 * time.Second)
	if isError {
		statusUntil = time.Now().Add(30 * time.Second)
	}
	statusMu.Unlock()
	requestUIRedraw()
}

func requestUIRedraw() {
	uiRevision.Add(1)
}

func renderSmallTerminal() {
	screen.setText(1, 1, "Resize to at least 40x16.", styleText)
	screen.setText(3, 1, "Q Quit", styleAction)
}

func resetExpiredFactoryConfirmation() {
	if resetConfirmUntil.IsZero() || time.Now().Before(resetConfirmUntil) {
		return
	}
	resetConfirmUntil = time.Time{}
	requestUIRedraw()
}

func resetExpiredForgetConfirmation() {
	if forgetConfirmUntil.IsZero() || time.Now().Before(forgetConfirmUntil) {
		return
	}
	forgetConfirmUntil = time.Time{}
	forgetConfirmKey = ""
	requestUIRedraw()
}

func clearExpiredStatus() {
	statusMu.Lock()
	defer statusMu.Unlock()
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
	case screenSearch, screenPairedDevices, screenDongleSettings, screenHeadsetSettings, screenSwitchDevice, screenFirmware, screenSound, screenSoundDetail, screenSoundVolume:
	default:
		menuState = screenStartMenu
	}
	updateStartMenu()
	syncSoundSelection()
	if menuState == screenSwitchDevice {
		refreshSwitchDeviceItems()
	}
	if menuState == screenFirmware {
		refreshFirmwareTargets()
	}
	syncStartMenuSelection()
	currentSelection = clampSelection(currentSelection, currentMenuLength())
}

// composeFrame paints the entire UI into the off-screen buffer and returns it.
// It only reads state, so the same state always yields the same frame.
func composeFrame() *frame {
	screen.baseStyle = styleBase
	screen.resize(width, height)
	if width < 40 || height < 16 {
		renderSmallTerminal()
		return screen
	}

	header()
	left, right, bottom := panelBounds()
	screen.clip = true
	screen.clipLeft = left + 1
	screen.clipRight = right - 1
	screen.clipTop = 5
	screen.clipBottom = bottom - 1
	if activeSettingEditor != nil {
		renderSettingEditor()
		screen.clip = false
		renderStatus()
		return screen
	}
	switch menuState {
	case screenSound:
		renderSound()
	case screenSoundDetail, screenSoundVolume:
		renderSoundDetail()
	case screenSearch:
		menuSearchForNewDevices()
	case screenPairedDevices:
		menuPairedDevices()
	case screenDongleSettings:
		renderDongleSettings()
	case screenHeadsetSettings:
		renderHeadsetSettings()
	case screenSwitchDevice:
		renderSwitchDevice()
	case screenFirmware:
		renderFirmware()
	default:
		menu()
	}
	screen.clip = false
	renderStatus()
	return screen
}

func startUi(parent context.Context) {
	ctx, stopSignals := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	uiResultContext = ctx
	defer func() {
		activityMu.Lock()
		activities = map[uint64]uiActivity{}
		activityMu.Unlock()

	}()
	fmt.Print("\x1b[?2004h")
	defer fmt.Print("\x1b[?2004l")
	defer func() { activeSettingEditor = nil }()

	keyEvents := make(chan keyEvent, 32)
	actionResults := make(chan actionResult, 8)
	keysDone := make(chan struct{})
	go func() { defer close(keysDone); startKeysPressedListener(ctx, keyEvents) }()
	defer func() { stopSignals(); <-keysDone }()

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
			applyActionResult(result, actionResults)
			if pendingFirmwareInstall != nil {
				return
			}
			forceRedraw = true
		case <-ticker.C:
			animationFrame++

			firmwareViewMu.RLock()
			firmwareLoading := firmwareView.loading
			firmwareViewMu.RUnlock()
			if uiBusy() || menuState == screenFirmware && firmwareLoading {
				forceRedraw = true
			}
			ensureFirmwareView(actionResults)
			resetExpiredFactoryConfirmation()
			resetExpiredForgetConfirmation()
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
