package main

import (
	"errors"
	"github.com/Watchdog0x/jabridge/internal/firmware"
	"os"
)

type firmwareLanguagePicker struct {
	choices                     []firmware.VoiceLanguage
	choice                      int
	current                     string
	currentPack                 string
	currentID                   uint16
	file, attachment, targetKey string
	request                     uint64
	pid                         uint16
}

var activeFirmwareLanguagePicker *firmwareLanguagePicker
var voiceLanguageReadForTUI = firmware.ReadVoiceLanguage
var voiceLanguageDownloadForTUI = downloadFirmwareForTUI
var voiceLanguageCaptureForTUI = firmware.CaptureInstallAttachment
var voiceLanguageOptionsForTUI = firmware.VoiceLanguages
var voiceLanguagePrepareForTUI = firmware.PrepareInteractiveLanguageInstall

func startFirmwareLanguageSelection(results chan<- actionResult) {
	if width < 60 || height < 22 {
		setStatus("Resize to 60x22 to review a voice language update", true)
		return
	}
	if uiBusy() {
		return
	}
	target, exists := selectedFirmwareTarget()
	if !exists || target.Device.productID != 0x2549 || target.Device.deviceConnection != deviceConnectionType_USB {
		setStatus("Connect Evolve3 65 directly by USB to change its voice language", true)
		return
	}
	if os.Getenv(firmware.CSRLanguageEnv) != firmware.CSRLanguageAcknowledgement {
		setStatus("Voice language updates need the preview opt-in. See the test instructions.", true)
		return
	}
	firmwareViewMu.RLock()
	view := firmwareView
	firmwareViewMu.RUnlock()
	if view.loading || view.targetKey != firmwareTargetBinding(target) || view.latestVersion == "" {
		setStatus("Wait for the firmware check", false)
		return
	}
	device := *target.Device
	activity := beginUIActivity("Reading voice language and checking the official package. No device changes yet.")
	ctx := currentUIResultContext()
	go func() {
		result := actionResult{activityID: activity, firmwareRequest: view.request}
		attachment, err := voiceLanguageCaptureForTUI(device.productID)
		if err != nil {
			result.err = err
			sendUIResult(ctx, results, result)
			return
		}
		current, err := voiceLanguageReadForTUI(ctx, device.productID)
		if err != nil {
			result.err = err
			sendUIResult(ctx, results, result)
			return
		}
		file, err := voiceLanguageDownloadForTUI(&device)
		if err == nil && file.Version != current.Firmware {
			err = errors.New("the official package must match the installed firmware before changing voice language")
		}
		var choices []firmware.VoiceLanguage
		if err == nil {
			choices, err = voiceLanguageOptionsForTUI(file.Path)
		}
		if err == nil {
			picker := &firmwareLanguagePicker{choices: choices, current: current.Name, currentID: current.ID, currentPack: current.PackVersion, file: file.Path, attachment: attachment, targetKey: view.targetKey, request: view.request, pid: device.productID}
			for index, language := range choices {
				if language.ID == current.ID {
					picker.choice = index
				}
			}
			result.languageOptions = picker
		}
		result.err = err
		sendUIResult(ctx, results, result)
	}()
}

func handleFirmwareLanguageKey(event keyEvent, results chan<- actionResult) {
	picker := activeFirmwareLanguagePicker
	if picker == nil {
		return
	}
	if width < 60 || height < 22 {
		if navigationKey(event) == keyBack {
			activeFirmwareLanguagePicker = nil
			requestUIRedraw()
		}
		return
	}
	switch navigationKey(event) {
	case keyBack:
		activeFirmwareLanguagePicker = nil
		setStatus("No language changed.", false)
	case keyUp:
		picker.choice = max(0, picker.choice-1)
	case keyDown:
		picker.choice = min(len(picker.choices)-1, picker.choice+1)
	case keyEnter:
		target, exists := selectedFirmwareTarget()
		firmwareViewMu.RLock()
		request := firmwareView.request
		firmwareViewMu.RUnlock()
		if !exists || firmwareTargetBinding(target) != picker.targetKey || request != picker.request {
			activeFirmwareLanguagePicker = nil
			setStatus("Device changed; select the headset again", true)
			break
		}
		if picker.choice < 0 || picker.choice >= len(picker.choices) {
			return
		}
		language := picker.choices[picker.choice]
		if language.ID == picker.currentID && language.Version == picker.currentPack {
			activeFirmwareLanguagePicker = nil
			setStatus("This voice language is already selected.", false)
			break
		}
		selected := *picker
		activeFirmwareLanguagePicker = nil
		activity := beginUIActivity("Checking voice language update. Confirmation is still required.")
		ctx := currentUIResultContext()
		go func() {
			plan, err := voiceLanguagePrepareForTUI(selected.file, selected.pid, selected.attachment, language.ID)
			sendUIResult(ctx, results, actionResult{activityID: activity, firmwareRequest: selected.request, installPlan: plan, err: err})
		}()
	}
	requestUIRedraw()
}

func renderFirmwareLanguagePicker() {
	picker := activeFirmwareLanguagePicker
	if picker == nil {
		return
	}
	drawingBox()
	if width < 60 || height < 22 {
		drawCentered(6, "Needs 60x22 to review voice language", false)
		drawSplitActionBar([]string{"Esc Cancel"}, nil)
		return
	}
	drawCenteredStyled(6, "Voice prompt language", styleTitle)
	left, right, bottom := panelBounds()
	drawListItem(8, left+4, trimToWidth("Current: "+picker.current, max(1, right-left-8)), false)
	visible := max(1, bottom-12)
	start, end := listWindow(picker.choice, len(picker.choices), visible)
	drawListWindowHint(9, start, end, len(picker.choices))
	for index := start; index < end; index++ {
		drawListItem(10+index-start, left+4, picker.choices[index].Name, index == picker.choice)
	}
	drawListItem(bottom-1, left+4, trimToWidth("The headset restarts. You will confirm before installation.", max(1, right-left-8)), false)
	drawSplitActionBar([]string{"Esc Cancel"}, []string{hintMove, "Enter Continue"})
}
