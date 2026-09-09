package firmware

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type commitTransport struct {
	mode      string
	replies   [][]byte
	committed bool
}

func (f *commitTransport) Close() error { return nil }

func (f *commitTransport) Write(report []byte) error {
	address, seq, flags, class, op := report[1], report[3], report[4], report[5], report[6]
	if flags&0xc0 == GnpFlagQuery {
		data := []byte{2, 1, 0x67}
		if class == 2 && op == 3 {
			data = []byte{5, '1', '.', '2', '.', '3'}
		}
		reply := append([]byte{0, address, seq, 0xc0 | byte(6+len(data)), class, op}, data...)
		f.replies = append(f.replies, padTo63(reply))
		return nil
	}
	ack := append([]byte{0, address, seq, 0xca, GnpAckOK}, report[1:6]...)
	switch op {
	case OtaOpDfuFromSquif:
		f.committed = true
		switch f.mode {
		case "write failure":
			return unix.ENODEV
		case "rejected":
			f.replies = append(f.replies, padTo63([]byte{0, address, seq, 0xc6, 0xfe, 1}))
			return nil
		case "lost reply", "read timeout":
			return nil
		}
	case OtaOpWriteBlock:
		f.replies = append(f.replies, padTo63([]byte{0, address, 0x42, 10, 0x0f, OtaEventPreloadProgress, 0, 0, 0, 0}),
			padTo63([]byte{0, address, 0x43, 7, 0x0f, OtaEventVerifyStatus, 0}))
		return nil
	case OtaOpSendStart:
		f.replies = append(f.replies, padTo63(ack), padTo63([]byte{0, address, 0x41, 10, 0x0f, OtaEventFlashEraseDone, 0, 0, 0, 0}))
		return nil
	}
	f.replies = append(f.replies, padTo63(ack))
	return nil
}

func (f *commitTransport) Read(time.Duration) ([]byte, error) {
	if len(f.replies) > 0 {
		reply := f.replies[0]
		f.replies = f.replies[1:]
		return reply, nil
	}
	if f.committed && f.mode == "lost reply" {
		return nil, unix.ENODEV
	}
	return nil, errors.New("read timed out")
}

func TestCSRCommitOnlyRecoversReplyLossAfterSuccessfulWrite(t *testing.T) {
	for _, mode := range []string{"ack", "lost reply", "write failure", "rejected", "read timeout"} {
		t.Run(mode, func(t *testing.T) {
			transport := &commitTransport{mode: mode}
			updater := NewCsrOtaUpdater(transport, CsrOtaOptions{AckTimeout: time.Millisecond, EraseTimeout: time.Millisecond, VerifyTimeout: time.Millisecond, ReattachWait: time.Millisecond}, 64, 1, 0x24c7)
			verified := false
			updater.verifyReattach = func() error { verified = true; return nil }
			err := updater.FlashAll([]OtaPartition{{ID: 254, Data: make([]byte, 16)}}, [3]byte{1, 2, 3})
			wantSuccess := mode == "ack" || mode == "lost reply"
			if (err == nil) != wantSuccess || verified != wantSuccess {
				t.Fatalf("mode=%s err=%v verified=%t", mode, err, verified)
			}
		})
	}
}

func TestCSRCommitReplyLossRequiresSuccessfulVersionReadback(t *testing.T) {
	updater := NewCsrOtaUpdater(&commitTransport{mode: "lost reply"}, CsrOtaOptions{AckTimeout: time.Millisecond, EraseTimeout: time.Millisecond, VerifyTimeout: time.Millisecond, ReattachWait: time.Millisecond}, 64, 1, 0x24c7)
	wanted := fmt.Errorf("wrong device or installed version")
	updater.verifyReattach = func() error { return wanted }
	if err := updater.FlashAll([]OtaPartition{{ID: 254, Data: make([]byte, 16)}}, [3]byte{1, 2, 3}); !errors.Is(err, wanted) {
		t.Fatal(err)
	}
}
