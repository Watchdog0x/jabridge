package main

import (
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

var (
	loading      = [10]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	loadingIndex = 0

	// Box Drawing
	horizontalLine = "" +
		"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" +
		"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" +
		"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" // 240 total and 3 Bytes per ━

	verticalLine      = "┃"
	leftCornerTop     = "┏"
	rightCornerTop    = "┓"
	leftCornerBottom  = "┗"
	rightCornerBottom = "┛"

	// screens size
	width, height = 0, 0

	// For Navigation
	resetCurrentSelection = false
	currentSelection      = 0
	menuState             = 0
	startMenuSelected     = -1

	// selecet
	selectedItemsPairedDevices = -1
	menuItemsPairedDevices     = [5]string{"Q Back", "1 Connect", "2 Disconnect", "3 Remove", "4 Clear"}

	selectedItemsSearchForNewDevices = -1
	menuItemsSearchForNewDevices     = [2]string{"Q Back", "1 Connect"}

	// Headset Settings state
	//
	// hsDisplay is the flat, scrollable list shown on screen. It is built
	// from deviceSettings.items once per visit and reset to nil on exit so
	// that re-entering always shows the freshest data.
	hsDisplay    []hsDisplayItem
	hsCurSel     int // cursor row in hsDisplay (skips header rows)
	hsSaveFlash  int // countdown frames to show "Saved!" banner
	hsErrStr     string
	// hsEditIdx >= 0 means we are in the value-picker sub-view for that
	// hsDisplay row; hsEditOptSel is the highlighted option inside it.
	hsEditIdx    int = -1
	hsEditOptSel int
)

const (
	batteryFullChar     = "◼"
	batteryEmptyChar    = "◻"
	batteryWidth        = 10
	lowBatteryThreshold = 20
)

// hsDisplayItem is one row in the headset-settings screen.
// Group-name headers (isHeader == true) are rendered but not selectable.
type hsDisplayItem struct {
	isHeader bool
	header   string // group name when isHeader
	settIdx  int    // index into deviceSettings.items when !isHeader
}

func enableRawMode() (*unix.Termios, error) {

	fd := int(os.Stdin.Fd())

	// Get the current terminal settings
	oldSettings, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}

	newSettings := *oldSettings
	newSettings.Lflag &^= unix.ECHO | unix.ICANON // Disable echo and canonical mode
	newSettings.Iflag &^= unix.ICRNL              // Disable carriage return/newline conversion

	// Apply the new terminal settings
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &newSettings); err != nil {
		return nil, err
	}

	return oldSettings, nil
}

func restoreTerminal(oldSettings *unix.Termios) {
	unix.IoctlSetTermios(int(os.Stdin.Fd()), unix.TCSETS, oldSettings)
}

