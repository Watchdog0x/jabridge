package main

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/btsearch"
)

func TestBackQueuesSearchStopWhileStartIsBusy(t *testing.T) {
	withMenuState(t)
	oldBackend := currentTUIBackend()
	defer setTUIBackend(oldBackend)
	oldTail, oldGeneration := searchCommandDone, searchCommandGeneration
	defer func() { searchCommandDone, searchCommandGeneration = oldTail, oldGeneration }()
	searchCommandDone = nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldContext := uiResultContext
	uiResultContext = ctx
	defer func() { uiResultContext = oldContext }()
	server, connection := net.Pipe()
	client := ipc.ClientFromConnection(connection)
	defer func() { _ = server.Close(); _ = client.Close() }()
	setTUIBackend(&tuiIPCBackend{client: client})
	started := make(chan struct{})
	release := make(chan struct{})
	stopped := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		decoder := json.NewDecoder(server)
		encoder := json.NewEncoder(server)
		for _, method := range []string{"bt.search", "bt.search.stop"} {
			var request ipc.Request
			if err := decoder.Decode(&request); err != nil {
				return
			}
			if request.Method != method {
				t.Errorf("got %s, want %s", request.Method, method)
				return
			}
			if method == "bt.search" {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return
				}
			} else {
				close(stopped)
			}
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]bool{"ok": true}}); err != nil {
				return
			}
		}
	}()
	results := make(chan actionResult, 4)
	activateStartMenuItem(menuItem{id: 0}, results)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("search did not start")
	}
	if !uiBusy() {
		t.Fatal("test did not hold the startup activity")
	}
	before := time.Now()
	handleBackKey(results)
	if menuState != screenStartMenu || time.Since(before) > 100*time.Millisecond {
		t.Fatal("Back blocked on search startup")
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Back dropped the stop while startup was busy")
	}
	for range 2 {
		select {
		case result := <-results:
			applyActionResult(result, results)
		case <-time.After(time.Second):
			t.Fatal("search activity did not finish")
		}
	}
	if uiBusy() {
		t.Fatal("search left a busy activity behind")
	}
	statusMu.RLock()
	message := statusMessage
	statusMu.RUnlock()
	if message != "Search stopped" {
		t.Fatal("late startup result replaced the stop result", message)
	}
	<-serverDone
}

type completedNativeSearchIO struct{}

func (completedNativeSearchIO) Start(context.Context) error { return nil }
func (completedNativeSearchIO) Stop(context.Context) error  { return nil }
func (completedNativeSearchIO) Close() error                { return nil }
func (completedNativeSearchIO) Read(context.Context) ([]byte, error) {
	return []byte{5, 0, 1, 0, 6, 0x0d, 0x23}, nil
}

func TestCompletedNativeSearchOffersSearchAgain(t *testing.T) {
	withMenuState(t)
	dongle := &jabra_DeviceInfo{deviceID: 0, instance: "native-test", productID: 0x24c7, isDongle: true, hidrawPath: "synthetic-cache-only"}
	withDeviceState(t, devices{0: dongle}, -1, 0)
	oldBackend := currentTUIBackend()
	setTUIBackend(nil)
	defer setTUIBackend(oldBackend)
	session, err := btsearch.Start(context.Background(), completedNativeSearchIO{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("synthetic native search did not finish")
	}
	nativeSearch.Lock()
	oldSession, oldTarget, oldToken, oldError := nativeSearch.session, nativeSearch.target, nativeSearch.token, nativeSearch.lastError
	nativeSearch.session, nativeSearch.target, nativeSearch.token, nativeSearch.lastError = session, dongle, strings.Repeat("a", 32), ""
	nativeSearch.Unlock()
	defer func() {
		nativeSearch.Lock()
		nativeSearch.session, nativeSearch.target, nativeSearch.token, nativeSearch.lastError = oldSession, oldTarget, oldToken, oldError
		nativeSearch.Unlock()
	}()
	oldContext := uiResultContext
	uiResultContext = context.Background()
	defer func() { uiResultContext = oldContext }()
	menuState = screenSearch
	clearSearchResults()
	defer cancelSearchRefresh()
	results := make(chan actionResult, 1)
	refreshSearchDeviceList(results)
	select {
	case result := <-results:
		applyActionResult(result, results)
	case <-time.After(time.Second):
		t.Fatal("cached native search did not load")
	}
	if searchViewState.State != "complete" || searchDeviceList.listType != searchComplete {
		t.Fatal("completed native search was shown as active", searchViewState.State, searchDeviceList.listType)
	}
	width, height = 80, 24
	f := composeFrame()
	var text strings.Builder
	for row := 1; row <= height; row++ {
		text.WriteString(rowText(f, row))
	}
	if !strings.Contains(text.String(), "Enter Search again") || strings.Contains(text.String(), "Searching for headsets") {
		t.Fatal("completed native search did not offer a retry")
	}
}

func TestNewUISessionDoesNotWaitForOldSearchCommand(t *testing.T) {
	withMenuState(t)
	oldBackend := currentTUIBackend()
	defer setTUIBackend(oldBackend)
	oldTail, oldGeneration := searchCommandDone, searchCommandGeneration
	defer func() { searchCommandDone, searchCommandGeneration = oldTail, oldGeneration }()
	searchCommandDone = make(chan struct{}) // An old worker which has not returned.
	before := searchCommandGeneration
	resetSearchCommands()
	if searchCommandDone != nil || searchCommandGeneration == before {
		t.Fatal("new UI retained the old search queue")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	oldContext := uiResultContext
	uiResultContext = ctx
	defer func() { uiResultContext = oldContext }()
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
		if request.Method != "bt.search" {
			t.Error("unexpected command", request.Method)
		}
		_ = json.NewEncoder(server).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]bool{"ok": true}})
	}()
	results := make(chan actionResult, 1)
	queueSearchCommand(results, true)
	select {
	case result := <-results:
		if result.err != nil {
			t.Fatal("new search waited for an old session", result.err)
		}
		applyActionResult(result, results)
	case <-ctx.Done():
		t.Fatal("new search waited for an old session")
	}
	<-done
}

