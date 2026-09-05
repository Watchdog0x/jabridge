package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Watchdog0x/jabridge/internal/history"
)

type Client struct {
	conn          net.Conn
	writeMu       sync.Mutex
	pendingMu     sync.Mutex
	pending       map[string]chan clientResponse
	notifications chan Notification
	done          chan struct{}
	closeOnce     sync.Once
	nextID        atomic.Uint64
}

type clientResponse struct {
	Result json.RawMessage
	Error  *RPCError
}

type clientWireMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RemoteError struct {
	Code    int
	Message string
}

func (err *RemoteError) Error() string {
	return fmt.Sprintf("IPC error %d: %s", err.Code, err.Message)
}

func Dial(ctx context.Context, socketPath string) (*Client, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", socketPath, err)
	}
	return newClient(conn), nil
}

func DialWithRetry(ctx context.Context, socketPath string) (*Client, error) {
	backoff := 100 * time.Millisecond
	for {
		client, err := Dial(ctx, socketPath)
		if err == nil {
			return client, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 2*time.Second {
			backoff *= 2
		}
	}
}

func newClient(conn net.Conn) *Client {
	client := &Client{
		conn:          conn,
		pending:       make(map[string]chan clientResponse),
		notifications: make(chan Notification, 64),
		done:          make(chan struct{}),
	}
	go client.readLoop()
	return client
}

func (client *Client) Call(ctx context.Context, method string, params, result any) (callErr error) {
	started := time.Now()
	entry := history.Event{Component: "ipc-client", Action: "request", Method: method, Operation: history.NextOperation()}
	trace := history.TraceMethod(method)
	if trace {
		entry.Phase = "start"
		history.Record(entry)
	}
	defer func() {
		if value := recover(); value != nil {
			entry.Phase = "panic"
			entry.Error = "panic"
			history.Record(entry)
			panic(value)
		}
		if !trace && callErr == nil {
			return
		}
		entry.Phase = "ok"
		entry.Error = history.Classify(callErr)
		entry.DurationMS = time.Since(started).Milliseconds()
		if callErr != nil {
			entry.Phase = "error"
		}
		var remote *RemoteError
		if errors.As(callErr, &remote) {
			entry.RPCCode = remote.Code
		}
		history.Record(entry)
	}()
	if client == nil || client.conn == nil {
		return errors.New("IPC client is not connected")
	}
	id := client.nextID.Add(1)
	idText := strconv.FormatUint(id, 10)
	idJSON := json.RawMessage(idText)
	request := Request{JSONRPC: "2.0", ID: idJSON, Method: method}
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("encode %s params: %w", method, err)
		}
		request.Params = encoded
	}
	responseChannel := make(chan clientResponse, 1)
	client.pendingMu.Lock()
	client.pending[idText] = responseChannel
	client.pendingMu.Unlock()
	defer func() {
		client.pendingMu.Lock()
		delete(client.pending, idText)
		client.pendingMu.Unlock()
	}()

	client.writeMu.Lock()
	err := json.NewEncoder(client.conn).Encode(request)
	client.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("send %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-client.done:
		return errors.New("IPC connection closed")
	case response := <-responseChannel:
		if response.Error != nil {
			return &RemoteError{Code: response.Error.Code, Message: response.Error.Message}
		}
		if result == nil || len(response.Result) == 0 || string(response.Result) == "null" {
			return nil
		}
		if err := json.Unmarshal(response.Result, result); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
		return nil
	}
}

func (client *Client) Subscribe(ctx context.Context) error {
	var response struct {
		Subscribed bool `json:"subscribed"`
	}
	if err := client.Call(ctx, "subscribe", nil, &response); err != nil {
		return err
	}
	if !response.Subscribed {
		return errors.New("service did not confirm subscription")
	}
	return nil
}

func (client *Client) Ping(ctx context.Context) error {
	var response struct {
		OK bool `json:"ok"`
	}
	if err := client.Call(ctx, "service.ping", nil, &response); err != nil {
		return err
	}
	if !response.OK {
		return errors.New("service ping failed")
	}
	return nil
}

func (client *Client) Notifications() <-chan Notification {
	return client.notifications
}

func (client *Client) Done() <-chan struct{} {
	return client.done
}

func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	var closeErr error
	client.closeOnce.Do(func() {
		close(client.done)
		closeErr = client.conn.Close()
		client.failPending()
	})
	return closeErr
}

func (client *Client) readLoop() {
	defer history.CapturePanic(history.Event{Component: "ipc-client", Action: "request"})
	scanner := bufio.NewScanner(client.conn)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	for scanner.Scan() {
		var message clientWireMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			history.Record(history.Event{Component: "ipc-client", Action: "malformed", Phase: "error", Error: "malformed"})
			continue
		}
		if message.Method != "" && len(message.ID) == 0 {
			var params interface{}
			if len(message.Params) != 0 {
				_ = json.Unmarshal(message.Params, &params)
			}
			notification := Notification{JSONRPC: message.JSONRPC, Method: message.Method, Params: params}
			select {
			case client.notifications <- notification:
			default:
				select {
				case <-client.notifications:
				default:
				}
				select {
				case client.notifications <- notification:
				default:
				}
			}
			continue
		}
		client.pendingMu.Lock()
		responseChannel := client.pending[string(message.ID)]
		client.pendingMu.Unlock()
		if responseChannel != nil {
			responseChannel <- clientResponse{Result: message.Result, Error: message.Error}
		}
	}
	select {
	case <-client.done:
	default:
		reason := "transport-closed"
		phase := "observed"
		if scanner.Err() != nil {
			reason = history.Classify(scanner.Err())
			phase = "error"
		}
		history.Record(history.Event{Component: "ipc-client", Action: "close", Phase: phase, Error: reason})
	}
	client.closeOnce.Do(func() {
		_ = client.conn.Close()
		close(client.done)
		client.failPending()
	})
}

func (client *Client) failPending() {
	client.pendingMu.Lock()
	defer client.pendingMu.Unlock()
	for id := range client.pending {
		delete(client.pending, id)
	}
}
