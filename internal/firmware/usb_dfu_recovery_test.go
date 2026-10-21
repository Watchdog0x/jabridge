package firmware

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDFURecoveryFinishesManifestationWithoutRewriting(t *testing.T) {
	profile, _ := usbDFUProfileForPID(0x2465)
	for _, tc := range []struct {
		name     string
		statuses []dfuStatus
		tolerant bool
	}{
		{"awaiting reset", []dfuStatus{{State: 8}}, false},
		{"manifesting", []dfuStatus{{State: 7, Poll: time.Millisecond}}, false},
		{"sync then manifesting", []dfuStatus{{State: 6}, {State: 7, Poll: time.Millisecond}}, false},
		{"tolerant manifesting", []dfuStatus{{State: 7, Poll: time.Millisecond}, {State: 2}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, version := range []string{"2.38.0", "2.32.0"} {
				original := USBDevice{VendorID: JabraVendorID, ProductID: profile.DFUPID, SysPath: "/fixture/evolve75"}
				current := original
				peer := &dfuTestTransport{statuses: append([]dfuStatus(nil), tc.statuses...)}
				resets, reads := 0, 0
				peer.reset = func() error {
					resets++
					current.ProductID = 0x2465
					return nil
				}
				intf := testDFUInterface()
				if tc.tolerant {
					intf.Attributes |= 4
				}
				backend := usbDFUBackend{
					enumerate: func() ([]USBDevice, error) { return []USBDevice{current}, nil },
					inspect:   func(USBDevice) (dfuInterface, error) { return intf, nil },
					open:      func(USBDevice) (usbDFUSession, error) { return peer, nil },
					enter: func(context.Context, USBDevice, byte) error {
						t.Fatal("recovery re-entered update mode")
						return nil
					},
					version: func(USBDevice) (string, byte, error) { reads++; return version, 8, nil },
					wait:    func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
				}
				image := &jabraDFUImage{Profile: profile, Manifest: &BuildVector{Version: "2.38.0"}, Payload: []byte{1}}
				err := runUSBDFU(context.Background(), original, 0, image, backend)
				if (err == nil) != (version == "2.38.0") || resets != 1 || reads != 1 || len(peer.writes) != 0 {
					t.Fatalf("version=%s result=%v resets=%d reads=%d writes=%d", version, err, resets, reads, len(peer.writes))
				}
				if version != "2.38.0" && !strings.Contains(err.Error(), "expected 2.38.0") {
					t.Fatal("recovery did not reject the old firmware", err)
				}
			}
		})
	}
}

func TestDFURecoveryWaitsForBusyDownloadBeforeAbort(t *testing.T) {
	for _, state := range []byte{3, 4} {
		poll := 10 * time.Millisecond
		peer := &dfuTestTransport{statuses: []dfuStatus{{State: state, Poll: poll}, {State: 5}}}
		start := time.Now()
		_, err := prepareDFUDownload(context.Background(), peer, testDFUInterface())
		if err != nil || time.Since(start) < poll || !bytes.Equal(peer.requests, []byte{3, 3, 6, 3}) || len(peer.writes) != 0 {
			t.Fatalf("state=%d result=%v requests=%v writes=%d", state, err, peer.requests, len(peer.writes))
		}
	}
}

func TestDFURecoveryBusyWaitCancellationDoesNotAbort(t *testing.T) {
	for _, state := range []byte{4, 7} {
		peer := &dfuTestTransport{statuses: []dfuStatus{{State: state, Poll: time.Minute}}}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		_, err := prepareDFUDownload(ctx, peer, testDFUInterface())
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || !bytes.Equal(peer.requests, []byte{3}) {
			t.Fatalf("state=%d result=%v requests=%v", state, err, peer.requests)
		}
	}
}

func TestDFURecoveryRejectsErrorsWhileFinishingPendingWork(t *testing.T) {
	for _, state := range []byte{3, 4, 6} {
		peer := &dfuTestTransport{statuses: []dfuStatus{{State: state}, {Code: 3, State: 10}}}
		_, err := prepareDFUDownload(context.Background(), peer, testDFUInterface())
		if err == nil || !strings.Contains(err.Error(), "DFU status 0x03") || !bytes.Equal(peer.requests, []byte{3, 3}) {
			t.Fatalf("state=%d result=%v requests=%v", state, err, peer.requests)
		}
	}
}

func TestDFUModeSwitchOtherErrorsStopBeforeDiscovery(t *testing.T) {
	profile, _ := usbDFUProfileForPID(0x2465)
	original := USBDevice{VendorID: JabraVendorID, ProductID: 0x2465, SysPath: "/fixture/evolve75"}
	image := &jabraDFUImage{Profile: profile, Manifest: &BuildVector{Version: "2.38.0"}, Payload: []byte{1}}
	for _, failure := range []error{unix.EIO, context.Canceled, context.DeadlineExceeded} {
		backend := usbDFUBackend{
			enter: func(context.Context, USBDevice, byte) error { return failure },
			enumerate: func() ([]USBDevice, error) {
				t.Fatal("continued after mode-switch failure", failure)
				return nil, nil
			},
		}
		if err := runUSBDFU(context.Background(), original, 8, image, backend); !errors.Is(err, failure) {
			t.Fatalf("failure=%v result=%v", failure, err)
		}
	}
}

func TestDFUModeSwitchTimeoutRequiresSamePortBootloader(t *testing.T) {
	profile, _ := usbDFUProfileForPID(0x2465)
	for _, appeared := range []bool{true, false} {
		original := USBDevice{VendorID: JabraVendorID, ProductID: 0x2465, SysPath: "/fixture/evolve75"}
		current := original
		peer := &dfuTestTransport{state: 2}
		enters, opens := 0, 0
		ctx, cancel := context.WithCancel(context.Background())
		peer.reset = func() error { current = original; return nil }
		backend := usbDFUBackend{
			enumerate: func() ([]USBDevice, error) {
				other := USBDevice{VendorID: JabraVendorID, ProductID: profile.DFUPID, SysPath: "/fixture/other"}
				return []USBDevice{other, current}, nil
			},
			inspect: func(USBDevice) (dfuInterface, error) { return testDFUInterface(), nil },
			open: func(device USBDevice) (usbDFUSession, error) {
				if device.SysPath != original.SysPath || device.ProductID != profile.DFUPID {
					t.Fatal("opened an unconfirmed update target")
				}
				opens++
				return peer, nil
			},
			enter: func(context.Context, USBDevice, byte) error {
				enters++
				if appeared {
					current.ProductID = profile.DFUPID
				}
				return unix.ETIMEDOUT
			},
			version: func(USBDevice) (string, byte, error) { return "2.38.0", 8, nil },
			wait: func(ctx context.Context, _ time.Duration) error {
				cancel()
				return ctx.Err()
			},
		}
		image := &jabraDFUImage{Profile: profile, Manifest: &BuildVector{Version: "2.38.0"}, Payload: []byte{1}}
		err := runUSBDFU(ctx, original, 8, image, backend)
		cancel()
		if enters != 1 {
			t.Fatal("mode switch was replayed", enters)
		}
		if appeared {
			if err != nil || opens != 1 || len(peer.writes) != 2 {
				t.Fatalf("confirmed mode switch failed: result=%v opens=%d writes=%d", err, opens, len(peer.writes))
			}
		} else if !errors.Is(err, unix.ETIMEDOUT) || !errors.Is(err, context.Canceled) || opens != 0 || len(peer.writes) != 0 {
			t.Fatalf("unconfirmed mode switch: result=%v opens=%d writes=%d", err, opens, len(peer.writes))
		}
	}
}
