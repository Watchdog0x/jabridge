package main

import (
	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"strings"
	"testing"
)

func TestFindHeadsetMenuUsesSupportedDongle(t *testing.T) {
	withMenuState(t)
	old := currentTUIBackend()
	setTUIBackend(&tuiIPCBackend{})
	defer setTUIBackend(old)
	for _, pid := range []uint16{0x24c7, 0x24c8, 0x2e50, 0xffff} {
		t.Run(string(rune(pid)), func(t *testing.T) {
			d := metadataTestDongle()
			d.productID = pid
			withDeviceState(t, devices{0: d}, -1, 0)
			updateStartMenu()
			found := false
			for _, item := range startMenu {
				found = found || item.id == 0 && item.label == "Find headset"
			}
			if found != supportsValidatedPairingReads(pid) {
				t.Fatal(pid, found)
			}
		})
	}
}

func TestEmptySearchStopsDisplayingSearchingAfterCompletion(t *testing.T) {
	withMenuState(t)
	oldList, oldState := searchDeviceList, searchViewState
	defer func() { searchDeviceList, searchViewState = oldList, oldState }()
	searchDeviceList = &pairingList{}
	for _, state := range []string{"searching", "complete", "stopped", "timed_out"} {
		searchViewState = ipc.SearchState{State: state}
		f := newRenderTarget(t, 80, 24)
		menuSearchForNewDevices()
		var lines strings.Builder
		for row := 1; row <= 24; row++ {
			lines.WriteString(rowText(f, row))
		}
		text := lines.String()
		if state == "searching" {
			if !strings.Contains(text, "Searching for headsets") {
				t.Fatal(text)
			}
		} else if strings.Contains(text, "Searching for headsets") || !strings.Contains(text, "Enter Search again") {
			t.Fatal(state, text)
		}
	}
}
