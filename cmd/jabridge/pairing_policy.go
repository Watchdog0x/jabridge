package main

type searchWire int

const (
	searchWireLegacy searchWire = iota
	searchWireDualMode
)

type pairingPolicy struct {
	Reads, SearchConnect, RememberedWrites, BoundSearch bool
	Wire                                                searchWire
}

func pairingPolicyFor(device *jabra_DeviceInfo) pairingPolicy {
	if device == nil || !device.isDongle {
		return pairingPolicy{}
	}
	switch device.productID {
	case 0x24c7, 0x24c8:
		return pairingPolicy{Reads: true, SearchConnect: true, RememberedWrites: true}
	case 0x24c9:
		return pairingPolicy{Reads: true, SearchConnect: true, BoundSearch: true}
	case 0x2e57:
		return pairingPolicy{Reads: true, SearchConnect: true, RememberedWrites: true, BoundSearch: true, Wire: searchWireDualMode}
	case 0x2e56:
		return pairingPolicy{BoundSearch: true, Wire: searchWireDualMode}
	}
	return pairingPolicy{}
}

// Recognition, pairing reads, fresh pairing and remembered-device writes are
// separate permissions. Fresh pairing never grants the other writes.
func supportsValidatedPairingReads(pid uint16) bool {
	return pairingPolicyFor(&jabra_DeviceInfo{isDongle: true, productID: pid}).Reads
}

func supportsExperimentalDongleWrites(pid uint16) bool {
	return pairingPolicyFor(&jabra_DeviceInfo{isDongle: true, productID: pid}).RememberedWrites
}

// Reset has its own hardware qualification and is never granted by discovery
// or fresh-pairing support.
func supportsValidatedFactoryReset(pid uint16) bool {
	return pid == 0x24c7 || pid == 0x24c8
}
