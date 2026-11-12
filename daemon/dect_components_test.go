package daemon

import (
	"context"
	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"net"
	"slices"
	"testing"
	"time"
)

type dectForwardAPI struct {
	nilAPI
	calls int
	id    uint16
}

func (a *dectForwardAPI) DECTComponents(id uint16) ([]ipc.DiagnosticCheck, error) {
	a.calls++
	a.id = id
	return []ipc.DiagnosticCheck{{Feature: "DECT base", State: "PASS", Detail: "observed fixture"}}, nil
}
func TestDECTComponentServiceWrapperAndCapabilities(t *testing.T) {
	inner := &dectForwardAPI{}
	wrapped := &busylightAPI{API: inner}
	server, connection := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); ipc.HandleConnection(server, wrapped) }()
	client := ipc.ClientFromConnection(connection)
	defer func() { _ = client.Close(); <-done }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var cap ipc.ServiceCapabilities
	if err := client.Call(ctx, "service.capabilities", nil, &cap); err != nil || !slices.Contains(cap.Reads, "diagnostics.dect_components") {
		t.Fatal(cap, err)
	}
	var checks []ipc.DiagnosticCheck
	if err := client.Call(ctx, "diagnostics.dect_components", map[string]uint16{"id": 7}, &checks); err != nil || inner.calls != 1 || inner.id != 7 || len(checks) != 1 {
		t.Fatal(checks, err, inner.calls)
	}
	if _, err := (&busylightAPI{API: &nilAPI{}}).DECTComponents(7); err == nil {
		t.Fatal("missing inspection backend reported success")
	}
}

type dectInfoForwardAPI struct {
	nilAPI
	calls int
	id    uint16
}

func (a *dectInfoForwardAPI) DECTImageInfo(ctx context.Context, id uint16) ([]ipc.DiagnosticCheck, error) {
	a.calls++
	a.id = id
	return []ipc.DiagnosticCheck{{Feature: "DECT base", State: "PASS", Detail: "observed fixture"}}, nil
}
func TestDECTImageInfoServiceWrapperAndCapabilities(t *testing.T) {
	inner := &dectInfoForwardAPI{}
	wrapped := &busylightAPI{API: inner}
	server, connection := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); ipc.HandleConnection(server, wrapped) }()
	client := ipc.ClientFromConnection(connection)
	defer func() { _ = client.Close(); <-done }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var cap ipc.ServiceCapabilities
	if err := client.Call(ctx, "service.capabilities", nil, &cap); err != nil || !slices.Contains(cap.Reads, "diagnostics.dect_image_info") {
		t.Fatal(cap, err)
	}
	var checks []ipc.DiagnosticCheck
	if err := client.CallScoped(ctx, "diagnostics.dect_image_info", map[string]uint16{"id": 7}, &checks); err != nil || inner.calls != 1 || inner.id != 7 || len(checks) != 1 {
		t.Fatal(checks, err, inner.calls)
	}
	if _, err := (&busylightAPI{API: &nilAPI{}}).DECTImageInfo(ctx, 7); err == nil {
		t.Fatal("missing inspection backend reported success")
	}
}
