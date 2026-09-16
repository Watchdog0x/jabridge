package firmware

import (
	"context"
	"errors"
	"io"
	"testing"
)

type cameraCommandPeer struct {
	writes            int
	writeErr, readErr error
}

func (p *cameraCommandPeer) Write(context.Context, []byte) error  { p.writes++; return p.writeErr }
func (p *cameraCommandPeer) Read(context.Context) ([]byte, error) { return nil, p.readErr }

func TestNativeCameraActivationDistinguishesUnsentFromLostReply(t *testing.T) {
	previous := commandLineRiskAccepted.Load()
	commandLineRiskAccepted.Store(true)
	defer commandLineRiskAccepted.Store(previous)
	for _, mode := range []string{"already-cancelled", "before-write", "write-uncertain", "reply-lost"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			peer := &cameraCommandPeer{readErr: io.EOF}
			switch mode {
			case "already-cancelled":
				cancel()
			case "before-write":
				peer.writeErr = managementNotSent(context.DeadlineExceeded)
			case "write-uncertain":
				peer.writeErr = context.DeadlineExceeded
			}
			control := &nativeCameraControl{runtime: &sitelRuntime{io: peer}, id: cameraIdentity{Address: 8}}
			err := control.Start(ctx)
			if err == nil {
				t.Fatal("injected failure was ignored")
			}
			known := mode == "already-cancelled" || mode == "before-write"
			if errors.Is(err, errCameraCommandNotStarted) != known {
				t.Fatalf("wrong transmission classification for %s: %v", mode, err)
			}
			if mode == "already-cancelled" && (peer.writes != 0 || !errors.Is(err, context.Canceled)) {
				t.Fatal("cancelled activation reached transport or lost cause", peer.writes, err)
			}
			if mode == "before-write" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("pre-write failure lost its cause")
			}
		})
	}
}

func TestCameraExplicitActivationRejectionCanRetry(t *testing.T) {
	d, a, state, id := cameraInstallFixture()
	first := &fakeCameraControl{id: id, startError: errSitelRejected}
	backend := &fakeCameraBackend{controls: []*fakeCameraControl{first}}
	if err := runCameraInstall(context.Background(), backend, d, "unused", a, state, func() error { return nil }, nil); !errors.Is(err, errSitelRejected) {
		t.Fatal(err)
	}
	if state.Phase != "staging" || first.started {
		t.Fatal("explicit rejection left an active operation")
	}
}
