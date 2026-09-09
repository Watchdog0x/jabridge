package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestShutdownWaitsForDevicePollingToReleaseHardware(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := DefaultConfig()
	cfg.DisablePipeWire = true
	cfg.SocketPath = filepath.Join(t.TempDir(), "service.sock")
	cfg.PIDPath = cfg.SocketPath + ".pid"
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cfg, func(ctx context.Context) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			<-release // Model an in-flight bounded HID operation cleaning up.
		}, &nilAPI{})
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("service failed to start: %v", err)
	case <-time.After(time.Second):
		t.Fatal("poller did not start")
	}
	cancel()
	<-cancelled
	select {
	case err := <-done:
		close(release)
		t.Fatalf("service returned before the hardware worker stopped: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("service did not finish after polling stopped")
	}
}
