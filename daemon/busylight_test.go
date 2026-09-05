package daemon

import (
	"errors"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

type failingSender struct{}

func (failingSender) SetBusylight(bool) error { return errors.New("device rejected command") }

type mockSender struct {
	calls []bool // true=on, false=off
}

func (m *mockSender) SetBusylight(on bool) error {
	m.calls = append(m.calls, on)
	return nil
}

func TestBusylightAutoMode_CallStartsAndEnds(t *testing.T) {
	sender := &mockSender{}
	ctrl := NewBusylightController(sender)

	// Start a call
	ctrl.OnCallStateChange(pipewire.CallState{InCall: true, AppName: "Firefox", Since: time.Now()})
	if !ctrl.IsOn() {
		t.Fatal("light should be ON after call started")
	}
	if len(sender.calls) != 1 || sender.calls[0] != true {
		t.Fatalf("sender calls = %v, want [true]", sender.calls)
	}

	// End the call
	ctrl.OnCallStateChange(pipewire.CallState{InCall: false})
	if ctrl.IsOn() {
		t.Fatal("light should be OFF after call ended")
	}
	if len(sender.calls) != 2 || sender.calls[1] != false {
		t.Fatalf("sender calls = %v, want [true, false]", sender.calls)
	}
}

func TestBusylightAutoMode_NoDoubleToggle(t *testing.T) {
	sender := &mockSender{}
	ctrl := NewBusylightController(sender)

	// Two call-started events in a row
	ctrl.OnCallStateChange(pipewire.CallState{InCall: true, AppName: "Zoom"})
	ctrl.OnCallStateChange(pipewire.CallState{InCall: true, AppName: "Zoom"})

	if len(sender.calls) != 1 {
		t.Fatalf("sender called %d times, want 1 (no double toggle)", len(sender.calls))
	}
}

func TestBusylightManualOn(t *testing.T) {
	sender := &mockSender{}
	ctrl := NewBusylightController(sender)
	if err := ctrl.SetMode(BusylightOn); err != nil {
		t.Fatal(err)
	}

	if !ctrl.IsOn() {
		t.Fatal("light should be ON in manual-on mode")
	}

	// Call state changes should be ignored
	ctrl.OnCallStateChange(pipewire.CallState{InCall: false})
	if !ctrl.IsOn() {
		t.Fatal("light should stay ON in manual-on mode even when call ends")
	}
}

func TestBusylightManualOff(t *testing.T) {
	sender := &mockSender{}
	ctrl := NewBusylightController(sender)

	// Start in auto, turn on via call
	ctrl.OnCallStateChange(pipewire.CallState{InCall: true, AppName: "Teams"})
	if !ctrl.IsOn() {
		t.Fatal("light should be ON")
	}

	// Switch to manual off
	if err := ctrl.SetMode(BusylightOff); err != nil {
		t.Fatal(err)
	}
	if ctrl.IsOn() {
		t.Fatal("light should be OFF after switching to off mode")
	}

	// Call state changes should be ignored
	ctrl.OnCallStateChange(pipewire.CallState{InCall: true, AppName: "Teams"})
	if ctrl.IsOn() {
		t.Fatal("light should stay OFF in off mode")
	}
}

func TestBusylightModeSwitch(t *testing.T) {
	sender := &mockSender{}
	ctrl := NewBusylightController(sender)

	if ctrl.Mode() != BusylightAuto {
		t.Errorf("default mode = %v, want auto", ctrl.Mode())
	}

	if err := ctrl.SetMode(BusylightOn); err != nil {
		t.Fatal(err)
	}
	if ctrl.Mode() != BusylightOn {
		t.Errorf("mode = %v, want on", ctrl.Mode())
	}
}

func TestBusylightModeRollsBackOnHardwareError(t *testing.T) {
	ctrl := NewBusylightController(failingSender{})
	if err := ctrl.SetMode(BusylightOn); err == nil {
		t.Fatal("hardware error was swallowed")
	}
	if ctrl.Mode() != BusylightAuto || ctrl.IsOn() {
		t.Fatal("controller state changed after failed hardware write")
	}
}

func TestParseBusylightMode(t *testing.T) {
	cases := []struct {
		input string
		want  BusylightMode
		err   bool
	}{
		{"auto", BusylightAuto, false},
		{"on", BusylightOn, false},
		{"off", BusylightOff, false},
		{"invalid", BusylightOff, true},
	}
	for _, c := range cases {
		got, err := ParseBusylightMode(c.input)
		if (err != nil) != c.err {
			t.Errorf("ParseBusylightMode(%q) error = %v, want err=%v", c.input, err, c.err)
		}
		if got != c.want {
			t.Errorf("ParseBusylightMode(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}
