package main

func rememberedPreviewIsReadOnly() bool {
	dongle, exists := selectedDongleSnapshot()
	return exists && !supportsExperimentalDongleWrites(dongle.productID)
}

// The service owns pairing permission. Direct mode uses the same policy table.
func dongleSearchConnectAllowed(device *jabra_DeviceInfo) bool {
	if device == nil || !device.isDongle {
		return false
	}
	if currentTUIBackend() != nil {
		return device.searchConnect
	}
	return pairingPolicyFor(device).SearchConnect
}

// Existing writable variants keep working with older services that omit the
// fresh-pairing permission. Link 380c requires the service's advertised value.
func searchPreviewIsReadOnly() bool {
	dongle, exists := selectedDongleSnapshot()
	return exists && !supportsExperimentalDongleWrites(dongle.productID) && !dongleSearchConnectAllowed(dongle)
}

// Service permission is display data here, never backend authorization.
func pairingUIAllowed(device *jabra_DeviceInfo) bool {
	if device == nil || !device.isDongle {
		return false
	}
	if device.productID == 0x24c9 {
		return dongleSearchConnectAllowed(device)
	}
	return supportsValidatedPairingReads(device.productID)
}
