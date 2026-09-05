package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/firmware"
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
