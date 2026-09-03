package main

import "testing"

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
