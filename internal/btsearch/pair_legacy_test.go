package btsearch

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func legacyPairPeer() (*pairPeer, Device) {
	peer := newPairPeer(0) // Pairing namespace 0 is Classic.
	peer.scans = 0         // The discovery session already acknowledged Stop.
	found := peer.device
	found.BluetoothType = 1 // Legacy discovery flag 1 is also Classic.
	return peer, found
}

func TestLegacyBoundPairUsesClassicAndDoesNotRepeatStop(t *testing.T) {
	peer, found := legacyPairPeer()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := PairLegacyBound(ctx, peer, found); err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0x30, 4}, found.Address[:]...)
	if len(peer.writes) != 1 || !bytes.Equal(peer.writes[0], want) || peer.confirmations != 1 {
		t.Fatal("wrong Classic packet, duplicate stop/pair, or missing readback", peer.writes, peer.confirmations)
	}
}

func TestLegacyBoundPairRejectsUnknownFlagsAndMissingAddress(t *testing.T) {
	for _, flag := range []byte{0, 2, 3, 255} {
		peer, found := legacyPairPeer()
		found.BluetoothType = flag
		if err := PairLegacyBound(context.Background(), peer, found); err == nil || len(peer.writes) != 0 {
			t.Fatal("unknown legacy flag reached wire", flag, err)
		}
	}
	peer, found := legacyPairPeer()
	found.Address = [6]byte{}
	if err := PairLegacyBound(context.Background(), peer, found); err == nil || len(peer.writes) != 0 {
		t.Fatal("empty address reached wire", err)
	}
}

func TestLegacyBoundPairDoesNotReplayFailure(t *testing.T) {
	peer, found := legacyPairPeer()
	peer.pairNAK = true
	if err := PairLegacyBound(context.Background(), peer, found); err == nil || len(peer.writes) != 1 {
		t.Fatal("failed pair was accepted or repeated", peer.writes, err)
	}
	peer, found = legacyPairPeer()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := PairLegacyBound(ctx, peer, found); !errors.Is(err, context.Canceled) || len(peer.writes) != 0 {
		t.Fatal("cancelled pair reached wire", err)
	}
}

func TestLegacyBoundPairConfirmsWithoutEvents(t *testing.T) {
	peer, found := legacyPairPeer()
	peer.noEvents = true
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := PairLegacyBound(ctx, peer, found); err != nil || len(peer.writes) != 1 || peer.confirmations != 1 {
		t.Fatal("missing events caused a retry or false failure", peer.writes, peer.confirmations, err)
	}
}

func TestLegacyBoundPairAckAloneIsNotSuccess(t *testing.T) {
	peer, found := legacyPairPeer()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	peer.confirmHook = func(context.Context) (bool, error) { cancel(); return false, context.Canceled }
	if err := PairLegacyBound(ctx, peer, found); err == nil || len(peer.writes) != 1 || peer.confirmations == 0 {
		t.Fatal("ACK without requested connection was accepted", peer.writes, peer.confirmations, err)
	}
}

type legacyFailurePeer struct {
	*pairPeer
	commandError error
	replaced     bool
}

func (p *legacyFailurePeer) Command(ctx context.Context, op byte, payload []byte) error {
	if p.commandError != nil {
		p.writes = append(p.writes, append([]byte{op}, payload...))
		return p.commandError
	}
	return p.pairPeer.Command(ctx, op, payload)
}

func (p *legacyFailurePeer) Valid() error {
	if p.replaced && len(p.writes) > 0 {
		return errors.New("USB attachment changed")
	}
	return p.pairPeer.Valid()
}

func TestLegacyBoundPairDoesNotReplayTimeoutOrConfirmReplacement(t *testing.T) {
	for _, failure := range []string{"ack-timeout", "replacement"} {
		peer, found := legacyPairPeer()
		io := &legacyFailurePeer{pairPeer: peer}
		if failure == "ack-timeout" {
			io.commandError = context.DeadlineExceeded
		} else {
			io.replaced = true
		}
		if err := PairLegacyBound(context.Background(), io, found); err == nil || len(peer.writes) != 1 || peer.confirmations != 0 {
			t.Fatal("failed or replaced pair was repeated or confirmed", failure, peer.writes, peer.confirmations, err)
		}
	}
}
