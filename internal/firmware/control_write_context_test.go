package firmware

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestControlWriteContextCancelsBackpressureWithoutGoroutine(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(pair[0]), "fake-control")
	defer func() { _ = file.Close(); _ = unix.Close(pair[1]) }()
	for {
		_, err = unix.Write(pair[0], make([]byte, 4096))
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	layout, err := SelectControlLayout(managementReports(5, 64))
	if err != nil {
		t.Fatal(err)
	}
	transport := &ControlHidraw{file: file, Layout: layout}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := transport.WriteContext(ctx, []byte{5, 1, 0, 0, 0x86, 13, 0x21}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("blocked write ignored deadline", err)
	}
	// No worker goroutine owns the handle after cancellation. It remains ours.
	if _, err := file.Stat(); err != nil {
		t.Fatal(err)
	}
}
