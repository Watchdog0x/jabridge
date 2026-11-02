package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/headsetvolume"
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
	_, err := headsetvolume.Inspect(nil)
	if got := protocolDiagnosticError(fmt.Errorf("PRIVATE_PATH: %w", err)); got != "not the validated Evolve2 30 SE USB firmware" {
		t.Fatal("descriptor rejection was hidden or leaked its wrapper", got)
	}
}

func TestPairingAccessCheckExplainsPolicyWithoutIO(t *testing.T) {
	preservePairingOption(t)
	for _, preview := range []bool{false, true} {
		link380CSearchConnectEnabled.Store(preview)
		for _, pid := range []uint16{0x24c7, 0x24c8, 0x24c9, 0x24ca, 0x24e9, 0x2e50, 0x2e51, 0x2e56, 0x2e57, 0xffff} {
			device := &jabra_DeviceInfo{productID: pid, isDongle: true}
			before := *device
			check, ok := pairingAccessCheck(device)
			if !ok || check.Feature != "pairing access" || check.State != "INFO" || !strings.Contains(check.Detail, "Policy check only; no device query sent.") || strings.Contains(check.Detail, link380CSearchConnectAck) {
				t.Fatal(pid, preview, check)
			}
			if !reflect.DeepEqual(*device, before) {
				t.Fatal("policy check changed device", pid)
			}
			enabled := pid == 0x24c7 || pid == 0x24c8 || pid == 0x2e57 || pid == 0x24c9 && preview
			if enabled != strings.Contains(check.Detail, "are enabled") {
				t.Fatal(pid, preview, check)
			}
			if pid == 0x2e56 {
				policy := pairingPolicyFor(device)
				if policy.Reads || policy.SearchConnect || policy.RememberedWrites || !strings.Contains(check.Detail, "no opt-in exists") || !strings.Contains(check.Detail, "2e56") {
					t.Fatal(policy, check)
				}
			}
		}
	}
	for _, device := range []*jabra_DeviceInfo{nil, {}, {productID: 0x2e56}} {
		if _, ok := pairingAccessCheck(device); ok {
			t.Fatal("non-dongle received pairing row")
		}
	}
}

func TestDiagnosticClosingChecksKeepHardwareLimits(t *testing.T) {
	checks := diagnosticClosingChecks(&jabra_DeviceInfo{isDongle: true, productID: 0x2e56})
	var features []string
	for _, check := range checks {
		features = append(features, check.Feature)
		if check.Feature != "pairing access" && check.State != "NOT TESTED" {
			t.Fatal(check)
		}
	}
	if len(checks) != 8 {
		t.Fatal(checks)
	}
	for _, feature := range []string{"pairing access", "setting writes and read-back", "pairing/reset", "firmware installation/recovery", "microphone and speaker audio quality", "button/wheel behavior", "meeting-app call control", "USB reconnect and power cycle"} {
		if !slices.Contains(features, feature) {
			t.Fatal("missing original diagnostic limit", feature)
		}
	}
	if got := diagnosticClosingChecks(&jabra_DeviceInfo{}); len(got) != 7 {
		t.Fatal(got)
	}
}

type pairingPolicyReportAPI struct {
	jabraAPIBridge
	includePolicy bool
}

func (*pairingPolicyReportAPI) ListDevices() []ipc.DeviceInfo {
	return []ipc.DeviceInfo{{ID: 7, PID: 0x2e56, IsDongle: true, Connection: "usb", Selected: true}}
}
func (a *pairingPolicyReportAPI) DiagnoseDevice(uint16) ([]ipc.DiagnosticCheck, error) {
	checks := diagnosticClosingChecks(&jabra_DeviceInfo{isDongle: true, productID: 0x2e56})
	if !a.includePolicy {
		checks = checks[1:]
	}
	return checks, nil
}
func TestNativeReportShowsPairingPolicyWithoutChangingNextSteps(t *testing.T) {
	var reports []string
	for _, includePolicy := range []bool{false, true} {
		client := policyIPCClient(t, &pairingPolicyReportAPI{includePolicy: includePolicy})
		var report bytes.Buffer
		writeNativeDiagnostic(&report, client)
		reports = append(reports, report.String())
	}
	if !strings.Contains(reports[1], "pairing access") || !strings.Contains(reports[1], "no opt-in exists") || strings.Contains(reports[0], "pairing access") {
		t.Fatal(reports)
	}
	if !slices.Equal(reportNextSteps(reports[0]), reportNextSteps(reports[1])) {
		t.Fatal("policy row changed health advice", reports)
	}
}
