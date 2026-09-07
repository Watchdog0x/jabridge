package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/firmware"
	"github.com/Watchdog0x/jabridge/internal/history"
	"golang.org/x/sys/unix"
)

func TestDiagnosticDoesNotLeakOSPaths(t *testing.T) {
	err := &os.PathError{Op: "open", Path: "/home/private-user/secret-device", Err: os.ErrPermission}
	got := diagnosticError(err)
	if !strings.Contains(got, "permission denied") || strings.Contains(got, "private-user") {
		t.Fatalf("unsafe diagnostic: %s", got)
	}
	if got := diagnosticError(errors.New("serial ABC123 /home/name")); got != "failed" {
		t.Fatal(got)
	}
}

func TestServiceDiagnosticWhitelistsFieldsAndExplainsNamespaceFailure(t *testing.T) {
	got := formatServiceDiagnostic([]byte("ActiveState=failed\nExecMainStatus=226\nEnvironment=SECRET=value\nExecStart=/home/person/jabridge\nResult=exit-code\n"))
	if !strings.Contains(got, "namespace setup failed") || strings.Contains(got, "SECRET") || strings.Contains(got, "/home") {
		t.Fatal(got)
	}
}

func TestCapabilityStartupFailureIsNotReportedAsDevicePermissionOrTimeout(t *testing.T) {
	state := formatServiceDiagnostic([]byte("ActiveState=activating\nSubState=auto-restart\nExecMainStatus=218\nProtectKernelModules=yes\nCapabilityBoundingSet=cap_chown cap_net_raw\nDropInPaths=/home/PRIVATE_NAME/secret.conf\nEnvironment=PRIVATE_TOKEN\n"))
	if !strings.Contains(state, "218/CAPABILITIES") || !strings.Contains(state, "before Jabridge started") || !strings.Contains(state, "UnitOverridesPresent=true") || strings.Contains(state, "PRIVATE") {
		t.Fatal(state)
	}
	err := serviceReadinessError(context.DeadlineExceeded, state)
	if history.Classify(err) != "service-capabilities" || strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatal(err)
	}
	steps := strings.Join(reportNextSteps("hidraw4: read/write access ready\nJabra input event7: ready\n"+state), "\n")
	if !strings.Contains(steps, "corrected user unit") || strings.Contains(steps, "Device control access is denied") || strings.Contains(steps, "run jabridge setup on the host") {
		t.Fatal(steps)
	}
	categories := strings.Join(serviceFailureCategories("Failed to drop capabilities: Operation not permitted\nstatus=218/CAPABILITIES"), "\n")
	if !strings.Contains(categories, "service capability setup failure") {
		t.Fatal(categories)
	}
}

func TestMediaListenerDropsOrdinaryKeyboardAndRawEvents(t *testing.T) {
	for _, event := range []struct {
		kind, code uint16
		value      int32
	}{{unix.EV_KEY, 30, 1}, {unix.EV_MSC, 4, 999}, {unix.EV_REL, 0, 5}} {
		if got := mediaInputEvent(event.kind, event.code, event.value); got != "" {
			t.Fatal(got)
		}
	}
	if got := mediaInputEvent(unix.EV_KEY, 248, 1); got != "Microphone mute: pressed" {
		t.Fatal(got)
	}
	if got := mediaInputEvent(unix.EV_REL, 8, -1); got != "Volume wheel/dial: -1" {
		t.Fatal(got)
	}
}

func TestVendorControlCandidatesRequireBidirectionalVendorReports(t *testing.T) {
	reports := []firmware.HIDReport{
		{ID: 2, Kind: "input", Bytes: 33, Fields: []firmware.HIDField{{UsagePage: 0xff00}}},
		{ID: 2, Kind: "output", Bytes: 33, Fields: []firmware.HIDField{{UsagePage: 0xff00}}},
		{ID: 3, Kind: "input", Bytes: 3, Fields: []firmware.HIDField{{UsagePage: 0x000b}}},
		{ID: 3, Kind: "output", Bytes: 3, Fields: []firmware.HIDField{{UsagePage: 0x0008}}},
		{ID: 4, Kind: "input", Bytes: 3, Fields: []firmware.HIDField{{UsagePage: 0xff30}}},
	}
	got := vendorControlCandidates(reports)
	if len(got) != 1 || !strings.Contains(got[0], "report 2") || !strings.Contains(got[0], "input=33") || !strings.Contains(got[0], "pages=ff00") {
		t.Fatalf("candidates = %#v", got)
	}
}
