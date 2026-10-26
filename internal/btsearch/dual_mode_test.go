package btsearch

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestLegacyDiscoveryTranscriptUnchanged(t *testing.T) {
	if !bytes.Equal(StartCommand(), []byte{0x20, 5, 20}) || !bytes.Equal(StopCommand(), []byte{0x21, 1}) {
		t.Fatal("Link 380 command transcript changed")
	}
	if !bytes.Equal(DualModeStartCommand(), []byte{0x20, 7, 20}) || !bytes.Equal(DualModeStopCommand(), []byte{0x21, 3}) {
		t.Fatal("Link 390 masks differ from vendor source")
	}
}

func TestDualDiscoveryPreservesBothModesAtOneAddress(t *testing.T) {
	results := Results{SeparateTypes: true}
	for _, flags := range []byte{1, 2, 1} {
		packet := found("Headset", 1)
		packet[7] = flags
		event, relevant, err := DecodeDualMode(packet, 1)
		if err != nil || !relevant || event.Device.BluetoothType != flags-1 {
			t.Fatal(event, relevant, err)
		}
		if err := results.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	if len(results.Devices) != 2 {
		t.Fatal("connection choices were merged", results.Devices)
	}
}

func TestDualDiscoveryRejectsUnknownTypesAndBadNames(t *testing.T) {
	for _, flags := range []byte{0, 3, 8, 255} {
		packet := found("Headset", 1)
		packet[7] = flags
		if _, relevant, err := DecodeDualMode(packet, 1); err != nil || relevant {
			t.Fatal(flags, relevant, err)
		}
	}
	packet := found("Headset", 1)
	packet[7] = 1
	packet[14] = 255
	if _, _, err := DecodeDualMode(packet, 1); err == nil {
		t.Fatal("invalid name size accepted")
	}
	packet[14] = 0
	if _, relevant, err := DecodeDualMode(packet, 1); err != nil || relevant {
		t.Fatal("empty incomplete name published", err)
	}
}

func TestCompletedDualSearchStillStopsBothBeforePair(t *testing.T) {
	transport := &testIO{packets: make(chan []byte, 2)}
	packet := found("Headset", 6)
	packet[7] = 2
	transport.packets <- packet
	transport.packets <- []byte{5, 0, 1, 0, 6, 0x0d, 0x23}
	s, err := StartDualMode(context.Background(), transport, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	<-s.Done()
	if err := s.Stop(); err != nil || transport.stops.Load() != 0 {
		t.Fatal(err, transport.stops.Load())
	}
	peer := newPairPeer(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := PairDualMode(ctx, peer, peer.device); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(peer.writes[0], []byte{0x21, 3}) {
		t.Fatal("completed search skipped explicit stop", peer.writes)
	}
}

func FuzzDualDiscovery(f *testing.F) {
	packet := found("Headset", 1)
	packet[7] = 2
	f.Add(packet)
	f.Add([]byte{5, 0, 1, 0, 6, 0x0d, 0x23})
	f.Fuzz(func(t *testing.T, packet []byte) {
		event, relevant, err := DecodeDualMode(packet, 1)
		if err == nil && relevant && !event.Complete {
			if event.Device.BluetoothType > 1 || event.Device.Address == [6]byte{} {
				t.Fatal("invalid usable discovery", event)
			}
			if _, err := PairCommand(event.Device); err != nil {
				t.Fatal(err)
			}
		}
	})
}
