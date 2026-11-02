package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"strings"
	"testing"
	"time"
)

func TestStablePairingDefaultsRejectCachedAndRawReadsBeforeIO(t *testing.T) {
	t.Setenv(experimentalWritesEnv, experimentalWritesAck)
	for _, pid := range []uint16{0x24ca, 0x24e9, 0x2e56} {
		d := &jabra_DeviceInfo{deviceID: 0, instance: "default-off", productID: pid, isDongle: true, hidrawPath: "synthetic-no-device", searchConnect: true, pairingList: &pairingList{pairedDevices: []pairedDevice{{deviceName: "Cached", isConnected: true}}}}
		withDeviceState(t, devices{0: d}, -1, 0)
		if _, err := getPairingList(0); !errors.Is(err, ErrNotSupported) {
			t.Fatal("disabled list reached transport", pid, err)
		}
		if list := (&jabraAPIBridge{}).GetPairingList(); len(list) != 0 {
			t.Fatal("disabled list exposed cached entries", pid, list)
		}
		if err := setDongleInBTPairing(true); !errors.Is(err, ErrNotSupported) {
			t.Fatal("global write flag bypassed pairing policy", pid, err)
		}
	}
}

func TestServicePermissionControlsLink380CFindVisibility(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, make(devices), -1, -1)
	old := currentTUIBackend()
	setTUIBackend(&tuiIPCBackend{})
	t.Cleanup(func() { setTUIBackend(old) })
	for _, granted := range []bool{false, true} {
		replaceTUIDeviceState([]ipc.DeviceInfo{{ID: 0, Instance: "service", Name: "Link 380c", IsDongle: true, PID: 0x24c9, Selected: true, SearchConnect: granted}}, nil, ipc.FeatureInfo{})
		updateStartMenu()
		found := false
		for _, item := range startMenu {
			found = found || item.id == 0
		}
		if found != granted || !rememberedPreviewIsReadOnly() {
			t.Fatal("client lost daemon permission or unlocked remembered actions", granted, found)
		}
	}
}

// Search permission can change while an old menu or search screen stays open.
func withSearchPermissionTest(t *testing.T) chan actionResult {
	t.Helper()
	withMenuState(t)
	withDeviceState(t, make(devices), -1, -1)
	oldBackend, oldContext := currentTUIBackend(), uiResultContext
	oldList, oldState := searchDeviceList, searchViewState
	oldTail, oldGeneration := searchCommandDone, searchCommandGeneration
	uiResultContext = context.Background()
	resetSearchCommands()
	t.Cleanup(func() {
		setTUIBackend(oldBackend)
		uiResultContext = oldContext
		searchDeviceList, searchViewState = oldList, oldState
		searchCommandDone, searchCommandGeneration = oldTail, oldGeneration
	})
	return make(chan actionResult, 4)
}

func searchPermissionTestService(t *testing.T) <-chan string {
	t.Helper()
	backend, pipes := newTUIActionPipeBackend(t)
	setTUIBackend(backend)
	methods := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			server, ok := pipes.Accept()
			if !ok {
				return
			}
			var request ipc.Request
			if json.NewDecoder(server).Decode(&request) != nil {
				return
			}
			methods <- request.Method
			if json.NewEncoder(server).Encode(ipc.SuccessResponse(request.ID, map[string]bool{"ok": true})) != nil {
				return
			}
			// The action client closes after reading; pipes owns server cleanup.
		}
	}()
	t.Cleanup(func() { pipes.Close(); <-done })
	return methods
}

func finishSearchPermissionAction(t *testing.T, results chan actionResult) {
	t.Helper()
	select {
	case result := <-results:
		if result.err != nil {
			t.Error("fake search request failed", result.err)
		}
		applyActionResult(result, results)
	case <-time.After(time.Second):
		t.Fatal("search action did not finish")
	}
}

