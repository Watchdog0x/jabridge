package ipc

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestClientCallPingAndSubscribe(t *testing.T) {
	server, connection := net.Pipe()
	bus := NewEventBus()
	go HandleConnectionWithBus(server, &mockAPI{}, bus, time.Second)
	client := newClient(connection)
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Subscribe(ctx); err != nil {
		t.Fatal(err)
	}
	bus.Publish("device.attached", DeviceInfo{ID: 1, Name: "Test", PID: 2})
	select {
	case notification := <-client.Notifications():
		if notification.Method != "device.attached" {
			t.Fatalf("notification = %#v", notification)
		}
	case <-ctx.Done():
		t.Fatal("notification timed out")
	}
}

func TestClientDecodesRemoteError(t *testing.T) {
	server, connection := net.Pipe()
	go HandleConnection(server, &mockAPI{})
	client := newClient(connection)
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Call(ctx, "missing.method", nil, nil); err == nil {
		t.Fatal("remote error was ignored")
	}
}