func startKeysPressedListener() {
	buf := make([]byte, 3)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error reading input:", err)
			continue
		}

		// Handle arrow keys (escape sequences)
		if n >= 3 && buf[0] == 0x1B && buf[1] == '[' {
			switch buf[2] {
			case 'A': // Up Arrow
				handleUpKey()
			case 'B': // Down Arrow
				handleDownKey()
			}
			continue
		}

		// Handle single-byte input (e.g., 'w', 's', 'q', etc.)
		key := buf[0]
		switch menuState {
		case 0: // StartMenu
			switch key {
			case 'w': // Up
				handleUpKey()
			case 's': // Down
				handleDownKey()
			case '\r': // Enter
				startMenuSelected = currentSelection
			}
		// ############## Search For New Devices #################
		case 1:
			switch key {
			case 'q': // Back To Start Menu
				if err = setDongleInBTPairing(false); err != nil {
					fmt.Println(err) //  remember to add a error window in the ui
				}
				startMenuSelected = -1
			case '1':
				selectedItemsSearchForNewDevices = 1
				if len(searchDeviceList.pairedDevices) != 0 {
					if err := connectNewDevice(uint16(currentSelection)); err != nil {
						fmt.Println(err) //  remember to add a error window in the ui
					} else {
						startMenuSelected = -1
					}
				}
			}
		// ############## See Remembered Paired Devices #################
		case 2:
			switch key {
			case 'q': // Back To Start Menu
				startMenuSelected = -1
			case 'w': // Up
				handleUpKey()
			case 's': // Down
				handleDownKey()
			case '1':
				if err := connectDeviceFromPairedlist(uint16(currentSelection)); err != nil {
					fmt.Println(err) //  remember to add a error window in the ui
				}
				selectedItemsPairedDevices = 1
			case '2':
				if err := disconnectDeviceFromPairedlist(uint16(currentSelection)); err != nil {
					fmt.Println(err) //  remember to add a error window in the ui
				}
				selectedItemsPairedDevices = 2
			case '3':
				if err := removeDeviceFromPairedlist(uint16(currentSelection)); err != nil {
					fmt.Println(err) //  remember to add a error window in the ui
				}
				selectedItemsPairedDevices = 3
			case '4':
				if err := clearPairingList(); err != nil {
					fmt.Println(err) //  remember to add a error window in the ui
				}
				selectedItemsPairedDevices = 4
			}
		// ############# Dongle Settings ##################
		case 3:
			switch key {
			case 'q': // Back To Start Menu
				startMenuSelected = -1
			case 'w': // Up
				handleUpKey()
			case 's': // Down
				handleDownKey()
			case '\r': // Enter
				switch dongleSettignsMenu[currentSelection].id {
				case 0:
					getautoPairingState, _ := getAutoPairing()
					if err := setAutoPairing(!getautoPairingState); err != nil {
						fmt.Println(err) //  remember to add a error window in the ui
					}
					updateDongleSettignsMenu()
				case 1:
					if dongle, exists := deviceManager[selectedDongle]; exists {
						if err := factoryReset(dongle.deviceID); err != nil {
							fmt.Println(err) //  remember to add a error window in the ui
						}
						startMenuSelected = -1
					}
				}
			}
		// ############# switch  device ##################
		case 4:
			switch key {
			case 'q': // Back To Start Menu
				startMenuSelected = -1
			}
		// ############# Headset Settings ##################
		case 5:
			if hsEditIdx >= 0 && hsDisplay != nil && hsEditIdx < len(hsDisplay) {
				// ---- value-picker sub-view ----
				item := hsDisplay[hsEditIdx]
				headset, hExists := deviceManager[selectedHeadset]
				if !hExists || headset.deviceSettings == nil {
					hsEditIdx = -1
					break
				}
				s := headset.deviceSettings.items[item.settIdx]
				switch key {
				case 'w':
					if hsEditOptSel > 0 {
						hsEditOptSel--
					}
				case 's':
					if hsEditOptSel < len(s.listKeyValue)-1 {
						hsEditOptSel++
					}
				case '\r':
					chosen := s.listKeyValue[hsEditOptSel].key
					if writeErr := applySettingByte(headset.deviceID, &headset.deviceSettings, s.guid, uint8(chosen)); writeErr != nil {
						hsErrStr = writeErr.Error()
					} else {
						hsErrStr = ""
						hsSaveFlash = 24
					}
					hsDisplay = nil // rebuild display from fresh data next frame
					hsEditIdx = -1
				case 'q':
					hsEditIdx = -1
				}
			} else {
				// ---- main settings list ----
				switch key {
				case 'q':
					hsDisplay = nil
					hsCurSel = 0
					hsErrStr = ""
					hsEditIdx = -1
					startMenuSelected = -1
				case 'w':
					hsMoveUp()
				case 's':
					hsMoveDown()
				case '\r':
					hsActivate()
				}
			}
		}
	}
}

func handleUpKey() {
	if menuState == 5 {
		if hsEditIdx >= 0 {
			if hsEditOptSel > 0 {
				hsEditOptSel--
			}
		} else {
			hsMoveUp()
		}
		return
	}
	if currentSelection > 0 {
		currentSelection--
	}
}

