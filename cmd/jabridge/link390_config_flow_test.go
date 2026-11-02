package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type configFlowFake struct {
	attachmentFailureAfterWrite                       error
	id                                                link390Identity
	now                                               time.Time
	value                                             byte
	granted                                           bool
	enterErr, writeErr, endErr, readErr               error
	noApply, rebootWrite, rebootEnd, swap, detachRead bool
	opens, closes                                     int
	frames                                            []string
	openHook                                          func(*configFlowFake)
	cancelAfterWrite                                  context.CancelFunc
	readTimes                                         []time.Time
	endAt                                             time.Time
}

func newConfigFlowFake() *configFlowFake {
	return &configFlowFake{id: link390Identity{Port: "/usb/1-1", USBSerial: "SERIAL-A", GNPSerial: "SERIAL-A", Generation: "attachment-A", HIDPath: "/dev/hidraw0", VendorID: jabraVendorID, ProductID: 0x2e57, Firmware: "3.2.0", Variant: "04-10"}, now: time.Unix(0, 0), granted: true}
}
func (f *configFlowFake) Open(_ link390Identity, readOnly bool) (link390ConfigSession, error) {
	f.opens++
	if f.openHook != nil {
		f.openHook(f)
	}
	return &configFlowSession{f: f, readOnly: readOnly}, nil
}
func (f *configFlowFake) Now() time.Time { return f.now }
func (f *configFlowFake) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.now = f.now.Add(d)
	return nil
}

type configFlowSession struct {
	f                *configFlowFake
	readOnly, closed bool
}

func (s *configFlowSession) Identity() (link390Identity, error) { return s.f.id, nil }
func (s *configFlowSession) Attachment() (string, error) {
	if !s.readOnly && len(s.f.frames) >= 2 && s.f.attachmentFailureAfterWrite != nil {
		return "", s.f.attachmentFailureAfterWrite
	}
	return s.f.id.Generation, nil
}
func (s *configFlowSession) Read() (byte, error) {
	if s.readOnly {
		s.f.readTimes = append(s.f.readTimes, s.f.now)
	}
	if s.readOnly && s.f.readErr != nil {
		return 0, s.f.readErr
	}
	if s.readOnly && s.f.detachRead {
		s.f.id.Generation = "attachment-C"
		s.f.detachRead = false
	}
	return s.f.value, nil
}
func (s *configFlowSession) Enter() (bool, error) {
	s.f.frames = append(s.f.frames, "permission")
	return s.f.granted, s.f.enterErr
}
func (s *configFlowSession) Write(value byte) error {
	if s.readOnly {
		panic("write sent to a reopened handle")
	}
	s.f.frames = append(s.f.frames, fmt.Sprintf("write:%t@%s", value != 0, s.f.id.Generation))
	if !s.f.noApply {
		s.f.value = value
	}
	if s.f.rebootWrite {
		s.f.id.Generation = "attachment-B"
	}
	if s.f.swap {
		s.f.id.USBSerial = "SERIAL-B"
		s.f.id.GNPSerial = "SERIAL-B"
	}
	if s.f.cancelAfterWrite != nil {
		s.f.cancelAfterWrite()
	}
	return s.f.writeErr
}
func (s *configFlowSession) End() error {
	s.f.endAt = s.f.now
	if s.readOnly {
		panic("END sent to a reopened handle")
	}
	s.f.frames = append(s.f.frames, "end@"+s.f.id.Generation)
	if s.f.rebootEnd {
		s.f.id.Generation = "attachment-B"
	}
	return s.f.endErr
}
func (s *configFlowSession) Close() {
	if s.closed {
		panic("handle closed twice")
	}
	s.closed = true
	s.f.closes++
}

func TestLink390ConfigFlowConfirmsFreshReadbacks(t *testing.T) {
	f := newConfigFlowFake()
	target := f.id
	result, err := runLink390ConfigChange(context.Background(), f, target, "softphone-integration", 1, nil)
	if err != nil || result.Status != link390Applied || result.ReadBack == nil || *result.ReadBack != 1 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if strings.Join(f.frames, ",") != "permission,write:true@attachment-A,end@attachment-A" {
		t.Fatal(f.frames)
	}
	if f.opens != 3 || f.closes != 3 {
		t.Fatalf("expected initial handle and two fresh reads: opens=%d closes=%d", f.opens, f.closes)
	}
}

func TestLink390ConfigNoOpAndStaleEditSendNoConfiguration(t *testing.T) {
	for _, stale := range []bool{false, true} {
		f := newConfigFlowFake()
		f.value = 1
		expected := byte(0)
		var previous *byte
		if stale {
			previous = &expected
		}
		result, err := runLink390ConfigChange(context.Background(), f, f.id, "softphone-integration", 1, previous)
		if (err != nil) != stale || len(f.frames) != 0 || result.Attempted {
			t.Fatalf("stale=%t result=%+v error=%v frames=%v", stale, result, err, f.frames)
		}
		if f.opens != f.closes {
			t.Fatal("handle leak")
		}
	}
}

