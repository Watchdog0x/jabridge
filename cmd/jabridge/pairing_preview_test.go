package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

func TestLink390ReadOnlyPreviewKeepsWriteAndResetGates(t *testing.T) {
	if !supportsValidatedPairingReads(0x2e56) || !usesDualModeSearch(&jabra_DeviceInfo{isDongle: true, productID: 0x2e56}) {
		t.Fatal("read-only discovery is missing")
	}
	if supportsExperimentalDongleWrites(0x2e56) || supportsValidatedFactoryReset(0x2e56) {
		t.Fatal("read-only preview enabled pairing/reset writes")
	}
	for _, pid := range []uint16{0x2e50, 0x2e51} {
		if supportsValidatedPairingReads(pid) || supportsExperimentalDongleWrites(pid) || supportsValidatedFactoryReset(pid) {
			t.Fatal("unrelated Link390 variant gained access")
		}
	}
}

func TestReadOnlyPairingShortcutsNeverDispatchMutations(t *testing.T) {
	for _, test := range []struct {
		name              string
		screen            int
		event             keyEvent
		connected, forget bool
		method            string
	}{
		{"search-enter", screenSearch, keyEnter, false, false, "bt.search.connect"},
		{"search-action", screenSearch, keyAction1, false, false, "bt.search.connect"},
		{"remembered-enter-disconnected", screenPairedDevices, keyEnter, false, false, "bt.connect"},
		{"remembered-action-disconnected", screenPairedDevices, keyAction1, false, false, "bt.connect"},
		{"remembered-enter-connected", screenPairedDevices, keyEnter, true, false, "bt.disconnect"},
		{"remembered-action-connected", screenPairedDevices, keyAction1, true, false, "bt.disconnect"},
		{"armed-forget", screenPairedDevices, keyAction2, false, true, "bt.forget"},
	} {
		for _, pid := range []uint16{0x2e56, 0x24c9, 0x2e57} {
			t.Run(fmt.Sprintf("%s/%04x", test.name, pid), func(t *testing.T) {
				withMenuState(t)
				withDeviceState(t, make(devices), -1, -1)
				oldList, oldState, oldContext, oldBackend := searchDeviceList, searchViewState, uiResultContext, currentTUIBackend()
				t.Cleanup(func() {
					searchDeviceList, searchViewState, uiResultContext = oldList, oldState, oldContext
					setTUIBackend(oldBackend)
				})
				width, height = 100, 30
				menuState = test.screen
				currentSelection = 0
				uiResultContext = context.Background()
				entry := pairedDevice{deviceName: "Test headset", isConnected: test.connected}
				addDevice(&jabra_DeviceInfo{productID: pid, isDongle: true, searchConnect: pid == 0x24c9, deviceName: "Jabra Link", pairingList: &pairingList{pairedDevices: []pairedDevice{entry}}})
				searchDeviceList = &pairingList{pairedDevices: []pairedDevice{entry}}
				searchViewState = ipc.SearchState{State: "complete", Session: strings.Repeat("a", 32)}
				if test.forget {
					forgetConfirmKey = "0:Test headset"
					forgetConfirmUntil = time.Now().Add(time.Minute)
				}
				server, connection := net.Pipe()
				client := ipc.ClientFromConnection(connection)
				setTUIBackend(&tuiIPCBackend{client: client})
				requests := make(chan string, 2)
				done := make(chan struct{})
				go func() {
					defer close(done)
					var request ipc.Request
					if json.NewDecoder(server).Decode(&request) == nil {
						requests <- request.Method
						_ = json.NewEncoder(server).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]bool{"ok": true}})
					}
				}()
				t.Cleanup(func() { _ = server.Close(); _ = client.Close(); <-done })
				results := make(chan actionResult, 2)
				if test.event == keyEnter {
					handleEnterKey(results)
				} else {
					handleActionKey(test.event, results)
				}
				if pid == 0x2e56 || pid == 0x24c9 && test.screen != screenSearch {
					// Dispatch schedules a UI activity synchronously before its
					// goroutine. No activity therefore also rules out a late send.
					if uiBusy() || len(results) != 0 || len(requests) != 0 {
						t.Fatal("read-only shortcut scheduled or sent a mutation")
					}
					return
				}
				// Positive control: the same selectable entry and armed forget
				// must reach the exact mutation route for a writable dongle.
				select {
				case method := <-requests:
					if method != test.method {
						t.Fatal("wrong positive-control mutation", method, test.method)
					}
				case <-time.After(time.Second):
					t.Fatal("positive control did not dispatch")
				}
				select {
				case result := <-results:
					endUIActivity(result.activityID)
					if result.err != nil {
						t.Fatal(result.err)
					}
				case <-time.After(time.Second):
					t.Fatal("positive control did not complete")
				}
			})
		}
	}
}

func TestReadOnlyPairingMenusDoNotOfferConnectForget(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, make(devices), -1, -1)
	addDevice(&jabra_DeviceInfo{productID: 0x2e56, isDongle: true, deviceName: "Jabra Link 390", pairingList: &pairingList{}})
	if !rememberedPreviewIsReadOnly() {
		t.Fatal("wrong preview UI scope")
	}
	target := newRenderTarget(t, 100, 30)
	menuPairedDevices()
	text := ""
	for row := 1; row <= 30; row++ {
		text += rowText(target, row) + "\n"
	}
	if !strings.Contains(text, "read only") || strings.Contains(text, "Enter Connect") || strings.Contains(text, "Forget") {
		t.Fatal("read-only list offered a write", text)
	}
}
