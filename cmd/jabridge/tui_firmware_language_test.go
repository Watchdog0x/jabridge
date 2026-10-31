package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/internal/firmware"
)

func languagePickerFixture(t *testing.T) (chan actionResult, *int) {
	t.Helper()
	withMenuState(t)
	withDeviceState(t, make(devices), -1, -1)
	oldPicker, oldPlan := activeFirmwareLanguagePicker, pendingFirmwareInstall
	oldRead, oldDownload, oldCapture, oldOptions, oldPrepare := voiceLanguageReadForTUI, voiceLanguageDownloadForTUI, voiceLanguageCaptureForTUI, voiceLanguageOptionsForTUI, voiceLanguagePrepareForTUI
	firmwareViewMu.Lock()
	oldView := firmwareView
	firmwareViewMu.Unlock()
	t.Cleanup(func() {
		activeFirmwareLanguagePicker, pendingFirmwareInstall = oldPicker, oldPlan
		voiceLanguageReadForTUI, voiceLanguageDownloadForTUI, voiceLanguageCaptureForTUI, voiceLanguageOptionsForTUI, voiceLanguagePrepareForTUI = oldRead, oldDownload, oldCapture, oldOptions, oldPrepare
		firmwareViewMu.Lock()
		firmwareView = oldView
		firmwareViewMu.Unlock()
	})
	t.Setenv(firmware.CSRLanguageEnv, firmware.CSRLanguageAcknowledgement)
	width, height = 80, 24
	menuState = screenFirmware
	addDevice(&jabra_DeviceInfo{vendorID: jabraVendorID, productID: 0x2549, deviceName: "Jabra Evolve3 65", deviceConnection: deviceConnectionType_USB, firmwareVersion: "1.0.1"})
	refreshFirmwareTargets()
	target, exists := selectedFirmwareTarget()
	if !exists {
		t.Fatal("missing firmware target")
	}
	firmwareViewMu.Lock()
	firmwareView = firmwareViewState{request: 99, targetKey: firmwareTargetBinding(target), targetPID: 0x2549, currentVersion: "1.0.1", latestVersion: "1.0.1"}
	firmwareViewMu.Unlock()
	voiceLanguageCaptureForTUI = func(uint16) (string, error) { return "test-attachment", nil }
	voiceLanguageReadForTUI = func(context.Context, uint16) (firmware.VoiceLanguageStatus, error) {
		return firmware.VoiceLanguageStatus{ID: 0x409, Name: "English (US)", Firmware: "1.0.1", PackVersion: "0.2.1"}, nil
	}
	voiceLanguageDownloadForTUI = func(*jabra_DeviceInfo) (firmware.DownloadResult, error) {
		return firmware.DownloadResult{Path: "test-only.zip", Version: "1.0.1"}, nil
	}
	voiceLanguageOptionsForTUI = func(string) ([]firmware.VoiceLanguage, error) {
		return []firmware.VoiceLanguage{{ID: 0x409, Name: "English (US)", Version: "0.2.1"}, {ID: 0x407, Name: "German", Version: "0.2.1"}}, nil
	}
	prepared := 0
	voiceLanguagePrepareForTUI = func(path string, pid uint16, attachment string, language uint16) (*firmware.PreparedInstall, error) {
		prepared++
		if path != "test-only.zip" || pid != 0x2549 || attachment != "test-attachment" || language != 0x407 {
			t.Error("language handoff lost its file/device/language binding")
		}
		return new(firmware.PreparedInstall), nil
	}
	return make(chan actionResult, 2), &prepared
}

func loadLanguagePicker(t *testing.T, results chan actionResult) {
	t.Helper()
	startFirmwareLanguageSelection(results)
	select {
	case result := <-results:
		if result.err != nil {
			t.Fatal(result.err)
		}
		applyActionResult(result, results)
	case <-time.After(time.Second):
		t.Fatal("language options did not arrive")
	}
	if activeFirmwareLanguagePicker == nil {
		t.Fatal("language picker did not open")
	}
}

