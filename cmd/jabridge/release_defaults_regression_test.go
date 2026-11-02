package main

import (
	"strings"
	"testing"
)

func TestStableReleaseDefaultsDoNotEnableUnconfirmedFeatures(t *testing.T) {
	definition, ok := findBoolSettingDefinition(settingScopeHeadset, "power-saving")
	if !ok {
		t.Fatal("power-saving read definition missing")
	}
	if err := writeBoolSetting(&jabra_DeviceInfo{}, definition, true); err == nil || !strings.Contains(err.Error(), "firmware profile is unavailable") {
		t.Error("power saving wrote without a bound firmware profile", err)
	}
	if supportsValidatedPairingReads(0x24c9) {
		t.Error("untested Link 380c pairing feature enabled by default")
	}
	if supportsValidatedPairingReads(0x2e56) {
		t.Error("untested Link 390 list/search enabled by default")
	}
}