func TestSearchQueueWaitStopsWithItsSession(t *testing.T) {
	withMenuState(t)
	oldTail, oldGeneration := searchCommandDone, searchCommandGeneration
	defer func() { searchCommandDone, searchCommandGeneration = oldTail, oldGeneration }()
	searchCommandDone = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	oldContext := uiResultContext
	uiResultContext = ctx
	defer func() { uiResultContext = oldContext }()
	results := make(chan actionResult, 1)
	queueSearchCommand(results, false)
	done := searchCommandDone
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queue wait survived session cancellation")
	}
}

func TestBusySearchStartDoesNotOpenAFalseStartingScreen(t *testing.T) {
	withMenuState(t)
	menuState = screenStartMenu
	currentSelection = 2
	activity := beginUIActivity("Stopping search...")
	defer endUIActivity(activity)
	results := make(chan actionResult, 1)
	activateStartMenuItem(menuItem{id: 0}, results)
	if menuState != screenStartMenu || currentSelection != 2 {
		t.Fatal("rejected Search start changed navigation")
	}
	width, height = 80, 24
	startMenu = []menuItem{{id: 0, label: "Find headset"}}
	currentSelection = 0
	handleKeyEvent(keyEnter, results)
	if menuState != screenStartMenu {
		t.Fatal("busy key handler entered Search")
	}
}

func TestLateDeviceSelectionPreservesNewerNavigation(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, devices{1: {deviceID: 1, instance: "same-device", deviceName: "Headset", productID: 0x24b7}}, 1, -1)
	oldGeneration := deviceSelectionGeneration
	defer func() { deviceSelectionGeneration = oldGeneration }()
	deviceSelectionGeneration++
	selection := &deviceSelectionResult{registryID: 1, instance: "same-device", generation: deviceSelectionGeneration}
	menuState = screenSwitchDevice
	returnToStartMenu()
	currentSelection = 2
	results := make(chan actionResult, 1)
	applyActionResult(actionResult{selectedDevice: selection, returnToMainMenu: true}, results)
	if menuState != screenStartMenu || currentSelection != 2 {
		t.Fatal("late selection reset newer home navigation")
	}
	menuState = screenFirmware
	currentSelection = 1
	applyActionResult(actionResult{selectedDevice: selection, returnToMainMenu: true}, results)
	if menuState != screenFirmware || currentSelection != 1 {
		t.Fatal("late selection left the newer screen")
	}
}

func TestFirmwareBackRemainsAvailableDuringPreparation(t *testing.T) {
	withMenuState(t)
	menuState = screenFirmware
	width, height = 80, 24
	activity := beginUIActivity("Downloading and checking firmware")
	defer endUIActivity(activity)
	results := make(chan actionResult, 1)
	if handleKeyEvent(keyBack, results) || menuState != screenStartMenu {
		t.Fatal("firmware preparation swallowed Back")
	}
}
