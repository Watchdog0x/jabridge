package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

type reportTestAPI struct {
	jabraAPIBridge
	requested chan uint16
}

func (a *reportTestAPI) ListDevices() []ipc.DeviceInfo {
	return []ipc.DeviceInfo{{ID: 7, PID: 0x4052, Name: "PRIVATE_CUSTOM_NAME", Serial: "PRIVATE_SERIAL", Connection: "usb", Selected: true}}
}

func (a *reportTestAPI) DiagnoseDevice(id uint16) ([]ipc.DiagnosticCheck, error) {
	a.requested <- id
	return []ipc.DiagnosticCheck{{Feature: "setting sidetone", State: "FAIL", Detail: "device reply timed out"}}, nil
}

func TestNativeReportUsesServiceAndOmitsPrivateDeviceFields(t *testing.T) {
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "ipc.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	api := &reportTestAPI{requested: make(chan uint16, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			ipc.HandleConnection(connection, api)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, err := ipc.Dial(ctx, listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var report bytes.Buffer
	writeNativeDiagnostic(&report, client)
	_ = client.Close()
	<-done
	if !strings.Contains(report.String(), "4052") || !strings.Contains(report.String(), "FAIL") || strings.Contains(report.String(), "PRIVATE") {
		t.Fatal(report.String())
	}
	select {
	case id := <-api.requested:
		if id != 7 {
			t.Fatal(id)
		}
	default:
		t.Fatal("no native diagnostic requested")
	}
}

func TestBlockedSettingsDoNotBecomePassFromCatalogMetadata(t *testing.T) {
	device := &jabra_DeviceInfo{productID: 0x4052}
	catalog := &modelcatalog.Capabilities{Properties: map[string]modelcatalog.Property{
		"sidetoneEnabled":            {Name: "sidetoneEnabled"},
		"futureUnimplementedSetting": {Name: "futureUnimplementedSetting"},
	}}
	checks := diagnoseSettings(device, catalog, false)
	states := map[string]string{}
	for _, check := range checks {
		states[check.Feature] = check.State
		if check.State == "PASS" {
			t.Fatal("catalog metadata passed as a hardware read")
		}
	}
	if states["setting sidetone"] != "BLOCKED" || states["catalog property futureUnimplementedSetting"] != "NOT COVERED" {
		t.Fatal(states)
	}
}

func TestUnknownModelAndTransportDoNotInventIndividualSettings(t *testing.T) {
	checks := diagnoseSettings(&jabra_DeviceInfo{productID: 0x0422}, nil, false)
	if len(checks) != 1 || checks[0].Feature != "setting discovery" || checks[0].State != "BLOCKED" {
		t.Fatalf("checks = %#v", checks)
	}
}

func TestProtocolFailuresAreUsefulWithoutRawData(t *testing.T) {
	if got := protocolDiagnosticError(fmt.Errorf("invalid battery capacity 230 serial=PRIVATE")); got != "response format/value rejected by native decoder" {
		t.Fatal(got)
	}
	if got := safeFirmwareDiagnostic("PRIVATE_SERIAL"); got != "version format unrecognized" {
		t.Fatal(got)
	}
}
