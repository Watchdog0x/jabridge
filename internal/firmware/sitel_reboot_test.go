package firmware

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Reboot replies can disappear with the old HID interface. The simulator must
// expose that loss instead of always returning an ACK before changing PID.
type sitelRebootBackend struct {
	*sitelTestDevice
	replyError           error
	notSent, stayRuntime bool
	requests             int
}

func (b *sitelRebootBackend) runtime(ctx context.Context, d USBDevice) (*sitelRuntime, sitelIdentity, func() error, error) {
	r, id, close, err := b.sitelTestDevice.runtime(ctx, d)
	if err == nil {
		r.io = &sitelRebootIO{csrStageIO: r.io, backend: b}
	}
	return r, id, close, err
}

type sitelRebootIO struct {
	csrStageIO
	backend *sitelRebootBackend
	reboot  bool
}

func (p *sitelRebootIO) Write(ctx context.Context, data []byte) error {
	if len(data) >= 6 && data[4] == 0x85 && data[5] == 7 {
		p.backend.requests++
		if p.backend.notSent {
			return managementNotSent(p.backend.replyError)
		}
		p.reboot = true
	}
	err := p.csrStageIO.Write(ctx, data)
	if p.reboot && p.backend.stayRuntime {
		p.backend.bootMode = false
	}
	return err
}

func (p *sitelRebootIO) Read(ctx context.Context) ([]byte, error) {
	if p.reboot && p.backend.replyError != nil {
		return nil, p.backend.replyError
	}
	return p.csrStageIO.Read(ctx)
}

func TestSitelRebootLostReplyRequiresExpectedReconnection(t *testing.T) {
	for _, test := range []struct {
		name                                     string
		err                                      error
		notSent, stayRuntime, wrongPort, success bool
	}{
		{name: "acknowledged", success: true},
		{name: "disconnected", err: syscall.ENODEV, success: true},
		{name: "hid-read-error-after-reboot", err: syscall.EIO, success: true},
		{name: "reply-lost-after-reboot", err: context.DeadlineExceeded, success: true},
		{name: "never-sent", err: context.DeadlineExceeded, notSent: true},
		{name: "device-rejected", err: errSitelRejected},
		{name: "malformed-reply", err: errors.New("malformed Sitel management packet")},
		{name: "cancelled", err: context.Canceled},
		{name: "no-reconnection", err: context.DeadlineExceeded, stayRuntime: true},
		{name: "different-port", err: context.DeadlineExceeded, wrongPort: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			world := makeEvolve2World(0x0e41, 0x0e44)
			world.wrongPort = test.wrongPort
			backend := &sitelRebootBackend{sitelTestDevice: world, replyError: test.err, notSent: test.notSent, stayRuntime: test.stayRuntime}
			state := firmwareRecoveryState{ArchiveSHA256: "fixture"}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := runSitelInstall(ctx, backend, world.device(), world.images, world.wanted, &state, func() error { return nil }, nil)
			if (err == nil) != test.success {
				t.Fatalf("success=%t, got %v (bootMode=%t, writes=%d)", test.success, err, world.bootMode, world.writes)
			}
			if backend.requests != 1 {
				t.Fatalf("reboot command sent %d times", backend.requests)
			}
			if test.success {
				if world.bootMode || world.version != world.wanted || world.writes == 0 {
					t.Fatal("transfer did not verify and return to runtime")
				}
			} else if world.erases != 0 || world.writes != 0 {
				t.Fatal("unverified reboot reached flash writes")
			}
			if err != nil && !strings.HasPrefix(err.Error(), "sitel-") {
				t.Fatal("failed stage was lost", err)
			}
		})
	}
}
