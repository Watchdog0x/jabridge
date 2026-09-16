package main

import (
	"errors"
	"fmt"

	"github.com/Watchdog0x/jabridge/internal/firmware"
)

var pendingFirmwareInstall *firmware.PreparedInstall // UI loop -> terminal handoff

// UI loop only

func firmwareNumber(event keyEvent) int {
	if event >= keyAction1 && event <= keyAction4 {
		return int(event-keyAction1) + 1
	}
	if event >= keyRuneBase+'1' && event <= keyRuneBase+'9' {
		return int(event - keyRuneBase - '0')
	}
	if event == keyRuneBase+'0' {
		return 10
	}
	return 0
}

func selectFirmwareTargetNumber(number int, results chan<- actionResult) {
	if number < 1 || number > len(firmwareTargetItems) {
		setStatus("Choose a device number shown in the list", true)
		return
	}
	if number-1 == firmwareTargetIndex {
		return
	}
	firmwareTargetIndex = number - 1
	firmwareTargetID = firmwareTargetItems[firmwareTargetIndex].RegistryID
	refreshFirmwareView(results)
}

func handleFirmwareKey(event keyEvent, results chan<- actionResult) bool {
	if menuState != screenFirmware {
		return false
	}
	if navigationKey(event) == keyBack {
		return false
	}
	if uiBusy() {
		return true
	}
	if n := firmwareNumber(event); n != 0 {
		selectFirmwareTargetNumber(n, results)
		return true
	}
	switch navigationKey(event) {
	case keyUp:
		selectFirmwareTargetNumber(max(1, firmwareTargetIndex), results)
		return true
	case keyDown:
		selectFirmwareTargetNumber(min(len(firmwareTargetItems), firmwareTargetIndex+2), results)
		return true
	}
	return false
}

func startFirmwareUpdate(results chan<- actionResult) {
	if width < 60 || height < 22 {
		setStatus("Resize to 60x22 to review firmware before updating", true)
		return
	}
	if uiBusy() {
		return
	}
	target, exists := selectedFirmwareTarget()
	if !exists {
		setStatus("No device selected", true)
		return
	}
	firmwareViewMu.RLock()
	view := firmwareView
	firmwareViewMu.RUnlock()
	if view.loading || view.targetKey != firmwareTargetBinding(target) {
		setStatus("Wait for the firmware check", false)
		return
	}
	if view.latestVersion == "" {
		refreshFirmwareView(results)
		return
	}
	if view.currentVersion == view.latestVersion {
		setStatus("This device is already up to date", false)
		return
	}
	device := *target.Device

	if device.deviceConnection != deviceConnectionType_USB {
		parent, ok := deviceAt(int(device.parentDeviceID))
		if !ok || !firmware.SupportsWirelessFirmware(parent.productID, device.productID) {
			setStatus("Connect this headset directly by USB to install firmware", true)
			return
		}
	}
	startRealFirmwarePreparation(results, device, view)
}

func startRealFirmwarePreparation(results chan<- actionResult, device jabra_DeviceInfo, view firmwareViewState) {
	activity := beginUIActivity("Downloading and checking firmware. No device writes yet.")
	ctx := currentUIResultContext()
	var parent *jabra_DeviceInfo
	if device.deviceConnection == deviceConnectionType_BT {
		parent, _ = deviceAt(int(device.parentDeviceID))
	}
	go func() {
		result := actionResult{activityID: activity, firmwareRequest: view.request}
		if device.deviceConnection == deviceConnectionType_BT && (parent == nil || !firmware.SupportsWirelessFirmware(parent.productID, device.productID) || device.firmwareIdentity == "") {
			result.err = errors.New("wait for the headset identity check, then try again")
			sendUIResult(ctx, results, result)
			return
		}
		capturePID := device.productID
		if parent != nil {
			capturePID = parent.productID
		}
		attachment, err := firmware.CaptureInstallAttachment(capturePID)
		if err != nil {
			result.err = err
			sendUIResult(ctx, results, result)
			return
		}
		file, err := downloadFirmwareForTUI(&device)
		if err == nil {
			applyFirmwareDownload(view.request, view.targetKey, file.Path, file.Version)
			if parent != nil {
				result.installPlan, err = firmware.PrepareWirelessInteractiveInstall(file.Path, firmware.WirelessFirmwareSelection{ParentPID: parent.productID, ChildPID: device.productID, ChildIdentity: device.firmwareIdentity, ParentAttachment: attachment})
			} else {
				result.installPlan, err = firmware.PrepareInteractiveInstall(file.Path, device.productID, attachment)
			}
		}
		result.err = err
		sendUIResult(ctx, results, result)
	}()
}

func drawFirmwareTargets() int {
	_, _, bottom := panelBounds()
	rows := max(1, min(4, bottom-18))
	start, end := listWindow(firmwareTargetIndex, len(firmwareTargetItems), rows)
	drawListWindowHint(7, start, end, len(firmwareTargetItems))
	row := 8
	for index := start; index < end; index++ {
		number := fmt.Sprint(index + 1)
		if index == 9 {
			number = "0"
		}
		if index > 9 {
			number = "·"
		}
		device := firmwareTargetItems[index].Device
		drawLabelValue(row, number+"  "+device.deviceName, device.firmwareVersion, index == firmwareTargetIndex)
		row++
	}
	return row + 1
}
