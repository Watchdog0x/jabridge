package firmware

import (
	"context"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSitelIssue43UnnumberedHIDHandshake(t *testing.T) {
	// Descriptor fields from the report: report 0, 64 bytes, FF54, 8-bit
	// elements. Linux reads omit ID 0; writes include that leading zero.
	reports := []HIDReport{
		{ID: 0, Kind: "input", Bytes: 64, Fields: []HIDField{{SizeBits: 8, Count: 64, UsagePage: 0xff54}}},
		{ID: 0, Kind: "output", Bytes: 64, Fields: []HIDField{{SizeBits: 8, Count: 64, UsagePage: 0xff54}}},
	}
	in, out, err := selectSitelLayouts(reports)
	if err != nil || in.ReportID != 0 || in.ReportBytes != 65 || out != in {
		t.Fatal(in, out, err)
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fds[0]), "simulated-hid")
	defer func() { _ = file.Close(); _ = unix.Close(fds[1]) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		// Independent peer: read the host's 65-byte write and return the
		// bootloader's 64-byte unnumbered response, without using its codec.
		buffer := make([]byte, 65)
		for {
			if err := ctx.Err(); err != nil {
				done <- err
				return
			}
			poll := []unix.PollFd{{Fd: int32(fds[1]), Events: unix.POLLIN}}
			if _, err := unix.Poll(poll, 10); err != nil {
				done <- err
				return
			}
			if poll[0].Revents&unix.POLLIN == 0 {
				continue
			}
			n, err := unix.Read(fds[1], buffer)
			if err != nil {
				done <- err
				return
			}
			if n != 65 || buffer[0] != 0 || buffer[1] != 0x10 {
				done <- unix.EPROTO
				return
			}
			reply := make([]byte, 64)
			reply[0] = 0x20
			_, err = unix.Write(fds[1], reply)
			done <- err
			return
		}
	}()
	raw := &sitelRawHID{file: file, in: in, out: out}
	link := &sitelLink{io: raw, in: in, out: out, timeout: 100 * time.Millisecond}
	startErr := link.start(ctx)
	peerErr := <-done
	if startErr != nil || peerErr != nil || !link.ready {
		t.Fatal(startErr, peerErr)
	}
}
