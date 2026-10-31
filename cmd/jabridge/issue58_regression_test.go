package main

import "testing"

func TestIssue58Link380CDiscoveryGate(t *testing.T) {
	preservePairingOption(t)
	link380CSearchConnectEnabled.Store(false)
	if supportsValidatedPairingReads(0x24c9) {
		t.Fatal("preview enabled by default")
	}
	link380CSearchConnectEnabled.Store(true)
	if !isKnownDonglePID(0x24c9) {
		t.Fatal("reported device is not recognized")
	}
	if !supportsValidatedPairingReads(0x24c9) {
		t.Fatal("recognized Link 380c still has Find headset blocked")
	}
	if supportsExperimentalDongleWrites(0x24c9) || supportsValidatedFactoryReset(0x24c9) {
		t.Fatal("discovery accidentally enabled unrelated writes")
	}
}