func TestLink390ConfigFailureAndCleanupStates(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*configFlowFake)
		want      link390ChangeStatus
		frames    string
	}{
		{"permission-denied", func(f *configFlowFake) { f.granted = false; f.enterErr = errors.New("permission NAK") }, link390NotApplied, "permission"},
		{"malformed-permission", func(f *configFlowFake) { f.enterErr = errors.New("invalid permission reply") }, link390NotApplied, "permission,end@attachment-A"},
		{"write-NAK", func(f *configFlowFake) { f.noApply = true; f.writeErr = errors.New("write NAK") }, link390NotApplied, "permission,write:true@attachment-A,end@attachment-A"},
		{"write-uncertain-but-value-changed", func(f *configFlowFake) { f.writeErr = errors.New("write timeout") }, link390Unknown, "permission,write:true@attachment-A,end@attachment-A"},
		{"END-NAK", func(f *configFlowFake) { f.endErr = errors.New("END NAK") }, link390Unknown, "permission,write:true@attachment-A,end@attachment-A"},
		{"unchanged-readback", func(f *configFlowFake) { f.noApply = true }, link390NotApplied, "permission,write:true@attachment-A,end@attachment-A"},
		{"unreadable-readback", func(f *configFlowFake) { f.readErr = errors.New("node exists but protocol is unresponsive") }, link390Unknown, "permission,write:true@attachment-A,end@attachment-A"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newConfigFlowFake()
			test.configure(f)
			result, err := runLink390ConfigChange(context.Background(), f, f.id, "softphone-integration", 1, nil)
			if err == nil || result.Status != test.want || strings.Join(f.frames, ",") != test.frames {
				t.Fatalf("result=%+v error=%v frames=%v", result, err, f.frames)
			}
			if f.opens != f.closes {
				t.Fatalf("opens=%d closes=%d", f.opens, f.closes)
			}
			if test.name == "unreadable-readback" && result.ReadBack != nil {
				t.Fatal("pre-write value published as readback")
			}
		})
	}
}

func TestLink390ConfigRebootUsesOnlyReadHandlesAfterWrite(t *testing.T) {
	for _, duringWrite := range []bool{false, true} {
		f := newConfigFlowFake()
		target := f.id
		if duringWrite {
			f.rebootWrite = true
		} else {
			f.rebootEnd = true
			f.endErr = errLink390Uncertain
		}
		result, err := runLink390ConfigChange(context.Background(), f, target, "softphone-integration", 1, nil)
		if err != nil || result.Status != link390Applied || result.Identity.Generation != "attachment-B" {
			t.Fatalf("result=%+v error=%v", result, err)
		}
		if duringWrite && len(f.frames) != 2 {
			t.Fatalf("END sent after the original attachment disappeared: %v", f.frames)
		}
		for _, frame := range f.frames {
			if strings.Contains(frame, "@attachment-B") {
				t.Fatalf("write sent after reattach: %v", f.frames)
			}
		}
	}
}

func TestLink390ConfigNeverAcceptsAReplacementOrMixedGeneration(t *testing.T) {
	f := newConfigFlowFake()
	target := f.id
	f.swap = true
	f.rebootWrite = true
	result, err := runLink390ConfigChange(context.Background(), f, target, "softphone-integration", 1, nil)
	if err == nil || result.Status != link390Unknown || len(f.frames) != 2 {
		t.Fatalf("replacement accepted: %+v error=%v frames=%v", result, err, f.frames)
	}
	f = newConfigFlowFake()
	target = f.id
	f.openHook = func(f *configFlowFake) {
		if f.opens == 2 {
			f.id.Generation = "attachment-B"
		}
		if f.opens == 3 {
			f.id.Generation = "attachment-C"
		}
	}
	result, err = runLink390ConfigChange(context.Background(), f, target, "softphone-integration", 1, nil)
	if err != nil || result.Status != link390Applied || f.opens != 4 || result.Identity.Generation != "attachment-C" {
		t.Fatalf("mixed-generation success: %+v error=%v opens=%d", result, err, f.opens)
	}
	f = newConfigFlowFake()
	target = f.id
	f.detachRead = true
	result, err = runLink390ConfigChange(context.Background(), f, target, "softphone-integration", 1, nil)
	if err != nil || f.opens != 4 {
		t.Fatalf("detach during read trusted: %+v error=%v opens=%d", result, err, f.opens)
	}
}

func TestLink390ConfigMissingIdentityAndCancellation(t *testing.T) {
	f := newConfigFlowFake()
	target := f.id
	target.USBSerial = ""
	if _, err := runLink390ConfigChange(context.Background(), f, target, "softphone-integration", 1, nil); err == nil || f.opens != 0 {
		t.Fatal("missing serial reached the device")
	}
	f = newConfigFlowFake()
	target = f.id
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.cancelAfterWrite = cancel
	result, err := runLink390ConfigChange(ctx, f, target, "softphone-integration", 1, nil)
	if err == nil || result.Status != link390Unknown || strings.Join(f.frames, ",") != "permission,write:true@attachment-A,end@attachment-A" {
		t.Fatalf("cancel bypassed cleanup: %+v error=%v frames=%v", result, err, f.frames)
	}
}

func TestLink390ConfigCannotTurnUncertainCleanupIntoSuccess(t *testing.T) {
	f := newConfigFlowFake()
	f.attachmentFailureAfterWrite = errors.New("temporary sysfs access failure")
	result, err := runLink390ConfigChange(context.Background(), f, f.id, "softphone-integration", 1, nil)
	if err == nil || result.Status != link390Unknown || len(f.frames) != 2 {
		t.Fatalf("cleanup uncertainty accepted: %+v %v %v", result, err, f.frames)
	}
	if result.ReadBack == nil || *result.ReadBack != 1 {
		t.Fatal("last readback was lost")
	}
}
