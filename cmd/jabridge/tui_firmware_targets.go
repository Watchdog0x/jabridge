package main

import "fmt"

var firmwareRequestCounter uint64 // protected by firmwareViewMu

func firmwareTargetBinding(target switchDeviceItem) string {
	if target.Device == nil {
		return ""
	}
	return fmt.Sprintf("%d:%04x:%s:%s", target.RegistryID, target.Device.productID, target.Device.instance, target.Device.controlTopology)
}

func beginFirmwareView(target switchDeviceItem) (uint64, string) {
	key := firmwareTargetBinding(target)
	firmwareViewMu.Lock()
	defer firmwareViewMu.Unlock()
	firmwareRequestCounter++
	firmwareView = firmwareViewState{request: firmwareRequestCounter, targetKey: key, targetRegistryID: target.RegistryID, targetPID: target.Device.productID, loading: true, deviceName: target.Device.deviceName}
	return firmwareRequestCounter, key
}

func firmwareRequestMatches(request uint64, key string) bool {
	return firmwareView.request == request && firmwareView.targetKey == key
}

func applyFirmwareInfo(request uint64, key, current, latest string, currentErr error) bool {
	firmwareViewMu.Lock()
	defer firmwareViewMu.Unlock()
	if !firmwareRequestMatches(request, key) {
		return false
	}
	firmwareView.loading = false
	firmwareView.currentVersion = current
	firmwareView.latestVersion = latest
	if firmwareView.downloadedVersion != "" {
		firmwareView.latestVersion = firmwareView.downloadedVersion
	}
	firmwareView.currentError = ""
	if currentErr != nil {
		firmwareView.currentError = currentErr.Error()
	}
	return true
}

func applyFirmwareDownload(request uint64, key, path, version string) bool {
	firmwareViewMu.Lock()
	defer firmwareViewMu.Unlock()
	if !firmwareRequestMatches(request, key) {
		return false
	}
	firmwareView.downloadedPath = path
	firmwareView.downloadedVersion = version
	if version != "" {
		firmwareView.latestVersion = version
	}
	return true
}

func withFirmwareRequest(request uint64) actionOption {
	return func(result *actionResult) { result.firmwareRequest = request }
}

func ensureFirmwareView(results chan<- actionResult) {
	if menuState != screenFirmware {
		return
	}
	if uiBusy() {
		return
	}
	refreshFirmwareTargets()
	target, exists := selectedFirmwareTarget()
	firmwareViewMu.RLock()
	view := firmwareView
	firmwareViewMu.RUnlock()
	if !exists {
		if view.targetKey != "" {
			firmwareViewMu.Lock()
			firmwareView = firmwareViewState{}
			firmwareViewMu.Unlock()
			requestUIRedraw()
		}
		return
	}
	if view.targetKey != firmwareTargetBinding(target) {
		refreshFirmwareView(results)
		return
	}
	if !view.loading && target.Device.firmwareVersion != "" && target.Device.firmwareVersion != view.currentVersion {
		firmwareViewMu.Lock()
		firmwareView.currentVersion = target.Device.firmwareVersion
		firmwareView.currentError = ""
		firmwareViewMu.Unlock()
		requestUIRedraw()
	}
}
