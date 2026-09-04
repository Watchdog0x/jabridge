package main

import (
	"errors"
	"os"
	"strings"
	"testing"

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
