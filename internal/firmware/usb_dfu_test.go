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

type dfuTestTransport struct {
	statuses []dfuStatus
	writes   [][]byte
	blocks   []uint16
	requests []byte
	short    bool
	err      error
	state    byte
	reset    func() error
}

func (f *dfuTestTransport) Control(_ context.Context, kind, request byte, value, index uint16, data []byte) (int, error) {
	if index != 3 {
		return 0, errors.New("wrong interface")
	}
	f.requests = append(f.requests, request)
	if f.err != nil {
		return 0, f.err
	}
	if kind == 0xa1 && request == 3 {
		status := dfuStatus{State: f.state}
		if len(f.statuses) > 0 {
			status, f.statuses = f.statuses[0], f.statuses[1:]
		}
		poll := uint32(status.Poll.Milliseconds())
		copy(data, []byte{status.Code, byte(poll), byte(poll >> 8), byte(poll >> 16), status.State, 0})
		if f.short {
			return 5, nil
		}
		return 6, nil
	}
	if kind != 0x21 {
		return 0, errors.New("wrong USB request direction")
	}
	switch request {
	case 0:
		if value != 1000 {
			return 0, errors.New("wrong detach timeout")
		}
		f.state = 1
	case 1:
		f.writes = append(f.writes, append([]byte(nil), data...))
		f.blocks = append(f.blocks, value)
		f.state = 5
		if len(data) == 0 {
			f.state = 8
		}
		if f.short && len(data) > 0 {
			return len(data) - 1, nil
		}
	case 4, 6:
		f.state = 2
	default:
		return 0, errors.New("unexpected DFU request")
	}
	return len(data), nil
}

func (f *dfuTestTransport) Reset() error {
	if f.reset != nil {
		return f.reset()
	}
	f.state = 2
	return nil
}
func (f *dfuTestTransport) Claim(byte, bool) error { return nil }
func (f *dfuTestTransport) Close() error           { return nil }

func testDFUInterface() dfuInterface {
	return dfuInterface{Number: 3, Attributes: 1, Protocol: 2, TransferSize: 64, Version: 0x0100}
}

func TestDFUTransfersBoundedBlocksAndZeroLengthEOF(t *testing.T) {
	for _, size := range []int{1, 63, 64, 65, 128, 1792} {
		f := &dfuTestTransport{}
		payload := bytes.Repeat([]byte{0x51}, size)
		var progress []int
		if err := transferDFU(context.Background(), f, testDFUInterface(), payload, func(p int) { progress = append(progress, p) }); err != nil {
			t.Fatal(err)
		}
		if got := bytes.Join(f.writes, nil); !bytes.Equal(got, payload) {
			t.Fatal("firmware bytes changed")
		}
		if len(f.writes[len(f.writes)-1]) != 0 || progress[len(progress)-1] != 100 {
			t.Fatal("missing EOF or progress")
		}
		for index, block := range f.blocks {
			if block != uint16(index) || len(f.writes[index]) > 64 {
				t.Fatal("bad DFU block sequence")
			}
		}
	}
}

func TestDFUFailureStopsBeforeFollowingBlock(t *testing.T) {
	for _, test := range []struct {
		name string
		f    *dfuTestTransport
	}{
		{"short write", &dfuTestTransport{short: true}},
		{"device error", &dfuTestTransport{statuses: []dfuStatus{{Code: 3, State: 10}}}},
		{"unexpected state", &dfuTestTransport{statuses: []dfuStatus{{State: 0}}}},
		{"transport error", &dfuTestTransport{err: unix.EIO}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := transferDFU(context.Background(), test.f, testDFUInterface(), make([]byte, 129), nil); err == nil {
				t.Fatal("accepted failed transfer")
			}
			if len(test.f.writes) > 1 {
				t.Fatal("continued writing after error")
			}
		})
	}
}

