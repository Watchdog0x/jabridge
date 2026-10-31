package btsearch

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// The peer models documented search-mask and typed-pair semantics only. It
// does not claim that a particular scan mask caused the tester's hardware fault.
// A stale unnamed entry becomes connected only after a correctly typed pair.
type pairPeer struct {
	confirmHook                                  func(context.Context) (bool, error)
	device                                       Device
	scans                                        byte
	writes                                       [][]byte
	packets                                      chan []byte
	connected                                    bool
	stopNAK, pairNAK, replaceAfterPair, noEvents bool
	confirmations                                int
	stopHook                                     func()
}

func newPairPeer(kind byte) *pairPeer {
	return &pairPeer{device: Device{Name: "Headset", Address: [6]byte{1, 2, 3, 4, 5, 6}, BluetoothType: kind}, scans: 3, packets: make(chan []byte, 3)}
}

func (p *pairPeer) Valid() error {
	if p.replaceAfterPair && len(p.writes) > 1 {
		return errors.New("USB attachment changed")
	}
	return nil
}

func (p *pairPeer) Command(ctx context.Context, op byte, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.writes = append(p.writes, append([]byte{op}, payload...))
	switch op {
	case 0x21:
		if p.stopNAK {
			return errors.New("stop NAK")
		}
		if len(payload) != 1 {
			return errors.New("bad stop")
		}
		p.scans &^= payload[0]
		if p.stopHook != nil {
			p.stopHook()
		}
	case 0x30:
		if p.pairNAK {
			return errors.New("pair NAK")
		}
		if p.scans != 0 {
			return errors.New("discovery still active")
		}
		if len(payload) != 7 || payload[0] != 4+p.device.BluetoothType || !bytes.Equal(payload[1:], p.device.Address[:]) {
			return errors.New("wrong address or connection type")
		}
		p.connected = true
		if !p.noEvents {
			p.packets <- []byte{5, 0, 1, 0, 6, 0x0d, 1}
			p.packets <- []byte{5, 0, 1, 0, 6, 2, 4}
		}
	default:
		return errors.New("unexpected write")
	}
	return nil
}

func (p *pairPeer) Read(ctx context.Context) ([]byte, error) {
	select {
	case packet := <-p.packets:
		return packet, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *pairPeer) Confirm(ctx context.Context, device Device) (bool, error) {
	p.confirmations++
	if p.confirmHook != nil {
		return p.confirmHook(ctx)
	}
	return p.connected && device.Address == p.device.Address && device.BluetoothType == p.device.BluetoothType, ctx.Err()
}

func TestDualPairClearsBothScansAndPreservesSelectedType(t *testing.T) {
	for _, kind := range []byte{0, 1} {
		peer := newPairPeer(kind)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := PairDualMode(ctx, peer, peer.device)
		cancel()
		if err != nil || peer.confirmations != 1 || len(peer.writes) != 2 {
			t.Fatalf("kind=%d writes=%x err=%v", kind, peer.writes, err)
		}
		if !bytes.Equal(peer.writes[0], []byte{0x21, 3}) || peer.writes[1][1] != 4+kind {
			t.Fatal(peer.writes)
		}
	}
}

func TestStableClassicStopCannotStopBothNamespaces(t *testing.T) {
	peer := newPairPeer(1)
	if err := peer.Command(context.Background(), 0x21, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if peer.scans != 2 {
		t.Fatal("fake did not preserve LE scan bit")
	}
	if err := peer.Command(context.Background(), 0x30, append([]byte{4}, peer.device.Address[:]...)); err == nil {
		t.Fatal("peer accepted the old classic-only stop/pair transcript")
	}
}

func TestDualPairRefusesUnsupportedTypeBeforeWrites(t *testing.T) {
	peer := newPairPeer(2)
	if err := PairDualMode(context.Background(), peer, peer.device); err == nil || len(peer.writes) != 0 {
		t.Fatal(err, peer.writes)
	}
}

func TestDualPairDoesNotReplayAfterFailure(t *testing.T) {
	for _, kind := range []string{"stop", "pair", "replacement", "cancel"} {
		peer := newPairPeer(1)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		switch kind {
		case "stop":
			peer.stopNAK = true
		case "pair":
			peer.pairNAK = true
		case "replacement":
			peer.replaceAfterPair = true
		case "cancel":
			peer.stopHook = cancel
		}
		err := PairDualMode(ctx, peer, peer.device)
		cancel()
		if err == nil {
			t.Fatal("failure hidden", kind)
		}
		want := 2
		if kind == "stop" || kind == "cancel" {
			want = 1
		}
		if len(peer.writes) != want || peer.confirmations != 0 {
			t.Fatal(kind, peer.writes, peer.confirmations)
		}
	}
}

func TestDualPairMissingEventsUsesFreshReadback(t *testing.T) {
	peer := newPairPeer(1)
	peer.noEvents = true
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := PairDualMode(ctx, peer, peer.device); err != nil {
		t.Fatal(err)
	}
	if peer.confirmations != 1 || len(peer.writes) != 2 {
		t.Fatal(peer.confirmations, peer.writes)
	}
}

func TestDualPairCancellationBeforeStartSendsNothing(t *testing.T) {
	peer := newPairPeer(0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := PairDualMode(ctx, peer, peer.device); !errors.Is(err, context.Canceled) || len(peer.writes) != 0 {
		t.Fatal(err, peer.writes)
	}
}

func TestDualPairBoundsWholeConfirmationAndRejectsLateSuccess(t *testing.T) {
	peer := newPairPeer(1)
	outer, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	outerEnd, _ := outer.Deadline()
	peer.confirmHook = func(ctx context.Context) (bool, error) {
		end, ok := ctx.Deadline()
		if !ok || time.Until(end) > time.Second || !end.Before(outerEnd) {
			t.Fatal("confirmation did not receive its own one-second deadline")
		}
		if peer.confirmations == 1 {
			for query := 0; query < 2; query++ {
				got, _ := ctx.Deadline()
				if got != end {
					t.Fatal("query renewed the deadline")
				}
			}
			<-ctx.Done()
			peer.packets <- []byte{5, 0, 1, 0, 6, 0x0d, 1}
			peer.packets <- []byte{5, 0, 1, 0, 6, 2, 4}
			return true, nil // Expired success must be rejected.
		}
		return true, nil
	}
	if err := PairDualMode(outer, peer, peer.device); err != nil {
		t.Fatal(err)
	}
	if peer.confirmations != 2 || len(peer.writes) != 2 {
		t.Fatal("timeout accepted or command replayed", peer.confirmations, peer.writes)
	}
	if end, _ := outer.Deadline(); end != outerEnd {
		t.Fatal("outer deadline moved")
	}
}

func TestDualPairShorterOuterDeadlineWins(t *testing.T) {
	peer := newPairPeer(0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	want, _ := ctx.Deadline()
	peer.confirmHook = func(attempt context.Context) (bool, error) {
		if got, _ := attempt.Deadline(); got != want {
			t.Fatal("attempt exceeded outer deadline")
		}
		<-attempt.Done()
		return true, nil
	}
	if err := PairDualMode(ctx, peer, peer.device); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("late success accepted", err)
	}
	if peer.confirmations != 1 || len(peer.writes) != 2 {
		t.Fatal(peer.confirmations, peer.writes)
	}
}
