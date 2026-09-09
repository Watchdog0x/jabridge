package btsearch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type testIO struct {
	packets       chan []byte
	startErr      error
	stops, closed atomic.Int32
}

func (t *testIO) Start(context.Context) error { return t.startErr }
func (t *testIO) Stop(context.Context) error  { t.stops.Add(1); return nil }
func (t *testIO) Close() error                { t.closed.Add(1); return nil }
func (t *testIO) Read(ctx context.Context) ([]byte, error) {
	select {
	case p := <-t.packets:
		return p, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestSearchSessionCompletionAndCleanup(t *testing.T) {
	transport := &testIO{packets: make(chan []byte, 3)}
	transport.packets <- found("Office", 1)
	transport.packets <- []byte{5, 0, 1, 0, 6, 0x0d, 0x23}
	s, err := Start(context.Background(), transport, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("search did not complete")
	}
	state := s.Snapshot()
	if state.State != "complete" || len(state.Devices) != 1 || transport.stops.Load() != 0 || transport.closed.Load() != 1 {
		t.Fatal(state, transport.stops.Load())
	}
}

func TestSearchCancellationStopsOnlyOnce(t *testing.T) {
	transport := &testIO{packets: make(chan []byte)}
	s, err := Start(context.Background(), transport, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().State != "stopped" || transport.stops.Load() != 1 || transport.closed.Load() != 1 {
		t.Fatal(s.Snapshot())
	}
}

func TestSearchStartFailureCleansUp(t *testing.T) {
	transport := &testIO{packets: make(chan []byte), startErr: errors.New("start rejected")}
	if _, err := Start(context.Background(), transport, time.Second); err == nil {
		t.Fatal("start failure hidden")
	}
	if transport.stops.Load() != 1 || transport.closed.Load() != 1 {
		t.Fatal("failed start leaked reader")
	}
}

func TestSearchDeadlineStopsRadio(t *testing.T) {
	transport := &testIO{packets: make(chan []byte)}
	s, err := Start(context.Background(), transport, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("timeout did not finish")
	}
	if s.Snapshot().State != "timed_out" || transport.stops.Load() != 1 {
		t.Fatal(s.Snapshot())
	}
}