func TestIssue58StaleSearchStartRespectsServicePermission(t *testing.T) {
	for _, state := range []string{"complete", "stopped", "timed_out", "cached-home"} {
		for _, permission := range []string{"false", "omitted"} {
			t.Run(state+"/"+permission, func(t *testing.T) {
				results := withSearchPermissionTest(t)
				methods := searchPermissionTestService(t)
				info := ipc.DeviceInfo{ID: 0, Instance: "service", Name: "Link 380c", IsDongle: true, PID: 0x24c9, Selected: true, SearchConnect: true}
				replaceTUIDeviceState([]ipc.DeviceInfo{info}, nil, ipc.FeatureInfo{})
				updateStartMenu()
				cached := menuItem{id: -1}
				for _, item := range startMenu {
					if item.id == 0 {
						cached = item
					}
				}
				if cached.id != 0 {
					t.Fatal("positive control did not expose Find headset")
				}
				payload := `{"id":0,"instance":"service","isDongle":true,"pid":9417,"selected":true}`
				if permission == "false" {
					payload = strings.TrimSuffix(payload, "}") + `,"searchConnect":false}`
				}
				var refreshed ipc.DeviceInfo
				if err := json.Unmarshal([]byte(payload), &refreshed); err != nil {
					t.Fatal(err)
				}
				replaceTUIDeviceState([]ipc.DeviceInfo{refreshed}, nil, ipc.FeatureInfo{})
				currentSelection = 0
				menuState = screenSearch
				searchDeviceList = &pairingList{}
				searchViewState = ipc.SearchState{State: state}
				if state == "cached-home" {
					menuState = screenStartMenu
					startMenu = []menuItem{cached}
				}
				beforeGeneration, beforeScreen := searchCommandGeneration, menuState
				handleEnterKey(results)
				if searchCommandGeneration != beforeGeneration || uiBusy() || len(results) != 0 {
					t.Error("denied service permission still scheduled a search")
					finishSearchPermissionAction(t, results)
				}
				if menuState != beforeScreen {
					t.Error("refused search changed navigation")
				}
				select {
				case method := <-methods:
					t.Error("denied service permission dispatched IPC", method)
				case <-time.After(20 * time.Millisecond):
				}
			})
		}
	}
}

func TestIssue58AllowedSearchAndRevokedStop(t *testing.T) {
	results := withSearchPermissionTest(t)
	methods := searchPermissionTestService(t)
	info := ipc.DeviceInfo{ID: 0, Instance: "service", IsDongle: true, PID: 0x24c9, Selected: true, SearchConnect: true}
	replaceTUIDeviceState([]ipc.DeviceInfo{info}, nil, ipc.FeatureInfo{})
	activateStartMenuItem(menuItem{id: 0}, results)
	if !uiBusy() || menuState != screenSearch {
		t.Fatal("permitted search did not schedule and open its screen")
	}
	finishSearchPermissionAction(t, results)
	if method := <-methods; method != "bt.search" {
		t.Fatal("wrong search request", method)
	}
	info.SearchConnect = false
	replaceTUIDeviceState([]ipc.DeviceInfo{info}, nil, ipc.FeatureInfo{})
	handleBackKey(results)
	finishSearchPermissionAction(t, results)
	if method := <-methods; method != "bt.search.stop" || menuState != screenStartMenu {
		t.Fatal("revoked permission prevented search stop", method, menuState)
	}
}

func TestIssue58DirectSearchStartGate(t *testing.T) {
	for _, pid := range []uint16{0x24c9, 0x2e56} {
		t.Run(fmt.Sprintf("%04x", pid), func(t *testing.T) {
			results := withSearchPermissionTest(t)
			setTUIBackend(nil)
			withDeviceState(t, devices{0: {deviceID: 0, isDongle: true, productID: pid}}, -1, 0)
			// Force the native path to fail before any USB operation.
			nativeSearch.Lock()
			oldParent, oldError := nativeSearch.parent, nativeSearch.lastError
			nativeSearch.parent = nil
			nativeSearch.Unlock()
			t.Cleanup(func() {
				nativeSearch.Lock()
				nativeSearch.parent, nativeSearch.lastError = oldParent, oldError
				nativeSearch.Unlock()
			})
			before := searchCommandGeneration
			started := queueSearchCommand(results, true)
			if pid == 0x24c9 {
				if !started || !uiBusy() {
					t.Fatal("direct mode ignored the default pairing policy")
				}
			} else if started || uiBusy() || searchCommandGeneration != before {
				t.Error("unsupported direct search passed the gate")
			}
			if started {
				select {
				case result := <-results:
					if result.err == nil {
						t.Error("native fixture unexpectedly reached a service")
					}
					endUIActivity(result.activityID)
				case <-time.After(time.Second):
					t.Fatal("native fixture did not finish")
				}
			}
		})
	}
}

func TestIssue58SearchRequiresSelectedDongle(t *testing.T) {
	results := withSearchPermissionTest(t)
	methods := searchPermissionTestService(t)
	before := searchCommandGeneration
	if queueSearchCommand(results, true) || uiBusy() || searchCommandGeneration != before || len(results) != 0 {
		t.Error("search without a selected dongle was scheduled")
		finishSearchPermissionAction(t, results)
	}
	select {
	case method := <-methods:
		t.Error("search without a selected dongle dispatched IPC", method)
	case <-time.After(20 * time.Millisecond):
	}
}
