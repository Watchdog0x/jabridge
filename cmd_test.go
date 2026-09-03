package main

import "testing"

func TestHandleUpKey(t *testing.T) {
	origSelection := currentSelection
	origMenuState := menuState
	origHsEditIdx := hsEditIdx
	defer func() {
		currentSelection = origSelection
		menuState = origMenuState
		hsEditIdx = origHsEditIdx
	}()

	t.Run("decrements from 3 to 2", func(t *testing.T) {
		menuState = 0
		hsEditIdx = -1
		currentSelection = 3
		handleUpKey()
		if currentSelection != 2 {
			t.Errorf("expected currentSelection=2, got %d", currentSelection)
		}
	})

	t.Run("stays 0 at boundary", func(t *testing.T) {
		menuState = 0
		hsEditIdx = -1
		currentSelection = 0
		handleUpKey()
		if currentSelection != 0 {
			t.Errorf("expected currentSelection=0, got %d", currentSelection)
		}
	})
}

func TestHandleDownKey(t *testing.T) {
	origSelection := currentSelection
	origMenuState := menuState
	origStartMenu := startMenu
	origHsEditIdx := hsEditIdx
	defer func() {
		currentSelection = origSelection
		menuState = origMenuState
		startMenu = origStartMenu
		hsEditIdx = origHsEditIdx
	}()

	t.Run("menuState 0 increments from 0 to 1", func(t *testing.T) {
		menuState = 0
		hsEditIdx = -1
		startMenu = []menuItem{
			{id: 0, label: "A"},
			{id: 1, label: "B"},
			{id: 2, label: "C"},
			{id: 3, label: "D"},
		}
		currentSelection = 0
		handleDownKey()
		if currentSelection != 1 {
			t.Errorf("expected currentSelection=1, got %d", currentSelection)
		}
	})

	t.Run("menuState 0 stays at last item", func(t *testing.T) {
		menuState = 0
		hsEditIdx = -1
		startMenu = []menuItem{
			{id: 0, label: "A"},
			{id: 1, label: "B"},
			{id: 2, label: "C"},
			{id: 3, label: "D"},
		}
		currentSelection = 3
		handleDownKey()
		if currentSelection != 3 {
			t.Errorf("expected currentSelection=3, got %d", currentSelection)
		}
	})
}

func TestClearLine(t *testing.T) {
	t.Run("row 0 returns without action", func(t *testing.T) {
		// clearLine guards on row < 1, so row=0 should return immediately.
		// Just verify it does not panic.
		clearLine(0)
	})

	t.Run("row 5 does not panic", func(t *testing.T) {
		clearLine(5)
	})
}

func TestHsBuildDisplay(t *testing.T) {
	origDeviceManager := deviceManager
	origSelectedHeadset := selectedHeadset
	origHsDisplay := hsDisplay
	defer func() {
		deviceManager = origDeviceManager
		selectedHeadset = origSelectedHeadset
		hsDisplay = origHsDisplay
	}()

	t.Run("selectedHeadset -1 leaves hsDisplay nil", func(t *testing.T) {
		deviceManager = devices{}
		selectedHeadset = -1
		hsDisplay = nil
		hsBuildDisplay()
		if hsDisplay != nil {
			t.Errorf("expected hsDisplay=nil, got %v", hsDisplay)
		}
	})

	t.Run("headset not in deviceManager leaves hsDisplay nil", func(t *testing.T) {
		deviceManager = devices{}
		selectedHeadset = 99
		hsDisplay = nil
		hsBuildDisplay()
		if hsDisplay != nil {
			t.Errorf("expected hsDisplay=nil, got %v", hsDisplay)
		}
	})

	t.Run("headset with nil deviceSettings leaves hsDisplay nil", func(t *testing.T) {
		deviceManager = devices{
			0: {deviceID: 1, isDongle: false, deviceSettings: nil},
		}
		selectedHeadset = 0
		hsDisplay = nil
		hsBuildDisplay()
		if hsDisplay != nil {
			t.Errorf("expected hsDisplay=nil, got %v", hsDisplay)
		}
	})

	t.Run("headset with settings populates hsDisplay", func(t *testing.T) {
		ds := &deviceSettings{
			items: []settingInfo{
				{groupName: "Audio", cntrlType: cntrlComboBox, name: "Sidetone"},
				{groupName: "Audio", cntrlType: cntrlToggle, name: "MuteReminder"},
				{groupName: "Audio", cntrlType: cntrlLabel, name: "FirmwareLabel"},   // should be skipped
				{groupName: "General", cntrlType: cntrlDrpDown, name: "ActiveNoise"},
				{groupName: "General", cntrlType: cntrlHorzRuler, name: "Ruler"},     // should be skipped
				{groupName: "General", cntrlType: cntrlButton, name: "ResetButton"},  // should be skipped
				{groupName: "General", cntrlType: cntrlEditButton, name: "EditBtn"},  // should be skipped
				{groupName: "General", cntrlType: cntrlRadio, name: "ANCLevel"},
			},
		}
		deviceManager = devices{
			0: {deviceID: 10, isDongle: false, deviceSettings: ds},
		}
		selectedHeadset = 0
		hsDisplay = nil
		hsBuildDisplay()

		if hsDisplay == nil {
			t.Fatal("expected hsDisplay to be populated, got nil")
		}

		// Expected layout:
		// [0] header "Audio"
		// [1] settIdx=0 (Sidetone)
		// [2] settIdx=1 (MuteReminder)
		// [3] header "General"
		// [4] settIdx=3 (ActiveNoise)
		// [5] settIdx=7 (ANCLevel)
		expectedLen := 6
		if len(hsDisplay) != expectedLen {
			t.Fatalf("expected hsDisplay length=%d, got %d", expectedLen, len(hsDisplay))
		}

		// Check headers
		if !hsDisplay[0].isHeader || hsDisplay[0].header != "Audio" {
			t.Errorf("hsDisplay[0] should be header 'Audio', got %+v", hsDisplay[0])
		}
		if !hsDisplay[3].isHeader || hsDisplay[3].header != "General" {
			t.Errorf("hsDisplay[3] should be header 'General', got %+v", hsDisplay[3])
		}

		// Check non-header items reference correct settIdx
		if hsDisplay[1].isHeader || hsDisplay[1].settIdx != 0 {
			t.Errorf("hsDisplay[1] should be settIdx=0, got %+v", hsDisplay[1])
		}
		if hsDisplay[2].isHeader || hsDisplay[2].settIdx != 1 {
			t.Errorf("hsDisplay[2] should be settIdx=1, got %+v", hsDisplay[2])
		}
		if hsDisplay[4].isHeader || hsDisplay[4].settIdx != 3 {
			t.Errorf("hsDisplay[4] should be settIdx=3, got %+v", hsDisplay[4])
		}
		if hsDisplay[5].isHeader || hsDisplay[5].settIdx != 7 {
			t.Errorf("hsDisplay[5] should be settIdx=7, got %+v", hsDisplay[5])
		}
	})
}
