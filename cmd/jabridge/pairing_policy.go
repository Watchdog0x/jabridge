package main

import (
	"os"
	"sync/atomic"
)

const (
	link380CSearchConnectEnv = "JABRIDGE_ENABLE_LINK380C_SEARCH_CONNECT"
	link380CSearchConnectAck = "I_ACCEPT_UNTESTED_PAIRING"
)

var link380CSearchConnectEnabled atomic.Bool

// Only the daemon captures this option, before starting device workers. The
// terminal client receives its effective permission through devices.list.
func capturePairingPreviewOption() {
	link380CSearchConnectEnabled.Store(os.Getenv(link380CSearchConnectEnv) == link380CSearchConnectAck)
}

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
		return pairingPolicy{Reads: true, SearchConnect: link380CSearchConnectEnabled.Load(), BoundSearch: true}
	case 0x2e57:
		return pairingPolicy{Reads: true, SearchConnect: true, RememberedWrites: true, BoundSearch: true, Wire: searchWireDualMode}
	case 0x2e56:
		return pairingPolicy{Reads: true, BoundSearch: true, Wire: searchWireDualMode}
	}
	return pairingPolicy{}
}

// Recognition, pairing reads, fresh pairing and remembered-device writes are
// separate permissions. A preview for one never grants the others.
func supportsValidatedPairingReads(pid uint16) bool {
	return pairingPolicyFor(&jabra_DeviceInfo{isDongle: true, productID: pid}).Reads
}

func supportsExperimentalDongleWrites(pid uint16) bool {
	return pairingPolicyFor(&jabra_DeviceInfo{isDongle: true, productID: pid}).RememberedWrites
}

// Reset has its own hardware qualification and is never granted by discovery
// or the fresh-pairing preview option.
func supportsValidatedFactoryReset(pid uint16) bool {
	return pid == 0x24c7 || pid == 0x24c8
}
