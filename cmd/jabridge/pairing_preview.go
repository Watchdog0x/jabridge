package main

func pairingPreviewIsReadOnly() bool {
	dongle, exists := selectedDongleSnapshot()
	return exists && supportsValidatedPairingReads(dongle.productID) && !supportsExperimentalDongleWrites(dongle.productID)
}
