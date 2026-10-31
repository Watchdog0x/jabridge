package main

import (
	"os"
	"sync/atomic"
)

const (
	previewSettingWritesEnv      = "JABRIDGE_ENABLE_PREVIEW_SETTING_WRITES"
	previewSettingWritesAck      = "I_ACCEPT_UNTESTED_SETTINGS"
	previewSettingReadOnlyReason = "Read-only in this release; changes are not yet confirmed on hardware."
)

var previewSettingWritesEnabled atomic.Bool

// Capture only in the daemon. A client's environment never grants permission
// to the service, and the ordinary experimental-writes option is independent.
func capturePreviewSettingOption() {
	previewSettingWritesEnabled.Store(os.Getenv(previewSettingWritesEnv) == previewSettingWritesAck)
}

func previewSettingLocked(key string, structured bool) bool {
	preview := structured || key == "power-saving" || key == "noise-cycle" || isEqualizerBand(key)
	return preview && !previewSettingWritesEnabled.Load()
}

func applyPreviewChoicePolicy(definition choiceSettingDefinition) choiceSettingDefinition {
	if previewSettingLocked(definition.Key, definition.Codec != nil) {
		definition.Writable = false
		definition.Help = previewSettingReadOnlyReason
	}
	return definition
}