func handleDownKey() {
	switch menuState {
	case 0: // StartMenu
		if currentSelection < len(startMenu)-1 {
			currentSelection++
		}
	case 2: // See Remembered Paired Devices
		if dongle, exists := deviceManager[selectedDongle]; exists {
			if currentSelection < len(dongle.pairingList.pairedDevices)-1 {
				currentSelection++
			}
		}
	case 3: // Dongle Settings
		if currentSelection < len(dongleSettignsMenu)-1 {
			currentSelection++
		}
	case 5: // Headset Settings
		if hsEditIdx >= 0 && hsDisplay != nil && hsEditIdx < len(hsDisplay) {
			if headset, ok := deviceManager[selectedHeadset]; ok && headset.deviceSettings != nil {
				s := headset.deviceSettings.items[hsDisplay[hsEditIdx].settIdx]
				if hsEditOptSel < len(s.listKeyValue)-1 {
					hsEditOptSel++
				}
			}
		} else {
			hsMoveDown()
		}
	}
}

func moveCursor(row, col int) {
	fmt.Printf("\033[%d;%dH", row, col) // ANSI escape to move to row and column
}

func clearScreen() {
	fmt.Print("\033[2J") // Clear the screen
	fmt.Print("\033[H")  // Move the cursor to the top-left corner
}

func getScreenSize() {
	getWidth, getHeight, err := term.GetSize(1)
	if err != nil {
		log.Fatalln(err)
	}
	width, height = getWidth, getHeight
}

func drawingBox() {

	calcHeight := height - 4
	calcWidth := (width - 11) * 3
	if calcWidth > len(horizontalLine) {
		return
	}

	// Using horizontalLine[:(width-11)*3] is faster than using strings.Repeat,
	// as it directly slices the precomputed string to the required length.
	// The factor `3` accounts for each Unicode characte taking 3 byte
	moveCursor(3, 5)
	fmt.Printf("%s%s%s", leftCornerTop, horizontalLine[:calcWidth], rightCornerTop)

	for i := 4; i < calcHeight; i++ {
		moveCursor(i, 5)
		fmt.Print(verticalLine)
		moveCursor(i, width-5)
		fmt.Print(verticalLine)
		moveCursor(i, 6)
	}

	moveCursor(calcHeight, 5)
	fmt.Printf("%s%s%s", leftCornerBottom, horizontalLine[:(width-11)*3], rightCornerBottom)

}

func header() {
	moveCursor(2, 5)
	dongle, exists := deviceManager[selectedDongle]
	if !exists {
		fmt.Printf("Looking For Dongle %s", loading[loadingIndex])
		loadingIndex = (loadingIndex + 1) % len(loading)
		return
	}
	fmt.Printf("%s", dongle.deviceName)

	headset, exists := deviceManager[selectedHeadset]
	if !exists {
		moveCursor(2, width-25)
		fmt.Printf("Looking For HeadSet %s", loading[loadingIndex])
		loadingIndex = (loadingIndex + 1) % len(loading)
		return
	}
	if headset.batteryStatus == nil {
		return
	}

	levelInPercent := headset.batteryStatus.levelInPercent
	filledSegments := int(math.Round(float64(levelInPercent) / 100 * batteryWidth))
	emptySegments := batteryWidth - filledSegments
	var color string
	switch {
	case headset.batteryStatus.batteryLow:
		color = "\033[31m" // Red for low battery
	case levelInPercent <= 65:
		color = "\033[33m" // Yellow for medium battery
	default:
		color = "\033[32m" // Green for high battery
	}

	batteryBar := color +
		strings.Repeat(batteryFullChar, filledSegments) +
		strings.Repeat(batteryEmptyChar, emptySegments) +
		"\033[0m" // Reset color

	if headset.batteryStatus.charging {
		moveCursor(2, width-50)
		fmt.Printf("%s - Battery : [%s]🗲 %d%%", headset.deviceName, batteryBar, levelInPercent)
	} else {
		moveCursor(2, width-48)
		fmt.Printf("%s - Battery: [%s] %d%%", headset.deviceName, batteryBar, levelInPercent)
	}
}

func menu(width int) {
	resetCurrentSelection = false // we can make a map to rember what was the last currentSelection
	drawingBox()
	for i, option := range startMenu {
		mid := (width - len(option.label)) / 2

		if i == currentSelection {
			moveCursor(5+i, mid-1)
			fmt.Println("\033[42m", option.label, "\033[0m")
		} else {
			moveCursor(5+i, mid)
			fmt.Println(option.label)
		}
	}
}

