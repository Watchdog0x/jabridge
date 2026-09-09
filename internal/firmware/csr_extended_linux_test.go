package firmware

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type extendedIdentityPeer struct {
	replies                   [][]byte
	pid                       uint16
	both, wrongPID, malformed bool
	destructive               int
}

func (p *extendedIdentityPeer) Write(ctx context.Context, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if raw[4]&0xc0 != 0x40 {
		p.destructive++
		return errors.New("unexpected destructive operation")
	}
	if raw[1] == 1 && !p.both {
		p.replies = append(p.replies, []byte{5, 0, 1, raw[3], 0xc6, 0xfe, 1})
		return nil
	}
	var data []byte
	switch {
	case raw[5] == 2 && raw[6] == 0x11:
		pid := p.pid
		if p.wrongPID {
			pid++
		}
		data = make([]byte, 2)
		binary.LittleEndian.PutUint16(data, pid)
	case raw[5] == 2 && raw[6] == 1:
		data = append([]byte{9}, []byte("test-only")...)
	case raw[5] == 2 && raw[6] == 2:
		data = []byte{2, 1, 0x98}
	case raw[5] == 2 && raw[6] == 3:
		data = []byte{5, '1', '.', '0', '.', '0'}
		if p.malformed {
			data[0] = 7
		}
	case raw[5] == 0x13 && raw[6] == 8:
		data = []byte{9, 4}
	case raw[5] == 2 && raw[6] == 0x14:
		data = []byte{16}
	default:
		return errors.New("unknown identity query")
	}
	p.replies = append(p.replies, append([]byte{5, 0, raw[1], raw[3], 0xc0 | byte(6+len(data)), raw[5], raw[6]}, data...))
	return nil
}
func (p *extendedIdentityPeer) Read(ctx context.Context) ([]byte, error) {
	if len(p.replies) == 0 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	reply := p.replies[0]
	p.replies = p.replies[1:]
	return reply, nil
}

func TestExtendedCSRReadOnlyIdentityBinding(t *testing.T) {
	for _, name := range []string{"valid", "ambiguous", "wrong-pid", "malformed"} {
		t.Run(name, func(t *testing.T) {
			peer := &extendedIdentityPeer{pid: 0x253d, both: name == "ambiguous", wrongPID: name == "wrong-pid", malformed: name == "malformed"}
			device := USBDevice{VendorID: JabraVendorID, ProductID: 0x253d, SysPath: "isolated-port", attachment: &usbAttachment{fingerprint: "isolated-instance"}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			connection, err := identifyExtendedCSRConnection(ctx, peer, device, 64, func() error { return nil })
			if (err == nil) != (name == "valid") {
				t.Fatal(name, err)
			}
			if name == "valid" && (connection.Address != 8 || connection.Identity.PID != 0x253d || connection.Identity.Variant != "0198" || connection.Identity.Language != 0x409) {
				t.Fatal(connection.Identity)
			}
			if peer.destructive != 0 {
				t.Fatal("discovery sent a write")
			}
		})
	}
}

func TestExtendedCSRContextHID(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(pair[0]), "isolated-hid")
	defer func() { _ = file.Close() }()
	peer := pair[1]
	defer func() {
		if peer >= 0 {
			_ = unix.Close(peer)
		}
	}()
	transport := csrContextHID{&HidrawTransport{f: file, reportSize: 64}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	_, err = transport.Read(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("blocked read", err)
	}
	report := make([]byte, 64)
	report[0] = 5
	for {
		_, err = unix.Write(pair[0], report)
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = transport.Write(ctx, report)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("blocked write", err)
	}
	// Drain the deliberate backpressure before testing an orderly peer close.
	// Closing a socket with unread messages resets the connection instead.
	for {
		_, err = unix.Read(peer, make([]byte, 256))
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = unix.Write(peer, report); err != nil {
		t.Fatal(err)
	}
	if err = unix.Close(peer); err != nil {
		t.Fatal(err)
	}
	peer = -1
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := transport.Read(ctx)
	if err != nil || len(got) != 64 {
		t.Fatal("lost final reply", err, len(got))
	}
}
