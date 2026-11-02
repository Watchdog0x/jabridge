package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

type tuiRecordedRequest struct {
	method string
	action bool
}
type tuiRecordingService struct {
	mu       sync.Mutex
	requests []tuiRecordedRequest
	dials    atomic.Int32
	peers    []net.Conn
	handlers sync.WaitGroup
	closed   bool
	handle   func(ipc.Request)
}

func newTUIRecordingBackend(t *testing.T, handle func(ipc.Request)) (*tuiIPCBackend, *tuiRecordingService) {
	t.Helper()
	service := &tuiRecordingService{handle: handle}
	poll, peer := net.Pipe()
	service.peers = append(service.peers, peer)
	service.handlers.Add(1)
	go service.serve(peer, false)
	backend := newTUIIPCBackend(ipc.ClientFromConnection(poll), "", func(ctx context.Context) (*ipc.Client, error) {
		service.dials.Add(1)
		host, server := net.Pipe()
		service.mu.Lock()
		if service.closed {
			service.mu.Unlock()
			_ = host.Close()
			_ = server.Close()
			return nil, errTUIServiceNotConnected
		}
		service.peers = append(service.peers, server)
		service.handlers.Add(1)
		service.mu.Unlock()
		go service.serve(server, true)
		return ipc.ClientFromConnection(host), nil
	})
	t.Cleanup(func() {
		backend.close()
		service.mu.Lock()
		service.closed = true
		peers := append([]net.Conn(nil), service.peers...)
		service.mu.Unlock()
		for _, peer := range peers {
			_ = peer.Close()
		}
		done := make(chan struct{})
		go func() { service.handlers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("recording service did not stop")
		}
	})
	return backend, service
}
func (s *tuiRecordingService) serve(connection net.Conn, action bool) {
	defer s.handlers.Done()
	defer func() { _ = connection.Close() }()
	decoder, encoder := json.NewDecoder(connection), json.NewEncoder(connection)
	for {
		var request ipc.Request
		if decoder.Decode(&request) != nil {
			return
		}
		s.mu.Lock()
		s.requests = append(s.requests, tuiRecordedRequest{request.Method, action})
		s.mu.Unlock()
		if s.handle != nil {
			s.handle(request)
		}
		if encoder.Encode(ipc.SuccessResponse(request.ID, nil)) != nil {
			return
		}
		if action {
			// Match the service: the client owns the transaction lifetime.
			_, _ = io.Copy(io.Discard, connection)
			return
		}
	}
}
func (s *tuiRecordingService) snapshot() []tuiRecordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tuiRecordedRequest(nil), s.requests...)
}

