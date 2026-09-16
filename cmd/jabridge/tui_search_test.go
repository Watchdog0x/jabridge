package main

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

func TestSearchRefreshDoesNotBlockNavigation(t *testing.T) {
	withMenuState(t)
	oldBackend := currentTUIBackend()
	defer setTUIBackend(oldBackend)
	server, connection := net.Pipe()
	client := ipc.ClientFromConnection(connection)
	defer func() { _ = server.Close(); _ = client.Close() }()
	setTUIBackend(&tuiIPCBackend{client: client})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var request ipc.Request
		if err := json.NewDecoder(server).Decode(&request); err != nil {
			return
		}
		time.Sleep(400 * time.Millisecond)
		_ = json.NewEncoder(server).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": ipc.SearchState{State: "searching"}})
	}()
	defer func() { _ = server.Close(); _ = client.Close(); <-done }()
	menuState = screenSearch
	clearSearchResults()
	defer cancelSearchRefresh()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	previousContext := uiResultContext
	uiResultContext = ctx
	defer func() { uiResultContext = previousContext }()
	started := time.Now()
	results := make(chan actionResult, 1)
	refreshSearchDeviceList(results)
	elapsed := time.Since(started)
	t.Logf("search refresh returned in %s with a 400 ms service delay", elapsed)
	if elapsed > 150*time.Millisecond {
		t.Fatal("search polling blocked the UI event loop")
	}
	select {
	case result := <-results:
		applyActionResult(result, results)
		if searchRefreshPending || searchViewState.State != "searching" {
			t.Fatal("search result was not applied on the UI loop")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("search refresh did not complete")
	}
}

func TestSearchLayoutKeepsKeysVisibleAndCentersContent(t *testing.T) {
	withMenuState(t)
	oldList, oldState := searchDeviceList, searchViewState
	t.Cleanup(func() { searchDeviceList, searchViewState = oldList, oldState })
	searchDeviceList = &pairingList{}
	searchViewState = ipc.SearchState{State: "searching"}
	menuState = screenSearch
	for _, size := range [][2]int{{40, 16}, {60, 22}, {80, 24}, {120, 36}, {191, 51}, {240, 80}} {
		f := newRenderTarget(t, size[0], size[1])
		composeFrame()
		left, right, bottom := panelBounds()
		marginDifference := (left - 1) - (f.width - right)
		if marginDifference < -1 || marginDifference > 1 {
			t.Fatalf("panel is not centered at %dx%d", size[0], size[1])
		}
		keys, message := false, false
		for row := 1; row <= f.height; row++ {
			line := rowText(f, row)
			if strings.Contains(line, "Q Back") {
				keys = true
				if row <= bottom {
					t.Fatal("key hints overlap the content")
				}
			}
			if row < bottom && strings.Contains(line, "Searching") {
				message = true
				label := strings.TrimSpace(string([]rune(line)[left : right-1]))
				column := textColumn(t, f, row, label)
				textCenter := column + (displayWidth(label)-1)/2
				if difference := textCenter - (left+right)/2; difference < -1 || difference > 1 {
					t.Fatal("search text is not centered")
				}
				if row < bottom/2-2 || row > bottom/2+3 {
					t.Fatal("empty search content is not vertically centered")
				}
			}
		}
		if !keys || !message {
			t.Fatalf("missing search message or keys at %dx%d", size[0], size[1])
		}
	}
}

func TestSearchLateResultCannotReplaceNewSearch(t *testing.T) {
	withMenuState(t)
	oldList, oldState := searchDeviceList, searchViewState
	t.Cleanup(func() { cancelSearchRefresh(); searchDeviceList, searchViewState = oldList, oldState })
	searchDeviceList = &pairingList{}
	menuState = screenSearch
	clearSearchResults()
	old := searchRefreshGeneration
	clearSearchResults()
	applySearchLoad(&searchLoadResult{generation: old, state: ipc.SearchState{State: "completed"}, devices: []pairedDevice{{deviceName: "stale"}}})
	if searchViewState.State != "starting" || len(searchDeviceList.pairedDevices) != 0 {
		t.Fatal("stale search replaced the current view")
	}
	returnToStartMenu()
	applySearchLoad(&searchLoadResult{generation: searchRefreshGeneration - 1, state: ipc.SearchState{State: "completed"}})
	if menuState != screenStartMenu {
		t.Fatal("late search reopened the screen")
	}
}

func TestDeviceSelectionKeepsUIResponsive(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, devices{1: {deviceID: 1, instance: "original", deviceName: "Test headset", productID: 0x24b7}}, 1, -1)
	oldItems := switchDeviceItems
	t.Cleanup(func() { switchDeviceItems = oldItems })
	oldBackend := currentTUIBackend()
	defer setTUIBackend(oldBackend)
	server, connection := net.Pipe()
	client := ipc.ClientFromConnection(connection)
	setTUIBackend(&tuiIPCBackend{client: client})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var request ipc.Request
		if err := json.NewDecoder(server).Decode(&request); err != nil {
			return
		}
		time.Sleep(400 * time.Millisecond)
		_ = json.NewEncoder(server).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]bool{"ok": true}})
	}()
	defer func() { _ = server.Close(); _ = client.Close(); <-done }()
	menuState = screenSwitchDevice
	width, height = 100, 30
	refreshSwitchDeviceItems()
	currentSelection = 0
	results := make(chan actionResult, 1)
	started := time.Now()
	handleEnterKey(results)
	elapsed := time.Since(started)
	if elapsed > 150*time.Millisecond {
		t.Fatal("device selection blocked navigation", elapsed)
	}
	updateDeviceByID(1, func(device *jabra_DeviceInfo) { device.instance = "replacement" })
	select {
	case result := <-results:
		applyActionResult(result, results)
		if menuState != screenSwitchDevice {
			t.Fatal("late selection accepted a replacement device")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("selection did not complete")
	}
}