func updateSearchDeviceList() {
	for {
		if menuState != 1 {
			return
		}
		if dongle, exists := deviceManager[selectedDongle]; exists {
			updateSearchDeviceLis := getSearchDeviceList(dongle.deviceID)
			if updateSearchDeviceLis != nil {
				searchDeviceList.count = updateSearchDeviceLis.count
				searchDeviceList.listType = updateSearchDeviceLis.listType
				searchDeviceList.pairedDevices = updateSearchDeviceLis.pairedDevices
			}
		}
		time.Sleep(time.Second)
	}
}

func menuSearchForNewDevices() {
	if !resetCurrentSelection {
		currentSelection = 0
		resetCurrentSelection = true
		if err := searchForNewDevices(); err != nil {
			fmt.Println(err)
		}
		go updateSearchDeviceList()
	}

	drawingBox()

	if len(searchDeviceList.pairedDevices) != 0 {
		for i, pairedDevice := range searchDeviceList.pairedDevices {
			moveCursor(4+i, 10)
			device := fmt.Sprintf("%d %s", i+1, pairedDevice.deviceName)
			if i == currentSelection {
				fmt.Println("\033[42m", device, "\033[0m")
			} else {
				fmt.Println(device)
			}
		}
	}

	calcWidth := 0
	for i, item := range menuItemsSearchForNewDevices {
		moveCursor(height-3, 7+calcWidth)

		if i == selectedItemsSearchForNewDevices {
			fmt.Println("\033[44m", item, "\033[0m")
			go func() { // selected animation
				time.Sleep(time.Millisecond * 200)
				selectedItemsSearchForNewDevices = -1
			}()
		} else {
			fmt.Println("\033[42m", item, "\033[0m")
		}
		calcWidth += len(item) + 3 // Add the item's width plus a space for separation
	}

}

func menuPairedDevices() {
	if !resetCurrentSelection {
		currentSelection = 0
		resetCurrentSelection = true
	}

	drawingBox()

	if dongle, exists := deviceManager[selectedDongle]; exists {
		for i, pairedDevice := range dongle.pairingList.pairedDevices {
			moveCursor(4+i, 10)
			device := fmt.Sprintf("%d %s", i+1, pairedDevice.deviceName)
			if pairedDevice.isConnected {
				device += " (Connected)"
			}
			if i == currentSelection {
				fmt.Println("\033[42m", device, "\033[0m")
			} else {
				fmt.Println(device)
			}
		}

		calcWidth := 0
		for i, item := range menuItemsPairedDevices {
			moveCursor(height-3, 7+calcWidth)

			if i == selectedItemsPairedDevices {
				fmt.Println("\033[44m", item, "\033[0m")
				go func() { // selected animation
					time.Sleep(time.Millisecond * 200)
					selectedItemsPairedDevices = -1
				}()
			} else {
				fmt.Println("\033[42m", item, "\033[0m")
			}
			calcWidth += len(item) + 3 // Add the item's width plus a space for separation
		}

	} else {
		startMenuSelected = -1
	}
}

func dongleSettigns() {
	if !resetCurrentSelection {
		currentSelection = 0
		resetCurrentSelection = true
	}

	drawingBox()

	for i, item := range dongleSettignsMenu {

		if i == currentSelection {
			moveCursor(4+i, 9)
			fmt.Println("\033[42m", item.label, "\033[0m")
		} else {
			moveCursor(4+i, 10)
			fmt.Println(item.label)
		}
	}

	moveCursor(height-3, 7)
	fmt.Println("\033[42m", "Q Back", "\033[0m")
}