func TestTUIActionMethodsNeverUsePollConnection(t *testing.T) {
	backend, service := newTUIRecordingBackend(t, nil)
	old := currentTUIBackend()
	setTUIBackend(backend)
	t.Cleanup(func() { setTUIBackend(old) })
	methods := []string{"settings.list", "settings.set", "sound.default", "sound.volume", "sound.mute", "sound.mode", "sound.list", "device.select", "device.reset", "device.reset.bound", "bt.connect", "bt.disconnect", "bt.forget", "bt.search.connect", "bt.search", "bt.search.stop", "bt.search.status"}
	for _, method := range methods {
		var err error
		if method == "bt.search" || method == "bt.search.stop" || method == "bt.search.status" {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err = backend.call(ctx, method, nil, nil)
			cancel()
		} else {
			err = tuiIPCCall(method, nil, nil)
		}
		if err != nil {
			t.Fatal(method, err)
		}
	}
	requests := service.snapshot()
	if len(requests) != len(methods) || int(service.dials.Load()) != len(methods) {
		t.Fatal("wrong connection count", requests, service.dials.Load())
	}
	for i, request := range requests {
		if !request.action || request.method != methods[i] {
			t.Fatal("action used the poll connection", i, request)
		}
	}
}
func TestTUIActionTimeoutDoesNotReplay(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var commits atomic.Int32
	backend, service := newTUIRecordingBackend(t, func(ipc.Request) { commits.Add(1); close(entered); <-release })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- backend.call(ctx, "settings.set", nil, nil) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request never reached service")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deadline did not stop waiting")
	}
	if commits.Load() != 1 || service.dials.Load() != 1 || len(service.snapshot()) != 1 {
		t.Fatal("timed-out request replayed", commits.Load(), service.dials.Load())
	}
}
func TestTUICloseDuringActionIsClean(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	backend, service := newTUIRecordingBackend(t, func(ipc.Request) { close(entered); <-release })
	done := make(chan error, 1)
	go func() { done <- backend.call(context.Background(), "settings.set", nil, nil) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request never started")
	}
	backend.close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed action reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("close left an action waiting")
	}
	backend.mu.RLock()
	active := len(backend.active)
	backend.mu.RUnlock()
	if active != 0 || service.dials.Load() != 1 || len(service.snapshot()) != 1 {
		t.Fatal("close leaked or replayed action", active, service.dials.Load())
	}
}
func TestTUIRefusesActionWhilePollClientDown(t *testing.T) {
	backend, service := newTUIRecordingBackend(t, nil)
	_ = backend.clientSnapshot().Close()
	if err := backend.call(context.Background(), "settings.set", nil, nil); !errors.Is(err, errTUIServiceNotConnected) {
		t.Fatal(err)
	}
	if service.dials.Load() != 0 || len(service.snapshot()) != 0 {
		t.Fatal("request dialed while disconnected")
	}
}
func TestTUIActionsRemainSerialized(t *testing.T) {
	firstEntered, releaseFirst := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	defer release()
	var active, maxActive atomic.Int32
	backend, service := newTUIRecordingBackend(t, func(request ipc.Request) {
		if request.Method != "settings.set" {
			return
		}
		count := active.Add(1)
		defer active.Add(-1)
		for {
			old := maxActive.Load()
			if old >= count || maxActive.CompareAndSwap(old, count) {
				break
			}
		}
		var params map[string]string
		if json.Unmarshal(request.Params, &params) != nil {
			return
		}
		if params["key"] == "first" {
			close(firstEntered)
			<-releaseFirst
		}
	})
	old := currentTUIBackend()
	setTUIBackend(backend)
	t.Cleanup(func() { setTUIBackend(old) })
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- tuiIPCCall("settings.set", map[string]string{"key": "first"}, nil) }()
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first action did not start")
	}
	go func() { second <- tuiIPCCall("settings.set", map[string]string{"key": "second"}, nil) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := backend.call(ctx, "bt.search.status", nil, nil); err != nil {
		cancel()
		t.Fatal("search status queued behind action", err)
	}
	cancel()
	time.Sleep(30 * time.Millisecond)
	if len(service.snapshot()) != 2 || maxActive.Load() != 1 {
		t.Fatal("actions overlapped", service.snapshot(), maxActive.Load())
	}
	release()
	for _, done := range []chan error{first, second} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("serialized action did not finish")
		}
	}
	if maxActive.Load() != 1 || len(service.snapshot()) != 3 {
		t.Fatal("wrong serialized requests", service.snapshot(), maxActive.Load())
	}
}
func TestTUITimeoutCoversServerBudget(t *testing.T) {
	for _, method := range []string{"settings.list", "settings.set", "device.reset.bound"} {
		if got := tuiIPCTimeout(method); got != 90*time.Second {
			t.Fatal(method, got)
		}
	}
	for _, method := range []string{"bt.search.connect", "bt.connect", "bt.disconnect", "bt.forget"} {
		if got := tuiIPCTimeout(method); got != 45*time.Second {
			t.Fatal(method, got)
		}
	}
	for _, method := range []string{"sound.default", "sound.volume", "sound.mute", "sound.mode"} {
		if got := tuiIPCTimeout(method); got != 25*time.Second {
			t.Fatal(method, got)
		}
	}
	if got := tuiIPCTimeout("service.ping"); got != 15*time.Second {
		t.Fatal(got)
	}
}
func TestTUIShutdownRejectsLateReconnect(t *testing.T) {
	backend, _ := newTUIRecordingBackend(t, nil)
	backend.close()
	host, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	client := ipc.ClientFromConnection(host)
	backend.replaceClient(client)
	if backend.clientSnapshot() != nil {
		t.Fatal("shutdown installed a late reconnect")
	}
	select {
	case <-client.Done():
	default:
		t.Fatal("late reconnect was not closed")
	}
}

func TestTUIDialFailureSendsNothingAndUsesShortBudget(t *testing.T) {
	backend, service := newTUIRecordingBackend(t, nil)
	failure := errors.New("test dial failed")
	var dials atomic.Int32
	backend.dial = func(ctx context.Context) (*ipc.Client, error) {
		dials.Add(1)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Error("dial has no two-second budget")
		}
		return nil, failure
	}
	if err := backend.call(context.Background(), "settings.set", nil, nil); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if dials.Load() != 1 || len(service.snapshot()) != 0 {
		t.Fatal("failed dial sent or retried a request")
	}
}
func TestTUICloseDuringDialClosesLateClient(t *testing.T) {
	backend, service := newTUIRecordingBackend(t, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseDial := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseDial()
	host, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	late := ipc.ClientFromConnection(host)
	backend.dial = func(context.Context) (*ipc.Client, error) { close(entered); <-release; return late, nil }
	done := make(chan error, 1)
	go func() { done <- backend.call(context.Background(), "settings.set", nil, nil) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("dial did not start")
	}
	backend.close()
	releaseDial()
	select {
	case err := <-done:
		if !errors.Is(err, errTUIServiceNotConnected) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("late dial did not stop")
	}
	select {
	case <-late.Done():
	default:
		t.Fatal("late action client leaked")
	}
	if len(service.snapshot()) != 0 {
		t.Fatal("closed backend sent a request")
	}
}
func TestTUIQueuedActionUsesItsContextBudget(t *testing.T) {
	backend, service := newTUIRecordingBackend(t, nil)
	release, err := backend.acquireAction(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if second, err := backend.acquireAction(ctx); !errors.Is(err, context.DeadlineExceeded) {
		if second != nil {
			second()
		}
		t.Fatal(err)
	}
	if service.dials.Load() != 0 {
		t.Fatal("queued action dialed before taking its turn")
	}
}