func TestDFUPollTimeoutIsRespectedAndCancellable(t *testing.T) {
	f := &dfuTestTransport{statuses: []dfuStatus{{State: 4, Poll: 60 * time.Second}}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := transferDFU(ctx, f, testDFUInterface(), []byte{1}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second || len(f.writes) != 1 {
		t.Fatalf("cancellation: %v, writes=%d", err, len(f.writes))
	}
}

func TestDFURejectsOverflowAndCancelledTransferBeforeWrite(t *testing.T) {
	f := &dfuTestTransport{}
	intf := testDFUInterface()
	intf.TransferSize = 1
	if err := transferDFU(context.Background(), f, intf, make([]byte, 65536), nil); err == nil || len(f.requests) != 0 {
		t.Fatal("block overflow reached device")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := transferDFU(ctx, f, intf, []byte{1}, nil); !errors.Is(err, context.Canceled) || len(f.requests) != 0 {
		t.Fatal("cancelled operation reached device")
	}
}

func TestDFURecoveryClearsErrorAndAbortsInterruptedTransfer(t *testing.T) {
	for _, state := range []byte{2, 5, 9, 10} {
		f := &dfuTestTransport{state: state}
		detached, err := prepareDFUDownload(context.Background(), f, testDFUInterface())
		if err != nil || detached || f.state != 2 {
			t.Fatalf("state %d: detached=%t err=%v state=%d", state, detached, err, f.state)
		}
		if state == 10 && !bytes.Contains(f.requests, []byte{4}) {
			t.Fatal("DFU error not cleared")
		}
		if (state == 5 || state == 9) && !bytes.Contains(f.requests, []byte{6}) {
			t.Fatal("interrupted transfer not aborted")
		}
	}
	f := &dfuTestTransport{short: true}
	if _, err := prepareDFUDownload(context.Background(), f, testDFUInterface()); err == nil {
		t.Fatal("accepted short status")
	}
}

func TestDFURuntimeDetachRequiresReopenBeforeDownload(t *testing.T) {
	f := &dfuTestTransport{state: 0}
	detached, err := prepareDFUDownload(context.Background(), f, testDFUInterface())
	if err != nil || !detached || len(f.writes) != 0 || f.state != 2 {
		t.Fatalf("detach=%t err=%v state=%d", detached, err, f.state)
	}
}

func TestDFUSamePortCoordinatorAndVersionVerification(t *testing.T) {
	for _, profile := range usbDFUProfiles {
		t.Run(profile.Name, func(t *testing.T) {
			for _, version := range []string{"1.2.3", "1.2.2"} {
				for _, recover := range []bool{false, true} {
					original := USBDevice{VendorID: JabraVendorID, ProductID: profile.RuntimePIDs[0], SysPath: "/sys/bus/usb/devices/1-2.3"}
					if recover {
						original.ProductID = profile.DFUPID
					}
					current := original
					f := &dfuTestTransport{state: 0}
					if recover {
						f.state = 10
					}
					enters, opens := 0, 0
					f.reset = func() error {
						switch f.state {
						case 1:
							f.state = 2
						case 8:
							current.ProductID, f.state = profile.RuntimePIDs[0], 0
						default:
							t.Fatal("unexpected reset")
						}
						return nil
					}
					backend := usbDFUBackend{
						enumerate: func() ([]USBDevice, error) {
							other := current
							other.SysPath = "/sys/bus/usb/devices/9-9"
							return []USBDevice{other, current}, nil
						},
						open: func(d USBDevice) (usbDFUSession, error) {
							if d.SysPath != original.SysPath || d.ProductID != profile.DFUPID {
								t.Fatal("opened the wrong device")
							}
							opens++
							return f, nil
						},
						inspect: func(USBDevice) (dfuInterface, error) { return testDFUInterface(), nil },
						enter: func(context.Context, USBDevice, byte) error {
							enters++
							current.ProductID = profile.DFUPID
							return nil
						},
						version: func(USBDevice) (string, byte, error) { return version, 8, nil },
						wait:    func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
					}
					image := &jabraDFUImage{Profile: profile, Manifest: &BuildVector{Version: "1.2.3"}, Payload: make([]byte, 130)}
					err := runUSBDFU(context.Background(), original, 8, image, backend)
					if (version == "1.2.3") != (err == nil) {
						t.Fatalf("version %s: %v", version, err)
					}
					if err != nil && !strings.Contains(err.Error(), "expected 1.2.3") {
						t.Fatal(err)
					}
					if opens == 0 || len(f.writes) != 4 || (recover && enters != 0) || (!recover && enters != 1) {
						t.Fatalf("opens=%d writes=%d enters=%d recovery=%t", opens, len(f.writes), enters, recover)
					}
				}
			}
		})
	}
}

func TestDFUWaitNeverUsesAnotherPortOrModel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	backend := usbDFUBackend{
		enumerate: func() ([]USBDevice, error) {
			return []USBDevice{{SysPath: "/other", VendorID: JabraVendorID, ProductID: 0x0421},
				{SysPath: "/wanted", VendorID: 0x1234, ProductID: 0x0421},
				{SysPath: "/wanted", VendorID: JabraVendorID, ProductID: 0x24c7}}, nil
		},
		wait: func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() },
	}
	_, err := waitUSBDFUDevice(ctx, "/wanted", func(d USBDevice) bool { return d.ProductID == 0x0421 }, backend)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("accepted another port/model", err)
	}
}

func TestDFUModeCommandUsesDescriptorAndKnownAddress(t *testing.T) {
	for _, layout := range []ControlLayout{{OutputID: 2, OutputBytes: 33}, {OutputID: 5, OutputBytes: 64}} {
		packet, err := usbDFUModePacket(layout, 8)
		if err != nil || len(packet) != layout.OutputBytes || !bytes.Equal(packet[:6], []byte{layout.OutputID, 8, 0, 1, 0x85, 7}) {
			t.Fatal(packet, err)
		}
	}
	if _, err := usbDFUModePacket(ControlLayout{OutputID: 2, OutputBytes: 33}, 4); err == nil {
		t.Fatal("accepted a child-through-dongle target")
	}
}