func startUi() {
	sigChan := make(chan os.Signal, 1)
	go func() {
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	}()

	for {
		select {
		case <-sigChan:
			return
		default:
			clearScreen()
			getScreenSize()
			header()

			if startMenuSelected != -1 {
				switch startMenu[startMenuSelected].id {
				case 0: // Search For New Devices
					menuState = 1
					menuSearchForNewDevices()
				case 1: // See Remembered Paired Devices
					menuState = 2
					menuPairedDevices()
				case 2: // Dongle Settings
					menuState = 3
					dongleSettigns()
				case 3: // Switch Device
					moveCursor(4, 5)
					fmt.Println("Switch Device")
					menuState = 4
				case 4: // HeadSet Settings
					menuState = 5
					headsetSettingsScreen()
				case 5: // Exit
					return
				}
			} else {
				menuState = 0
				menu(width)
			}

			time.Sleep(time.Second / 12) // 12 Fps
		}
	}
}

// hsBuildDisplay constructs (or rebuilds) hsDisplay from the headset's
// cached deviceSettings. Items are grouped by groupName; group-name headers
// are interleaved as non-selectable rows. Non-interactive types (labels,
// rulers, plain buttons) are omitted entirely.
func hsBuildDisplay() {
	hsDisplay = nil
	headset, ok := deviceManager[selectedHeadset]
	if !ok || headset.deviceSettings == nil {
		return
	}

	var lastGroup string
	for i, s := range headset.deviceSettings.items {
		switch s.cntrlType {
		case cntrlLabel, cntrlHorzRuler, cntrlButton, cntrlEditButton:
			continue
		}
		if s.groupName != "" && s.groupName != lastGroup {
			hsDisplay = append(hsDisplay, hsDisplayItem{isHeader: true, header: s.groupName})
			lastGroup = s.groupName
		}
		hsDisplay = append(hsDisplay, hsDisplayItem{settIdx: i})
	}
}

// hsMoveUp moves the cursor up, skipping group-header rows.
func hsMoveUp() {
	for pos := hsCurSel - 1; pos >= 0; pos-- {
		if !hsDisplay[pos].isHeader {
			hsCurSel = pos
			return
		}
	}
}

// hsMoveDown moves the cursor down, skipping group-header rows.
func hsMoveDown() {
	for pos := hsCurSel + 1; pos < len(hsDisplay); pos++ {
		if !hsDisplay[pos].isHeader {
			hsCurSel = pos
			return
		}
	}
}

// hsActivate handles Enter on the currently highlighted setting.
func hsActivate() {
	if hsCurSel < 0 || hsCurSel >= len(hsDisplay) {
		return
	}
	item := hsDisplay[hsCurSel]
	if item.isHeader {
		return
	}

	headset, ok := deviceManager[selectedHeadset]
	if !ok || headset.deviceSettings == nil {
		return
	}
	s := headset.deviceSettings.items[item.settIdx]

	if s.isProtected && s.isProtectionEnabled {
		return // greyed out — skip silently
	}

	switch s.cntrlType {
	case cntrlToggle:
		// Pick the "other" valid key. Some Jabra toggles don't use {0, 1};
		// the device-side keys come from the manifest (e.g. {0, 2}).
		var newVal uint8
		if len(s.listKeyValue) >= 2 {
			found := false
			for _, o := range s.listKeyValue {
				if uint16(s.currByte) != o.key {
					newVal = uint8(o.key)
					found = true
					break
				}
			}
			if !found {
				newVal = uint8(s.listKeyValue[0].key)
			}
		} else {
			newVal = 1
			if s.currByte != 0 {
				newVal = 0
			}
		}
		if writeErr := applySettingByte(headset.deviceID, &headset.deviceSettings, s.guid, newVal); writeErr != nil {
			hsErrStr = writeErr.Error()
		} else {
			hsErrStr = ""
			hsSaveFlash = 24
		}
		hsDisplay = nil // rebuild on next frame

	case cntrlRadio, cntrlDrpDown, cntrlComboBox:
		if len(s.listKeyValue) == 0 {
			return
		}
		hsEditIdx = hsCurSel
		hsEditOptSel = 0
		for i, o := range s.listKeyValue {
			if o.key == uint16(s.currByte) {
				hsEditOptSel = i
				break
			}
		}
	}
}

