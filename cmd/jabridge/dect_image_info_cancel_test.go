package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	firmwaretool "github.com/Watchdog0x/jabridge/internal/firmware"
)

func TestDECTImageInfoRPCCancelStopsInspection(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			dectTestRegistry(t)
			old := inspectDECTImageInfo
			t.Cleanup(func() { inspectDECTImageInfo = old })
			firstRead, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var laterReads atomic.Int32
			inspectDECTImageInfo = func(ctx context.Context, _ *firmwaretool.ControlBinding) ([]firmwaretool.DECTComponentCheck, error) {
				defer close(stopped)
				close(firstRead) // Hold the first device response, as the core timeout fixture does.
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
					laterReads.Add(1)
					return nil, nil
				}
			}
			server, connection := net.Pipe()
			serverDone := make(chan struct{})
			go func() { defer close(serverDone); ipc.HandleConnection(server, &jabraAPIBridge{}) }()
			client := ipc.ClientFromConnection(connection)
			defer func() { close(release); _ = client.Close(); <-serverDone }()
			budget := time.Second
			if mode == "deadline" {
				budget = 40 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			callDone := make(chan error, 1)
			var received []ipc.DiagnosticCheck
			go func() {
				callDone <- client.CallScoped(ctx, "diagnostics.dect_image_info", map[string]uint16{"id": 0}, &received)
			}()
			select {
			case <-firstRead:
			case <-time.After(time.Second):
				t.Fatal("inspection did not start")
			}
			switch mode {
			case "cancel":
				cancel()
			case "disconnect":
				_ = client.Close()
			}
			select {
			case err := <-callDone:
				if err == nil || mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Error("cancelled call delivered success or wrong error", err)
				}
			case <-time.After(time.Second):
				t.Fatal("caller did not stop")
			}
			select {
			case <-stopped:
			case <-time.After(250 * time.Millisecond):
				t.Error("service kept inspecting after caller stopped")
			}
			if laterReads.Load() != 0 {
				t.Error("later component read began after cancellation")
			}
			if len(received) != 0 {
				t.Error("cancelled call delivered observations", received)
			}
		})
	}
}

func TestDECTImageInfoRPCCancelWhileWaitingForOwnership(t *testing.T) {
	for _, mode := range []string{"cancel", "disconnect", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			d := dectTestRegistry(t)
			reader, err := openHidrawCoordinated("synthetic", nil, func(string) (string, error) { return d.usbDevicePath, nil }, func(string) (*hidrawConn, error) { return &hidrawConn{}, nil })
			if err != nil {
				t.Fatal(err)
			}
			defer reader.close()
			old := inspectDECTImageInfo
			t.Cleanup(func() { inspectDECTImageInfo = old })
			var calls atomic.Int32
			inspectDECTImageInfo = func(context.Context, *firmwaretool.ControlBinding) ([]firmwaretool.DECTComponentCheck, error) {
				calls.Add(1)
				return nil, nil
			}
			server, connection := net.Pipe()
			serverDone := make(chan struct{})
			go func() { defer close(serverDone); ipc.HandleConnection(server, &jabraAPIBridge{}) }()
			client := ipc.ClientFromConnection(connection)
			defer func() { reader.close(); _ = client.Close(); <-serverDone }()
			budget := time.Second
			if mode == "deadline" {
				budget = 150 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			callDone := make(chan error, 1)
			go func() {
				callDone <- client.CallScoped(ctx, "diagnostics.dect_image_info", map[string]uint16{"id": 0}, nil)
			}()
			until := time.Now().Add(time.Second)
			for {
				link390ControlGate.mu.Lock()
				port := link390ControlGate.ports[d.usbDevicePath]
				waiting := port != nil && port.waiter != nil
				link390ControlGate.mu.Unlock()
				if waiting {
					break
				}
				if time.Now().After(until) {
					t.Fatal("inspection never waited for the existing reader")
				}
				time.Sleep(time.Millisecond)
			}
			switch mode {
			case "cancel":
				cancel()
			case "disconnect":
				_ = client.Close()
			}
			select {
			case err := <-callDone:
				if err == nil {
					t.Error("cancelled ownership wait succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("caller did not stop")
			}
			select {
			case <-serverDone:
			case <-time.After(250 * time.Millisecond):
				t.Fatal("service kept waiting after caller stopped")
			}
			reader.close()
			if calls.Load() != 0 || controlDeviceBusy(d) {
				t.Fatal("delayed inspection or ownership escaped", calls.Load())
			}
		})
	}
}

func TestDECTImageInfoRPCEnforcesRemoteDeadline(t *testing.T) {
	d := dectTestRegistry(t)
	old := inspectDECTImageInfo
	t.Cleanup(func() { inspectDECTImageInfo = old })
	started, stopped := make(chan struct{}), make(chan struct{})
	deadline := time.Now().Add(150 * time.Millisecond)
	inspectDECTImageInfo = func(ctx context.Context, _ *firmwaretool.ControlBinding) ([]firmwaretool.DECTComponentCheck, error) {
		defer close(stopped)
		if got, ok := ctx.Deadline(); !ok || got.After(deadline) {
			t.Error("client deadline was extended or lost", got)
		}
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	server, connection := net.Pipe()
	serverDone := make(chan struct{})
	go func() { defer close(serverDone); ipc.HandleConnection(server, &jabraAPIBridge{}) }()
	defer func() { _ = connection.Close(); <-serverDone }()
	request := ipc.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "diagnostics.dect_image_info", Params: json.RawMessage(`{"id":0}`), DeadlineUnixNano: deadline.UnixNano()}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("inspection never started")
	}
	// Keep the raw client open: only the transmitted deadline can stop work.
	select {
	case <-stopped:
	case <-time.After(400 * time.Millisecond):
		t.Fatal("service ignored the transmitted deadline")
	}
	select {
	case <-serverDone:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("scoped connection did not finish")
	}
	if controlDeviceBusy(d) {
		t.Fatal("cancelled inspection retained ownership")
	}
}
