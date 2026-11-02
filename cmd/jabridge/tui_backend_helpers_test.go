package main

import (
	"context"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

// Scripted tests receive a fresh server endpoint for every action. The idle
// poll endpoint remains separate, matching the production ownership contract.
type tuiActionPipes struct {
	mu       sync.Mutex
	closed   bool
	backend  *tuiIPCBackend
	poll     net.Conn
	peers    []net.Conn
	requests chan net.Conn
	done     chan struct{}
}

func newTUIActionPipeBackend(t *testing.T) (*tuiIPCBackend, *tuiActionPipes) {
	t.Helper()
	poll, peer := net.Pipe()
	requests := &tuiActionPipes{poll: peer, requests: make(chan net.Conn, 16), done: make(chan struct{})}
	backend := newTUIIPCBackend(ipc.ClientFromConnection(poll), "", requests.dial)
	requests.backend = backend
	t.Cleanup(requests.Close)
	return backend, requests
}
func (p *tuiActionPipes) dial(ctx context.Context) (*ipc.Client, error) {
	client, server := net.Pipe()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = client.Close()
		_ = server.Close()
		return nil, errTUIServiceNotConnected
	}
	p.peers = append(p.peers, server)
	p.mu.Unlock()
	select {
	case p.requests <- server:
		return ipc.ClientFromConnection(client), nil
	case <-p.done:
		_ = client.Close()
		_ = server.Close()
		return nil, errTUIServiceNotConnected
	case <-ctx.Done():
		_ = client.Close()
		_ = server.Close()
		return nil, ctx.Err()
	}
}
func (p *tuiActionPipes) Accept() (net.Conn, bool) {
	select {
	case connection := <-p.requests:
		return connection, true
	case <-p.done:
		return nil, false
	}
}
func (p *tuiActionPipes) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.done)
	peers := append([]net.Conn(nil), p.peers...)
	p.mu.Unlock()
	p.backend.close()
	_ = p.poll.Close()
	for _, peer := range peers {
		_ = peer.Close()
	}
}

func newTUIServiceTestBackend(t *testing.T, api ipc.API, buses ...*ipc.EventBus) *tuiIPCBackend {
	t.Helper()
	var bus *ipc.EventBus
	if len(buses) > 0 {
		bus = buses[0]
	}
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "tui.sock"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var connections []net.Conn
	var handlers sync.WaitGroup
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections = append(connections, connection)
			mu.Unlock()
			handlers.Add(1)
			go func() { defer handlers.Done(); ipc.HandleConnectionWithBus(connection, api, bus, 30*time.Second) }()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	client, err := ipc.Dial(ctx, listener.Addr().String())
	cancel()
	if err != nil {
		_ = listener.Close()
		<-accepted
		t.Fatal(err)
	}
	backend := newTUIIPCBackend(client, listener.Addr().String(), nil)
	t.Cleanup(func() {
		backend.close()
		_ = listener.Close()
		<-accepted
		mu.Lock()
		for _, connection := range connections {
			_ = connection.Close()
		}
		mu.Unlock()
		done := make(chan struct{})
		go func() { handlers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("test service handlers did not stop")
		}
	})
	return backend
}