// headsetSettingsScreen is the top-level render call for menuState == 5.
func headsetSettingsScreen() {
	// Build the display list on first visit or after a write.
	if hsDisplay == nil {
		hsBuildDisplay()
		// Move cursor to first selectable row.
		hsCurSel = 0
		for hsCurSel < len(hsDisplay) && hsDisplay[hsCurSel].isHeader {
			hsCurSel++
		}
		hsEditIdx = -1
	}

	drawingBox()

	headset, hExists := deviceManager[selectedHeadset]
	if !hExists || headset.deviceSettings == nil {
		moveCursor(5, 8)
		fmt.Print("\033[31mNo headset connected or settings unavailable.\033[0m")
		moveCursor(height-3, 7)
		fmt.Print("\033[42m Q Back \033[0m")
		return
	}

	if hsErrStr != "" {
		moveCursor(height-5, 8)
		maxW := width - 16
		msg := hsErrStr
		if maxW > 0 && len(msg) > maxW {
			msg = msg[:maxW] + "…"
		}
		fmt.Printf("\033[31mError: %s\033[0m", msg)
	}
	if hsSaveFlash > 0 {
		hsSaveFlash--
		moveCursor(height-5, 8)
		fmt.Print("\033[42m Saved! \033[0m")
	}

	if hsEditIdx >= 0 && hsEditIdx < len(hsDisplay) {
		// ---- value-picker sub-view ----
		editItem := hsDisplay[hsEditIdx]
		s := headset.deviceSettings.items[editItem.settIdx]

		moveCursor(4, 8)
		fmt.Printf("Setting: \033[1m%s\033[0m", s.name)
		if s.helpText != "" && s.helpText != s.name {
			moveCursor(5, 8)
			help := s.helpText
			maxW := width - 16
			if maxW > 0 && len(help) > maxW {
				help = help[:maxW] + "…"
			}
			fmt.Printf("\033[2m%s\033[0m", help)
		}

		for i, opt := range s.listKeyValue {
			moveCursor(7+i, 10)
			marker := "  "
			if opt.key == uint16(s.currByte) {
				marker = "✓ "
			}
			line := fmt.Sprintf("%s%s", marker, opt.value)
			if i == hsEditOptSel {
				fmt.Printf("\033[42m %s \033[0m", line)
			} else {
				fmt.Printf(" %s ", line)
			}
		}

		moveCursor(height-3, 7)
		fmt.Print("\033[42m Q Back \033[0m  \033[42m Enter Select \033[0m")
		return
	}

	// ---- main settings list ----
	innerRows := height - 7
	if innerRows < 1 {
		innerRows = 1
	}

	scrollOffset := 0
	if hsCurSel >= innerRows {
		scrollOffset = hsCurSel - innerRows + 1
	}

	nameMax := width - 32
	if nameMax < 10 {
		nameMax = 10
	}

	for i, item := range hsDisplay {
		visRow := i - scrollOffset
		if visRow < 0 || visRow >= innerRows {
			continue
		}
		moveCursor(4+visRow, 8)

		if item.isHeader {
			// Group header: bold, dimmed, not selectable.
			grp := item.header
			if len(grp) > nameMax+20 {
				grp = grp[:nameMax+19] + "…"
			}
			fmt.Printf(" \033[1;2m── %s ──\033[0m", grp)
			continue
		}

		s := headset.deviceSettings.items[item.settIdx]
		name := s.name
		if len(name) > nameMax {
			name = name[:nameMax-1] + "…"
		}

		suffix := ""
		if s.isDeviceRestart {
			suffix += " (reboot required)"
		}
		valLabel := settingValueLabel(s)

		var line string
		if s.isProtected && s.isProtectionEnabled {
			line = fmt.Sprintf("\033[2m%-*s  %s 🔒%s\033[0m", nameMax, name, valLabel, suffix)
		} else {
			line = fmt.Sprintf("%-*s  %s%s", nameMax, name, valLabel, suffix)
		}

		if i == hsCurSel {
			fmt.Printf("\033[42m %s \033[0m", line)
		} else {
			fmt.Printf(" %s ", line)
		}
	}

	moveCursor(height-3, 7)
	fmt.Print("\033[42m Q Back \033[0m  \033[42m Enter Edit \033[0m  \033[42m W/S Navigate \033[0m")
}
