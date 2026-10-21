package firmware

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A non-tolerant DFU device cannot answer another class request after the
// manifestation poll interval. USB DFU 1.1, A.2.8 and A.2.9 require a reset.
type dfuManifestPeer struct {
	dfuTestTransport
	poll          time.Duration
	tolerant      bool
	syncFirst     bool
	manifestReads int
	readyAt       time.Time
}

func (p *dfuManifestPeer) Control(ctx context.Context, kind, request byte, value, index uint16, data []byte) (int, error) {
	if kind == 0xa1 && request == 3 && len(p.writes) > 0 && len(p.writes[len(p.writes)-1]) == 0 {
		p.manifestReads++
		manifestRead := 1
		if p.syncFirst {
			manifestRead = 2
		}
		if p.manifestReads < manifestRead {
			p.statuses = []dfuStatus{{State: 6}}
		} else if p.manifestReads == manifestRead {
			p.readyAt = time.Now().Add(p.poll)
			p.statuses = []dfuStatus{{State: 7, Poll: p.poll}}
		} else {
			if !p.tolerant || time.Now().Before(p.readyAt) {
				return 0, unix.EPIPE
			}
			p.statuses = []dfuStatus{{State: 2}}
		}
	}
	return p.dfuTestTransport.Control(ctx, kind, request, value, index, data)
}

func TestDFUManifestationHonorsToleranceAndPollInterval(t *testing.T) {
	for _, tc := range []struct {
		name                string
		tolerant, syncFirst bool
	}{
		{"requires reset", false, false},
		{"sync then requires reset", false, true},
		{"tolerant", true, false},
		{"sync then tolerant", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := &dfuManifestPeer{poll: 10 * time.Millisecond, tolerant: tc.tolerant, syncFirst: tc.syncFirst}
			intf := testDFUInterface()
			if tc.tolerant {
				intf.Attributes |= 4
			}
			if err := transferDFU(context.Background(), peer, intf, []byte{1}, nil); err != nil {
				t.Fatal(err)
			}
			if time.Now().Before(peer.readyAt) {
				t.Fatal("returned for reset before the device's manifestation interval elapsed")
			}
			wantReads := 1
			if tc.tolerant {
				wantReads++
			}
			if tc.syncFirst {
				wantReads++
			}
			if peer.manifestReads != wantReads || len(peer.writes) != 2 {
				t.Fatalf("manifest reads=%d, writes=%d", peer.manifestReads, len(peer.writes))
			}
		})
	}
}

func TestDFUManifestationNeedsSuccessfulStatusBeforeReset(t *testing.T) {
	for _, peer := range []*dfuTestTransport{
		{finalStatusErrors: []error{unix.EPIPE}},
		{statuses: []dfuStatus{{State: 5}, {Code: 3, State: 7}}},
	} {
		if err := transferDFU(context.Background(), peer, testDFUInterface(), []byte{1}, nil); err == nil {
			t.Fatal("failed final status accepted as ready for reset")
		}
		if len(peer.writes) != 2 {
			t.Fatal("firmware was replayed")
		}
	}
}

func TestDFUManifestationWaitRemainsCancellable(t *testing.T) {
	peer := &dfuManifestPeer{poll: time.Minute}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := transferDFU(ctx, peer, testDFUInterface(), []byte{1}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || peer.manifestReads != 1 || len(peer.writes) != 2 {
		t.Fatalf("result=%v, reads=%d, writes=%d", err, peer.manifestReads, len(peer.writes))
	}
}

func TestDFUNonTolerantManifestationStillVerifiesRuntimeVersion(t *testing.T) {
	profile, _ := usbDFUProfileForPID(0x2465)
	for _, version := range []string{"2.38.0", "2.32.0"} {
		t.Run(version, func(t *testing.T) {
			original := USBDevice{VendorID: JabraVendorID, ProductID: 0x2465, SysPath: "/fixture/evolve75", Serial: "fixture"}
			current := original
			peer := &dfuManifestPeer{dfuTestTransport: dfuTestTransport{state: 2}, poll: time.Millisecond}
			resets, versions := 0, 0
			peer.reset = func() error {
				if peer.manifestReads != 1 || time.Now().Before(peer.readyAt) {
					t.Fatal("reset before manifestation finished")
				}
				resets++
				current = original
				return unix.ENODEV // Reset disconnected the old USB attachment.
			}
			backend := usbDFUBackend{
				enumerate: func() ([]USBDevice, error) { return []USBDevice{current}, nil },
				inspect:   func(USBDevice) (dfuInterface, error) { return testDFUInterface(), nil },
				open:      func(USBDevice) (usbDFUSession, error) { return peer, nil },
				enter: func(context.Context, USBDevice, byte) error {
					current.ProductID = profile.DFUPID
					return nil
				},
				version: func(USBDevice) (string, byte, error) {
					versions++
					return version, 8, nil
				},
				wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
			}
			image := &jabraDFUImage{Profile: profile, Manifest: &BuildVector{Version: "2.38.0"}, Payload: []byte{1}}
			err := runUSBDFU(context.Background(), original, 8, image, backend)
			if (err == nil) != (version == "2.38.0") || resets != 1 || versions != 1 || len(peer.writes) != 2 {
				t.Fatalf("result=%v, resets=%d, version reads=%d, writes=%d", err, resets, versions, len(peer.writes))
			}
		})
	}
}
