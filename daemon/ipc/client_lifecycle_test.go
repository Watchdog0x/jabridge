package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type writeObservedConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func TestIPCAlreadyCanceledCallDoesNotWrite(t *testing.T) {
	peer, conn := net.Pipe()
	observed := &writeObservedConn{Conn: conn, started: make(chan struct{})}
	client := newClient(observed)
	defer func() { _ = peer.Close(); _ = client.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Call(ctx, "settings.set", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-observed.started:
		t.Fatal("canceled call was transmitted")
	default:
	}
}

type shortRequestConn struct{ net.Conn }

func (c *shortRequestConn) Write([]byte) (int, error) { return 1, nil }

type replyBeforeWriteReturnsConn struct {
	net.Conn
	after func()
}

func (c *replyBeforeWriteReturnsConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err == nil {
		c.after()
	}
	return n, err
}

func TestIPCCompleteReplySurvivesImmediateDisconnect(t *testing.T) {
	peer, conn := net.Pipe()
	wrapped := &replyBeforeWriteReturnsConn{Conn: conn}
	client := newClient(wrapped)
	wrapped.after = func() { <-client.Done() }
	defer func() { _ = client.Close(); _ = peer.Close() }()
	go func() {
		defer func() { _ = peer.Close() }()
		var req Request
		if json.NewDecoder(peer).Decode(&req) != nil {
			return
		}
		_ = json.NewEncoder(peer).Encode(SuccessResponse(req.ID, map[string]bool{"ok": true}))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		t.Fatal("complete reply discarded when peer closed", err)
	}
}

func TestIPCShortWriteClosesTheStream(t *testing.T) {
	peer, conn := net.Pipe()
	client := newClient(&shortRequestConn{conn})
	defer func() { _ = peer.Close(); _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Call(ctx, "service.ping", nil, nil); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	select {
	case <-client.Done():
	default:
		t.Fatal("partial JSON stream left reusable")
	}
}

func TestIPCWriteDeadlineDoesNotLeakIntoNextCall(t *testing.T) {
	peer, conn := net.Pipe()
	go HandleConnection(peer, &mockAPI{})
	client := newClient(conn)
	defer func() { _ = client.Close() }()
	first, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := client.Ping(first); err != nil {
		t.Fatal(err)
	}
	<-first.Done()
	next, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := client.Ping(next); err != nil {
		t.Fatal("prior deadline contaminated next request", err)
	}
}

func TestIPCEventFloodKeepsRepliesResponsive(t *testing.T) {
	peer, conn := net.Pipe()
	client := newClient(conn)
	defer func() { _ = client.Close(); _ = peer.Close() }()
	flooded := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		decoder, encoder := json.NewDecoder(peer), json.NewEncoder(peer)
		var request Request
		if err := decoder.Decode(&request); err != nil {
			serverDone <- err
			return
		}
		if err := encoder.Encode(SuccessResponse(request.ID, map[string]bool{"subscribed": true})); err != nil {
			serverDone <- err
			return
		}
		for i := 0; i < 1000; i++ {
			if err := WriteNotification(peer, "device.signal", map[string]int{"sequence": i}); err != nil {
				serverDone <- err
				return
			}
		}
		close(flooded)
		if err := decoder.Decode(&request); err != nil {
			serverDone <- err
			return
		}
		serverDone <- encoder.Encode(SuccessResponse(request.ID, map[string]bool{"ok": true}))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Subscribe(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-flooded:
	case <-ctx.Done():
		t.Fatal("notification flood stalled reader")
	}
	if err := client.Ping(ctx); err != nil {
		t.Fatal("events starved RPC reply", err)
	}
	foundLatest := false
	for len(client.notifications) > 0 {
		n := <-client.notifications
		data := n.Params.(map[string]interface{})
		foundLatest = foundLatest || data["sequence"] == float64(999)
	}
	if !foundLatest {
		t.Fatal("latest notification was not retained")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestIPCConcurrentOutOfOrderAndDuplicateReplies(t *testing.T) {
	peer, conn := net.Pipe()
	client := newClient(conn)
	defer func() { _ = client.Close(); _ = peer.Close() }()
	serverDone := make(chan error, 1)
	go func() {
		decoder, encoder := json.NewDecoder(peer), json.NewEncoder(peer)
		requests := make([]Request, 3)
		for i := range requests {
			if err := decoder.Decode(&requests[i]); err != nil {
				serverDone <- err
				return
			}
		}
		for i := len(requests) - 1; i >= 0; i-- {
			response := SuccessResponse(requests[i].ID, map[string]string{"method": requests[i].Method})
			for repeat := 0; repeat < 2; repeat++ {
				if err := encoder.Encode(response); err != nil {
					serverDone <- err
					return
				}
			}
		}
		serverDone <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 3)
	for _, method := range []string{"first", "second", "third"} {
		go func(method string) {
			var response struct {
				Method string `json:"method"`
			}
			err := client.Call(ctx, method, nil, &response)
			if err == nil && response.Method != method {
				err = errors.New("reply matched to wrong concurrent request")
			}
			done <- err
		}(method)
	}
	for i := 0; i < 3; i++ {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func (c *writeObservedConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(p)
}

func TestIPCWriteHonorsCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			peer, conn := net.Pipe()
			observed := &writeObservedConn{Conn: conn, started: make(chan struct{})}
			client := newClient(observed)
			defer func() { _ = peer.Close(); _ = client.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			want := error(context.Canceled)
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
				want = context.DeadlineExceeded
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- client.Call(ctx, "service.ping", nil, nil) }()
			select {
			case <-observed.started:
			case <-time.After(time.Second):
				t.Fatal("write did not begin")
			}
			if !deadline {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Errorf("got %v, want %v", err, want)
				}
			case <-time.After(300 * time.Millisecond):
				t.Error("IPC call stayed blocked inside write after context ended")
				_ = client.Close()
				<-done
			}
		})
	}
}

func TestIPCQueuedWriterCanCancelWithoutClosingFirstCall(t *testing.T) {
	peer, conn := net.Pipe()
	observed := &writeObservedConn{Conn: conn, started: make(chan struct{})}
	client := newClient(observed)
	defer func() { _ = peer.Close(); _ = client.Close() }()
	first := make(chan error, 1)
	go func() { first <- client.Call(context.Background(), "service.ping", nil, nil) }()
	<-observed.started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() { second <- client.Call(ctx, "service.ping", nil, nil) }()
	select {
	case err := <-second:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Error(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Error("canceled request stuck behind another writer")
		_ = client.Close()
		<-second
	}
	select {
	case <-client.Done():
		t.Error("queued cancellation closed an unrelated call")
	default:
	}
	_ = client.Close()
	<-first
}

func TestIPCDuplicateRepliesDoNotBlockNotifications(t *testing.T) {
	peer, conn := net.Pipe()
	client := newClient(conn)
	response := make(chan clientResponse, 1)
	client.pendingMu.Lock()
	client.pending["1"] = response
	client.pendingMu.Unlock()
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		encoder := json.NewEncoder(peer)
		for i := 0; i < 3; i++ {
			if encoder.Encode(SuccessResponse(json.RawMessage("1"), map[string]bool{"ok": true})) != nil {
				return
			}
		}
		_ = WriteNotification(peer, "device.signal", map[string]string{"name": "battery", "value": "73"})
	}()
	defer func() {
		_ = client.Close()
		_ = peer.Close()
		// Unblock the pre-fix implementation's channel send during failure cleanup.
		select {
		case <-response:
		default:
		}
		<-sent
	}()
	select {
	case n := <-client.Notifications():
		if n.Method != "device.signal" {
			t.Fatal(n)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("duplicate reply blocked event delivery")
	}
}
