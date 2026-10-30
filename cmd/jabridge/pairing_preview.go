package main

func rememberedPreviewIsReadOnly() bool {
	dongle, exists := selectedDongleSnapshot()
	return exists && supportsValidatedPairingReads(dongle.productID) && !supportsExperimentalDongleWrites(dongle.productID)
}

// New clients use the daemon's advertised fresh-pairing permission. Existing
// qualified variants keep working with older services that omit that field.
func searchPreviewIsReadOnly() bool {
	dongle, exists := selectedDongleSnapshot()
	return exists && supportsValidatedPairingReads(dongle.productID) && !supportsExperimentalDongleWrites(dongle.productID) && !dongle.searchConnect
}
