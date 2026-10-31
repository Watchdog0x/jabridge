package main

func rememberedPreviewIsReadOnly() bool {
	dongle, exists := selectedDongleSnapshot()
	return exists && !supportsExperimentalDongleWrites(dongle.productID)
}

// New clients use the daemon's advertised fresh-pairing permission. Existing
// qualified variants keep working with older services that omit that field.
func searchPreviewIsReadOnly() bool {
	dongle, exists := selectedDongleSnapshot()
	return exists && !supportsExperimentalDongleWrites(dongle.productID) && !dongle.searchConnect
}

// Service permission is display data here, never backend authorization. The
// existing 24c9 option grants its reads and search as well as fresh pairing.
func pairingUIAllowed(device *jabra_DeviceInfo) bool {
	return device != nil && device.isDongle && (supportsValidatedPairingReads(device.productID) || device.productID == 0x24c9 && device.searchConnect)
}
