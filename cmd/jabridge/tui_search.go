package main

import (
	"context"
	"errors"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

// Only the UI loop owns these fields. Workers send results back rather than
// changing menu state or blocking input while the service is busy.
var searchRefreshGeneration uint64
var searchRefreshPending bool
var searchRefreshCancel context.CancelFunc

// These fields belong to the UI loop. Closing each worker's done channel
// orders Start then Stop even if Back arrives before the Start goroutine runs.
var searchCommandDone <-chan struct{}
var searchCommandGeneration uint64

func resetSearchCommands() {
	searchCommandDone = nil
	searchCommandGeneration++
}

func queueSearchCommand(results chan<- actionResult, start bool) bool {
	if start && uiBusy() {
		return false
	}
	previous := searchCommandDone
	done := make(chan struct{})
	searchCommandDone = done
	searchCommandGeneration++
	generation := searchCommandGeneration
	label, message, method := "Stopping search...", "Search stopped", "bt.search.stop"
	if start {
		label, message, method = "Starting search...", "Device search started", "bt.search"
	}
	activity := beginUIActivity(label)
	parent := currentUIResultContext()
	backend := currentTUIBackend()
	client := backend.clientSnapshot()
	go func() {
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		defer cancel()
		var err error
		if previous != nil {
			select {
			case <-previous:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			if backend != nil {
				if client == nil {
					err = errors.New("jabridge service is not connected")
				} else {
					var result map[string]bool
					err = client.Call(ctx, method, nil, &result)
				}
			} else if start {
				err = searchForNewDevices()
			} else {
				err = stopNativeSearch()
			}
		}
		close(done)
		sendUIResult(parent, results, actionResult{activityID: activity, message: message, err: err, searchCommandGeneration: generation})
	}()
	return true
}

type searchLoadResult struct {
	generation uint64
	state      ipc.SearchState
	devices    []pairedDevice
	err        error
}

func cancelSearchRefresh() {
	searchRefreshGeneration++
	if searchRefreshCancel != nil {
		searchRefreshCancel()
		searchRefreshCancel = nil
	}
	searchRefreshPending = false
}

func refreshSearchDeviceList(results chan<- actionResult) {
	if menuState != screenSearch || searchRefreshPending || time.Now().Before(nextSearchRefresh) {
		return
	}
	nextSearchRefresh = time.Now().Add(time.Second)
	searchRefreshPending = true
	searchRefreshGeneration++
	generation := searchRefreshGeneration
	parent := currentUIResultContext()
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	searchRefreshCancel = cancel
	backend := currentTUIBackend()
	dongle, _ := selectedDongleSnapshot()
	go func() {
		defer cancel()
		load := &searchLoadResult{generation: generation}
		if backend != nil {
			client := backend.clientSnapshot()
			if client == nil {
				load.err = errors.New("jabridge service is not connected")
			} else {
				load.err = client.Call(ctx, "bt.search.status", nil, &load.state)
			}
			for _, device := range load.state.Devices {
				load.devices = append(load.devices, pairedDevice{deviceName: device.Name, isConnected: device.Connected})
			}
		} else if dongle != nil {
			state, devices := currentNativeSearch()
			load.state = state
			if state.DeviceID != dongle.deviceID {
				load.state = ipc.SearchState{State: "idle"}
			} else {
				for _, device := range devices {
					load.devices = append(load.devices, pairedDevice{deviceName: device.Name, deviceBTAddr: device.Address, bluetoothType: device.BluetoothType})
				}
			}
		}
		sendUIResult(parent, results, actionResult{searchLoad: load})
	}()
}

func applySearchLoad(load *searchLoadResult) {
	if load == nil || load.generation != searchRefreshGeneration || menuState != screenSearch {
		return
	}
	searchRefreshPending = false
	searchRefreshCancel = nil
	if load.err != nil {
		searchViewState = ipc.SearchState{State: "failed", Error: load.err.Error()}
	} else {
		searchViewState = load.state
		listType := searchComplete
		if load.state.State == "starting" || load.state.State == "searching" {
			listType = searchResult
		}
		searchDeviceList = &pairingList{count: uint16(len(load.devices)), listType: listType, pairedDevices: load.devices}
		currentSelection = clampSelection(currentSelection, len(load.devices))
	}
	requestUIRedraw()
}
