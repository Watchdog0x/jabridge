package main

import "testing"

func TestIssue58Link380CPairingEnabledByDefault(t *testing.T) {
	for _, value := range []string{"", "yes", "I_ACCEPT_UNTESTED_PAIRING"} {
		t.Setenv("JABRIDGE_ENABLE_LINK380C_SEARCH_CONNECT", value)
		policy := pairingPolicyFor(&jabra_DeviceInfo{isDongle: true, productID: 0x24c9})
		if !isKnownDonglePID(0x24c9) || !policy.Reads || !policy.SearchConnect || !policy.BoundSearch || policy.Wire != searchWireLegacy {
			t.Fatalf("Link 380c pairing blocked for old option %q: %+v", value, policy)
		}
		if policy.RememberedWrites || supportsExperimentalDongleWrites(0x24c9) || supportsValidatedFactoryReset(0x24c9) {
			t.Fatal("pairing enabled unrelated writes")
		}
	}
}

func TestIssue58DirectPairingUsesServicePolicy(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, devices{0: {deviceID: 0, isDongle: true, productID: 0x24c9}}, -1, 0)
	old := currentTUIBackend()
	setTUIBackend(nil)
	t.Cleanup(func() { setTUIBackend(old) })
	if !pairingUIAllowed(deviceForID(0)) || searchPreviewIsReadOnly() || !rememberedPreviewIsReadOnly() {
		t.Fatal("direct mode must allow fresh pairing and refuse remembered-device changes")
	}
}
