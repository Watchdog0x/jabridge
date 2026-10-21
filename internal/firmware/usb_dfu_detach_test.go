package firmware

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
)

// Model a reset delayed by runtime drivers without making the test sleep.
// The physical Evolve 75 advertises 5000 ms; its reset took about 2500 ms.
type dfuDetachPeer struct {
	intf      dfuInterface
	state     byte
	timeoutMS uint16
	resetMS   uint16
	resets    int
	detaches  int
}

func (p *dfuDetachPeer) Control(_ context.Context, kind, request byte, value, index uint16, data []byte) (int, error) {
	if index != uint16(p.intf.Number) {
		return 0, errors.New("wrong DFU interface")
	}
	if kind == 0xa1 && request == 3 {
		copy(data, []byte{0, 0, 0, 0, p.state, 0})
		return 6, nil
	}
	if kind == 0x21 && request == 0 && len(data) == 0 {
		p.detaches++
		if value > p.intf.DetachMS {
			return 0, errors.New("requested detach timeout exceeds descriptor maximum")
		}
		p.timeoutMS, p.state = value, 1
		return 0, nil
	}
	return 0, errors.New("unexpected request; no firmware writes allowed")
}

func (p *dfuDetachPeer) Reset() error {
	p.resets++
	p.state = 0 // An expired detach window boots the original runtime.
	if p.resetMS <= p.timeoutMS {
		p.state = 2
	}
	return nil
}

func TestDFURuntimeDetachUsesAdvertisedResetWindow(t *testing.T) {
	for _, tc := range []struct {
		name           string
		maximum, delay uint16
		selfDetach     bool
	}{
		{"Evolve 75 slow reset", 5000, 2600, false},
		{"short maximum", 250, 100, false},
		{"zero maximum", 0, 0, false},
		{"self detaching", 5000, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intf := testDFUInterface()
			intf.DetachMS = tc.maximum
			if tc.selfDetach {
				intf.Attributes |= 8
			}
			peer := &dfuDetachPeer{intf: intf, resetMS: tc.delay}
			preparation, err := prepareDFUDownload(context.Background(), peer, intf)
			if err != nil || preparation != dfuPreparationReopen || peer.detaches != 1 || peer.timeoutMS != tc.maximum {
				t.Fatalf("preparation=%d error=%v detaches=%d timeout=%d", preparation, err, peer.detaches, peer.timeoutMS)
			}
			if tc.selfDetach {
				if peer.resets != 0 {
					t.Fatal("reset a self-detaching device")
				}
			} else if peer.resets != 1 || peer.state != 2 {
				t.Fatalf("reset missed detach window: resets=%d state=%d", peer.resets, peer.state)
			}
		})
	}
}

func TestEvolve75DFURuntimeDescriptor(t *testing.T) {
	// Physical 0b0e:097a on firmware 2.32.0, before DFU_DETACH. This
	// composite Bluetooth runtime becomes a single DFU interface after reset.
	descriptors, err := hex.DecodeString("12010002e00101400e0b7a093202000200010902c300030100c0320904000003e00101000705810310000107050202400001070582024000010904010002e001010007050301000001070583010000010904010102e001010007050301090001070583010900010904010202e001010007050301110001070583011100010904010302e001010007050301190001070583011900010904010402e001010007050301210001070583012100010904010502e001010007050301310001070583013100010904020000fe0101000921078813ff030101")
	if err != nil {
		t.Fatal(err)
	}
	intf, err := parseDFUInterface(descriptors, 1)
	wanted := dfuInterface{Number: 2, Protocol: 1, Attributes: 7, DetachMS: 5000, TransferSize: 1023, Version: 0x0101}
	if err != nil || intf != wanted {
		t.Fatalf("interface=%+v error=%v", intf, err)
	}
}