func TestFirmwareLanguagePickerCancelAndUnchangedDoNotPrepareUpdate(t *testing.T) {
	results, prepared := languagePickerFixture(t)
	loadLanguagePicker(t, results)
	handleFirmwareLanguageKey(keyBack, results)
	if activeFirmwareLanguagePicker != nil || *prepared != 0 || pendingFirmwareInstall != nil {
		t.Fatal("cancel prepared a write")
	}
	loadLanguagePicker(t, results)
	handleFirmwareLanguageKey(keyEnter, results)
	if activeFirmwareLanguagePicker != nil || *prepared != 0 || pendingFirmwareInstall != nil {
		t.Fatal("unchanged language prepared a write")
	}
}

func TestFirmwareLanguageSelectionPreparesOnlyTheChosenLanguage(t *testing.T) {
	results, prepared := languagePickerFixture(t)
	loadLanguagePicker(t, results)
	handleFirmwareLanguageKey(keyDown, results)
	handleFirmwareLanguageKey(keyEnter, results)
	select {
	case result := <-results:
		if result.err != nil || result.installPlan == nil || result.firmwareRequest != 99 {
			t.Fatal("invalid prepared result", result.err)
		}
		applyActionResult(result, results)
	case <-time.After(time.Second):
		t.Fatal("language preparation did not arrive")
	}
	if *prepared != 1 || pendingFirmwareInstall == nil || activeFirmwareLanguagePicker != nil {
		t.Fatal("language selection did not use the confirmation handoff")
	}
}

func TestFirmwareLanguagePickerRejectsChangedDeviceAndSmallTerminal(t *testing.T) {
	results, prepared := languagePickerFixture(t)
	loadLanguagePicker(t, results)
	handleFirmwareLanguageKey(keyDown, results)
	firmwareViewMu.Lock()
	firmwareView.request++
	firmwareViewMu.Unlock()
	handleFirmwareLanguageKey(keyEnter, results)
	if *prepared != 0 || activeFirmwareLanguagePicker != nil {
		t.Fatal("stale device selection prepared an update")
	}
	loadLanguagePicker(t, results)
	width, height = 48, 18
	handleFirmwareLanguageKey(keyEnter, results)
	if *prepared != 0 || activeFirmwareLanguagePicker == nil {
		t.Fatal("small terminal accepted an update")
	}
	handleFirmwareLanguageKey(keyEscape, results)
	if activeFirmwareLanguagePicker != nil {
		t.Fatal("small terminal hid cancel")
	}
}

func TestFirmwareLanguagePickerKeepsCurrentAndKeysVisible(t *testing.T) {
	results, _ := languagePickerFixture(t)
	loadLanguagePicker(t, results)
	for _, size := range [][2]int{{60, 22}, {80, 24}, {120, 40}} {
		target := newRenderTarget(t, size[0], size[1])
		renderFirmwareLanguagePicker()
		text := ""
		for row := 1; row <= size[1]; row++ {
			text += rowText(target, row) + "\n"
		}
		for _, want := range []string{"Voice prompt language", "Current: English (US)", "Cancel", "Continue"} {
			if !strings.Contains(text, want) {
				t.Fatal("language UI hid text/key", size, want, text)
			}
		}
	}
}

func TestCancelledFirmwareLanguageResultCannotReachInstaller(t *testing.T) {
	results, _ := languagePickerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	plan := new(firmware.PreparedInstall)
	sendUIResult(ctx, results, actionResult{installPlan: plan, firmwareRequest: 99})
	cancel()
	result := <-results
	applyActionResult(result, results)
	if pendingFirmwareInstall != nil || activeFirmwareLanguagePicker != nil {
		t.Fatal("queued result from a cancelled UI reached the installer")
	}
	sendUIResult(ctx, results, actionResult{installPlan: plan, firmwareRequest: 99})
	if len(results) != 0 {
		t.Fatal("already-cancelled producer queued a firmware handoff")
	}
}
