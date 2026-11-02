package daemon

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

type boundResetForwardTestAPI struct {
	nilAPI
	calls  int
	target ipc.SettingTarget
}

func (a *boundResetForwardTestAPI) FactoryResetBound(target ipc.SettingTarget) (ipc.BoundResetResult, error) {
	a.calls++
	a.target = target
	return ipc.BoundResetResult{Outcome: "requested", Attempted: true, Message: "Request accepted."}, nil
}
func TestServiceWrapperForwardsBoundResetOverIPC(t *testing.T) {
	inner := &boundResetForwardTestAPI{}
	wrapped := &busylightAPI{API: inner}
	server, connection := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); ipc.HandleConnection(server, wrapped) }()
	client := ipc.ClientFromConnection(connection)
	defer func() { _ = client.Close(); <-done }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	target := ipc.SettingTarget{ID: 7, Instance: strings.Repeat("a", 32), Topology: "same"}
	var result ipc.BoundResetResult
	if err := client.Call(ctx, "device.reset.bound", map[string]any{"confirm": "RESET_LINK390_DONGLE", "target": target}, &result); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 1 || inner.target != target || result.Outcome != "requested" || !result.Attempted {
		t.Fatal(inner.calls, inner.target, result)
	}
}

func TestServiceWrapperRefusesMissingBoundResetBackend(t *testing.T) {
	wrapped := &busylightAPI{API: &nilAPI{}}
	if _, err := wrapped.FactoryResetBound(ipc.SettingTarget{ID: 7, Instance: strings.Repeat("a", 32)}); err == nil {
		t.Fatal("missing backend reported reset success")
	}
}
