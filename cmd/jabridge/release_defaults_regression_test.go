package main

import "testing"

func TestStableReleaseDefaultsDoNotEnableUnconfirmedFeatures(t *testing.T) {
	definition, ok := findBoolSettingDefinition(settingScopeHeadset, "power-saving")
	if !ok {
		t.Fatal("power-saving read definition missing")
	}
	if canEditBoolSetting(&jabra_DeviceInfo{}, definition) {
		t.Error("power saving writes enabled by default")
	}
	if supportsValidatedPairingReads(0x24c9) {
		t.Error("untested Link 380c pairing feature enabled by default")
	}
	if supportsValidatedPairingReads(0x2e56) {
		t.Error("untested Link 390 list/search enabled by default")
	}
}
