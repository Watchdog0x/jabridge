package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

func preserveBoundResetUI(t *testing.T) {
	t.Helper()
	target, until, receipt := boundResetTarget, boundResetUntil, boundResetReceipt
	t.Cleanup(func() { boundResetTarget, boundResetUntil, boundResetReceipt = target, until, receipt })
	clearBoundResetConfirmation()
	boundResetReceipt = nil
}
func TestBoundResetConfirmationPinsFullTargetAndExpires(t *testing.T) {
	preserveBoundResetUI(t)
	now := time.Now()
	target := &ipc.SettingTarget{ID: 7, Instance: strings.Repeat("a", 32), Topology: "one"}
	if confirmBoundReset(now, target) {
		t.Fatal("one press sent reset")
	}
	changed := *target
	changed.Instance = strings.Repeat("b", 32)
	if confirmBoundReset(now.Add(time.Second), &changed) {
		t.Fatal("changed attachment accepted confirmation")
	}
	if !confirmBoundReset(now.Add(2*time.Second), &changed) {
		t.Fatal("unchanged second target not accepted")
	}
	if boundResetTarget != nil || !boundResetUntil.IsZero() {
		t.Fatal("confirmation retained")
	}
	if confirmBoundReset(now, target) || confirmBoundReset(now.Add(11*time.Second), target) {
		t.Fatal("expired confirmation accepted")
	}
	if confirmBoundReset(now, nil) || boundResetTarget != nil {
		t.Fatal("nil target retained")
	}
}
func TestBoundResetWarningAndReceiptsFitSmallTerminal(t *testing.T) {
	withMenuState(t)
	preserveBoundResetUI(t)
	scanned := firstScanComplete.Load()
	firstScanComplete.Store(true)
	t.Cleanup(func() { firstScanComplete.Store(scanned) })
	oldBackend := currentTUIBackend()
	setTUIBackend(nil)
	t.Cleanup(func() { setTUIBackend(oldBackend) })
	device := &jabra_DeviceInfo{deviceID: 7, instance: strings.Repeat("a", 32), vendorID: jabraVendorID, productID: 0x2e57, deviceName: "Simulated Link 390", isDongle: true, variantType: "04-10", firmwareVersion: "1.3.0", serialNumber: "simulated", usbDevicePath: "/usb/test", usbGeneration: "simulated-generation"}
	withDeviceState(t, devices{7: device}, -1, 7)
	for _, size := range [][2]int{{40, 20}, {96, 28}} {
		f := newRenderTarget(t, size[0], size[1])
		menuState = screenDongleSettings
		confirmBoundReset(time.Now(), settingTarget(device))
		header()
		renderDongleSettings()
		exportBoundResetFrame(t, f, "confirmation")
		var text strings.Builder
		for row := 1; row <= f.height; row++ {
			text.WriteString(resetPanelRow(f, row))
			text.WriteByte(' ')
		}
		compact := strings.Join(strings.Fields(text.String()), " ")
		for _, phrase := range []string{"Stop calls and audio first.", "Settings may return to defaults.", "Headsets may need pairing again.", "An address-only record can remain.", "Cancel", "Reset"} {
			if !strings.Contains(compact, phrase) {
				t.Fatal(size, phrase, compact)
			}
		}
		clearBoundResetConfirmation()
		for _, outcome := range []link390ResetOutcome{resetRefused, resetRejected, resetUnknown, resetRequested, resetRestartVerified} {
			receipt := ipcLink390ResetResult(link390ResetResult{Outcome: outcome})
			boundResetReceipt = &receipt
			f = newRenderTarget(t, size[0], size[1])
			header()
			renderDongleSettings()
			exportBoundResetFrame(t, f, string(outcome))
			var body strings.Builder
			for row := 1; row <= f.height; row++ {
				body.WriteString(resetPanelRow(f, row))
				body.WriteByte(' ')
			}
			visible := strings.Join(strings.Fields(body.String()), " ")
			if !strings.Contains(visible, receipt.Message) || !strings.Contains(visible, "Q Back") {
				t.Fatal("result clipped", size, receipt, visible)
			}
		}
		boundResetReceipt = nil
	}
}

func resetPanelRow(f *frame, row int) string {
	left, right, bottom := panelBounds()
	if row < 7 || row >= bottom {
		return rowText(f, row)
	}
	return string([]rune(rowText(f, row))[left+3 : right-4])
}

// Optional evidence export contains only this synthetic test device and the
// actual rendered cells; normal tests write no screenshots.
func exportBoundResetFrame(t *testing.T, f *frame, label string) {
	t.Helper()
	dir := os.Getenv("JABRIDGE_TEST_RESET_SCREEN_DIR")
	if dir == "" {
		return
	}
	type cell struct {
		Text  string `json:"text"`
		Style string `json:"style"`
	}
	dump := struct {
		Width  int    `json:"width"`
		Height int    `json:"height"`
		Cells  []cell `json:"cells"`
	}{Width: f.width, Height: f.height}
	for _, item := range f.cells {
		dump.Cells = append(dump.Cells, cell{Text: string(item.ch), Style: item.style})
	}
	data, err := json.Marshal(dump)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%dx%d.json", label, f.width, f.height))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
