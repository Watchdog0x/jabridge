package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mkTestUpdater builds a CsrDfuUpdater with stubbed backends and a buffered
// event channel the test fills. Keeps every test self-contained — no real
// USB, no real dfu-util, no sysfs access.
func mkTestUpdater(events chan CsrDfuEvent) *CsrDfuUpdater {
	cfg := CsrDfuConfig{
		FirmwarePath:  "/tmp/fake.dfu",
		NormalVID:     0x0b0e,
		NormalPID:     0x2450,
		SwitchTimeout: 500 * time.Millisecond,
		FlashTimeout:  500 * time.Millisecond,
		SettleTimeout: 500 * time.Millisecond,
	}
	u := &CsrDfuUpdater{
		cfg:      cfg,
		state:    CsrStateIdle,
		events:   events,
		enterDFU: func(string) error { return nil },
		flashDFU: func(string, string) error { return nil },
		resetNow: func(string) error { return nil },
	}
	return u
}

// TestCsrDfuHappyPath walks the FSM through every expected transition
// using stubbed backends and injected USB events. This is the ONLY way
// we can validate the state machine without real hardware.
func TestCsrDfuHappyPath(t *testing.T) {
	events := make(chan CsrDfuEvent, 16)
	u := mkTestUpdater(events)

	// Drive a complete successful run by queueing events in the order
	// the FSM expects them. Send before calling Run so every wait
	// finds its match immediately.
	normalPath := "/sys/bus/usb/devices/1-2"
	dfuPath := "/sys/bus/usb/devices/1-3"
	events <- CsrDfuEvent{Attached: true, NormalMode: true, SysPath: normalPath, VendorID: 0x0b0e, ProductID: 0x2450}
	events <- CsrDfuEvent{Attached: false, NormalMode: true, SysPath: normalPath, VendorID: 0x0b0e, ProductID: 0x2450}
	events <- CsrDfuEvent{Attached: true, NormalMode: false, SysPath: dfuPath, VendorID: 0x0b0e, ProductID: 0xFFFF}
	events <- CsrDfuEvent{Attached: false, NormalMode: false, SysPath: dfuPath, VendorID: 0x0b0e, ProductID: 0xFFFF}
	events <- CsrDfuEvent{Attached: true, NormalMode: true, SysPath: normalPath, VendorID: 0x0b0e, ProductID: 0x2450}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res := u.Run(ctx)
	if res.Err != nil {
		t.Fatalf("Run returned error: %v", res.Err)
	}
	if res.FinalState != CsrStateComplete {
		t.Fatalf("FinalState = %s, want complete", res.FinalState)
	}

	// Verify the transition sequence is what we expect. The first entry
	// is the Idle transition Run makes at the top, then each step in
	// order through Complete.
	want := []CsrDfuState{
		CsrStateIdle,
		CsrStateNormalAttached,
		CsrStateSwitching,
		CsrStateNormalDetached,
		CsrStateDfuAttached,
		CsrStateFlashing,
		CsrStateDfuDetached,
		CsrStateComplete,
	}
	if len(res.Transitions) != len(want) {
		t.Fatalf("transition count = %d, want %d: %v", len(res.Transitions), len(want), res.Transitions)
	}
	for i, s := range want {
		if res.Transitions[i] != s {
			t.Errorf("transition[%d] = %s, want %s", i, res.Transitions[i], s)
		}
	}
}

// TestCsrDfuEnterDFUFails verifies that a failure in the mode-switch
// callback produces a failed result with the right transition history
// (idle → normal-attached → failed). This matches the real-world case
// today — our realEnterDFUMode stub returns an error until the command
// bytes are extracted.
func TestCsrDfuEnterDFUFails(t *testing.T) {
	events := make(chan CsrDfuEvent, 4)
	u := mkTestUpdater(events)
	u.enterDFU = func(string) error { return errors.New("switch failed") }

	events <- CsrDfuEvent{Attached: true, NormalMode: true, SysPath: "/x", VendorID: 0x0b0e, ProductID: 0x2450}

	res := u.Run(context.Background())
	if res.Err == nil {
		t.Fatal("expected error, got nil")
	}
	if res.FinalState != CsrStateFailed {
		t.Fatalf("FinalState = %s, want failed", res.FinalState)
	}
	// Should have transitioned Idle → NormalAttached → Failed.
	want := []CsrDfuState{CsrStateIdle, CsrStateNormalAttached, CsrStateFailed}
	if len(res.Transitions) != len(want) {
		t.Fatalf("transitions = %v, want %v", res.Transitions, want)
	}
	for i, s := range want {
		if res.Transitions[i] != s {
			t.Errorf("transition[%d] = %s, want %s", i, res.Transitions[i], s)
		}
	}
}

// TestCsrDfuDeviceNeverAppears verifies that a timeout on the initial
// device wait produces a fail result — the real-world "no device
// attached" case.
func TestCsrDfuDeviceNeverAppears(t *testing.T) {
	events := make(chan CsrDfuEvent, 1)
	u := mkTestUpdater(events)
	// Intentionally no events sent.

	res := u.Run(context.Background())
	if res.Err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if res.FinalState != CsrStateFailed {
		t.Fatalf("FinalState = %s, want failed", res.FinalState)
	}
}

// TestCsrDfuRealEnterDFUStub verifies that the production stub explicitly
// reports that the command is not implemented or hardware-validated. This is
// a tripwire against shipping an incomplete path as a working updater.
func TestCsrDfuRealEnterDFUStub(t *testing.T) {
	u := NewCsrDfuUpdater(DefaultCsrDfuConfig("/tmp/x.dfu", 0x0b0e, 0x2450), make(chan CsrDfuEvent))
	err := u.realEnterDFUMode("/sys/bus/usb/devices/1-2")
	if err == nil {
		t.Fatal("realEnterDFUMode returned nil for an unvalidated mode switch")
	}
	msg := err.Error()
	for _, needle := range []string{"not implemented", "hardware-validated"} {
		if !containsString(msg, needle) {
			t.Errorf("stub error %q is missing required substring %q", msg, needle)
		}
	}
}

// TestCsrDfuStateStrings guards the String() method so debug output stays
// stable. If someone renames a state without updating String, tests catch it.
func TestCsrDfuStateStrings(t *testing.T) {
	cases := []struct {
		state CsrDfuState
		want  string
	}{
		{CsrStateIdle, "idle"},
		{CsrStateNormalAttached, "normal-attached"},
		{CsrStateSwitching, "switching-to-dfu"},
		{CsrStateNormalDetached, "normal-detached"},
		{CsrStateDfuAttached, "dfu-attached"},
		{CsrStateFlashing, "flashing"},
		{CsrStateDfuDetached, "dfu-detached"},
		{CsrStateComplete, "complete"},
		{CsrStateFailed, "failed"},
	}
	for _, c := range cases {
		if got := c.state.String(); got != c.want {
			t.Errorf("state %d .String() = %q, want %q", int(c.state), got, c.want)
		}
	}
}

// containsString is a local helper — substring check without pulling
// "strings" into the test file's visible imports, matching the style of
// the existing firmware_test.go file.
func containsString(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
