package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type resetFlowFake struct {
	target             link390Identity
	now                time.Time
	returnAfter        time.Duration
	sentAt             time.Time
	writes, opens      int
	attempts           map[string]bool
	sendErr            error
	wrongIdentity      bool
	before, after      link390ResetSummary
	outcomes           []link390ResetOutcome
	openHook           func(*resetFlowFake, bool) error
	resetHook          func()
	returnedGeneration string
}

func newResetFlowFake() *resetFlowFake {
	target := newConfigFlowFake().id
	target.Firmware = "1.3.0"
	low, normal := byte(2), byte(1)
	return &resetFlowFake{target: target, now: time.Unix(0, 0), returnAfter: 12 * time.Second, attempts: map[string]bool{}, before: link390ResetSummary{WirelessRange: &low}, after: link390ResetSummary{WirelessRange: &normal, Pairings: resetPairingSummary{Nameless: 1, Complete: true}}}
}
func (f *resetFlowFake) Now() time.Time { return f.now }
func (f *resetFlowFake) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.now = f.now.Add(d)
	return nil
}
func (f *resetFlowFake) BeginResetAttempt(target link390Identity) error {
	if f.attempts[target.Generation] {
		return errors.New("reset already attempted")
	}
	f.attempts[target.Generation] = true
	return nil
}
func (f *resetFlowFake) SetResetOutcome(_ link390Identity, outcome link390ResetOutcome) {
	f.outcomes = append(f.outcomes, outcome)
}
func (f *resetFlowFake) OpenReset(_ link390Identity, readOnly bool) (link390ResetSession, error) {
	f.opens++
	if f.openHook != nil {
		if err := f.openHook(f, readOnly); err != nil {
			return nil, err
		}
	}
	identity := f.target
	if f.writes > 0 && f.now.Sub(f.sentAt) >= f.returnAfter {
		identity.Generation = f.returnedGeneration
		if identity.Generation == "" {
			identity.Generation = "attachment-B"
		}
		identity.HIDPath = "/dev/hidraw8"
		if f.wrongIdentity {
			identity.USBSerial = "OTHER"
			identity.GNPSerial = "OTHER"
		}
	}
	return &resetFlowSession{f: f, identity: identity, readOnly: readOnly}, nil
}

type resetFlowSession struct {
	f        *resetFlowFake
	identity link390Identity
	readOnly bool
}

func (s *resetFlowSession) Identity() (link390Identity, error) { return s.identity, nil }
func (s *resetFlowSession) Attachment() (string, error)        { return s.identity.Generation, nil }
func (s *resetFlowSession) Summary() link390ResetSummary {
	if s.readOnly {
		return s.f.after
	}
	return s.f.before
}
func (s *resetFlowSession) Reset() error {
	if s.readOnly {
		return errors.New("replay on read-only session")
	}
	s.f.writes++
	s.f.sentAt = s.f.now
	if s.f.resetHook != nil {
		s.f.resetHook()
	}
	return s.f.sendErr
}
func (s *resetFlowSession) Close() {}

func TestLink390ResetOutcomesAndNoReplay(t *testing.T) {
	for _, test := range []struct {
		name     string
		change   func(*resetFlowFake)
		want     link390ResetOutcome
		returned bool
	}{
		{"restart", func(*resetFlowFake) {}, resetRestartVerified, true},
		{"NAK", func(f *resetFlowFake) { f.sendErr = errors.New("GNP NAK 9") }, resetRejected, false},
		{"lost ACK and return", func(f *resetFlowFake) { f.sendErr = errors.New("ACK timeout") }, resetUnknown, true},
		{"lost ACK and absent", func(f *resetFlowFake) { f.sendErr = errors.New("ACK timeout"); f.returnAfter = time.Hour }, resetUnknown, false},
		{"ACK but no observed restart", func(f *resetFlowFake) { f.returnAfter = time.Hour }, resetRequested, false},
		{"wrong returned serial", func(f *resetFlowFake) { f.wrongIdentity = true }, resetUnknown, false},
		{"range NAK", func(f *resetFlowFake) { f.after.WirelessRange = nil }, resetRestartVerified, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newResetFlowFake()
			test.change(f)
			result := runLink390Reset(context.Background(), f, f.target)
			if result.Outcome != test.want || result.Reconnected != test.returned || !result.Attempted || f.writes != 1 {
				t.Fatal(result, f.writes)
			}
			if result.Reconnected && f.now.Sub(f.sentAt) < 13*time.Second {
				t.Fatal("two spaced identity reads skipped")
			}
			retry := runLink390Reset(context.Background(), f, f.target)
			if retry.Attempted || f.writes != 1 {
				t.Fatal("same attachment reset replayed", retry, f.writes)
			}
			public := ipcLink390ResetResult(result)
			for _, word := range []string{"erased", "cleared", "complete", "factory-clean"} {
				if strings.Contains(strings.ToLower(public.Message), word) {
					t.Fatal("overclaimed result", public)
				}
			}
		})
	}
}
func TestLink390ResetExactScopeBeforeOpen(t *testing.T) {
	for _, mutate := range []func(*link390Identity){
		func(i *link390Identity) { i.ProductID = 0x2e56 }, func(i *link390Identity) { i.ProductID = 0x24c7 },
		func(i *link390Identity) { i.Firmware = "3.2.0" }, func(i *link390Identity) { i.Firmware = "1.4.2" },
		func(i *link390Identity) { i.Variant = "04-11" }, func(i *link390Identity) { i.USBSerial = "" },
		func(i *link390Identity) { i.GNPSerial = "wrong" }, func(i *link390Identity) { i.Port = "relative" },
	} {
		f := newResetFlowFake()
		target := f.target
		mutate(&target)
		result := runLink390Reset(context.Background(), f, target)
		if result.Outcome != resetRefused || f.opens != 0 || f.writes != 0 {
			t.Fatal(result, f.opens, f.writes)
		}
	}
}
func TestLink390ResetCancelAndTransientReturn(t *testing.T) {
	f := newResetFlowFake()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := runLink390Reset(ctx, f, f.target); result.Attempted || f.opens != 0 {
		t.Fatal(result)
	}
	f = newResetFlowFake()
	failOnce := true
	f.openHook = func(f *resetFlowFake, readOnly bool) error {
		if readOnly && f.now.Sub(f.sentAt) == 13*time.Second && failOnce {
			failOnce = false
			return errors.New("temporary open error")
		}
		return nil
	}
	result := runLink390Reset(context.Background(), f, f.target)
	if result.Outcome != resetRestartVerified || f.now.Sub(f.sentAt) < 15*time.Second {
		t.Fatal("count survived a failed open", result, f.now.Sub(f.sentAt))
	}
}
func TestLink390ResetSummaryDoesNotClaimEverySetting(t *testing.T) {
	f := newResetFlowFake()
	f.before.WirelessRange = f.after.WirelessRange
	result := runLink390Reset(context.Background(), f, f.target)
	checks := link390ResetChecks(result)
	if checks[0].State != "not-proven" || checks[1].State != "pass" {
		t.Fatal(checks)
	}
	result.After.Pairings.Complete = false
	if checks := link390ResetChecks(result); checks[1].State != "unavailable" {
		t.Fatal("partial list passed", checks)
	}
}
func TestResetPairingSummaryCountsNamelessAndRejectsTruncation(t *testing.T) {
	record := make([]byte, 12)
	record[6] = 1
	query := func(op byte, request []byte) ([]byte, error) {
		if op == gnpOpGetDBName {
			return []byte{0xff, 0xff}, nil
		}
		return record, nil
	}
	got, err := readPairingSummary([]byte{0}, query)
	if err != nil || !got.Complete || got.Named != 0 || got.Nameless != 1 {
		t.Fatal(got, err)
	}
	got, err = readPairingSummary([]byte{0, 1}, func(op byte, request []byte) ([]byte, error) {
		if request[2] == 1 {
			return nil, errors.New("namespace failed")
		}
		return query(op, request)
	})
	if err == nil || got.Complete {
		t.Fatal("partial namespace accepted", got, err)
	}
	for _, bad := range [][]byte{nil, make([]byte, 11), make([]byte, 12)} {
		got, err = readPairingSummary([]byte{0}, func(op byte, request []byte) ([]byte, error) {
			if op == gnpOpGetDBName {
				return []byte{0xff, 0xff}, nil
			}
			return bad, nil
		})
		if err == nil || got.Complete {
			t.Fatal("missing record accepted", got, err)
		}
	}
}

func FuzzResetPairingSummary(f *testing.F) {
	// Two-byte terminal name marker and one address-only record at index zero.
	f.Add([]byte{2, 0xff, 0xff, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0})
	f.Add([]byte{})
	f.Add([]byte{1, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		var name, record []byte
		if len(data) > 0 {
			size := int(data[0]) % 33
			if size > len(data)-1 {
				size = len(data) - 1
			}
			name, record = data[1:1+size], data[1+size:]
		}
		queries := 0
		summary, err := readPairingSummary([]byte{0, 1}, func(op byte, _ []byte) ([]byte, error) {
			queries++
			if queries > 1024 {
				t.Fatal("unbounded pairing traversal")
			}
			if op == gnpOpGetDBName {
				return name, nil
			}
			return record, nil
		})
		if summary.Complete && err != nil {
			t.Fatal("incomplete replies passed", summary, err)
		}
		if summary.Named < 0 || summary.Nameless < 0 || summary.Connected < 0 || summary.Named+summary.Nameless > 512 || summary.Connected > summary.Named+summary.Nameless {
			t.Fatal("invalid summary counts", summary)
		}
	})
}

func TestLink390ResetCancelAfterSendDoesNotReplay(t *testing.T) {
	fake := newResetFlowFake()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.resetHook = cancel
	result := runLink390Reset(ctx, fake, fake.target)
	if !result.Attempted || result.Outcome != resetRequested || fake.writes != 1 || !errors.Is(result.cause, context.Canceled) {
		t.Fatal(result, fake.writes)
	}
}
func TestLink390ResetTwoReadsMustUseSameReturnedGeneration(t *testing.T) {
	fake := newResetFlowFake()
	fake.openHook = func(f *resetFlowFake, readOnly bool) error {
		if readOnly && f.now.Sub(f.sentAt) >= 13*time.Second {
			f.returnedGeneration = "attachment-C"
		}
		return nil
	}
	result := runLink390Reset(context.Background(), fake, fake.target)
	if result.Outcome != resetRestartVerified || result.Identity.Generation != "attachment-C" || fake.now.Sub(fake.sentAt) < 14*time.Second || fake.writes != 1 {
		t.Fatal(result, fake.now.Sub(fake.sentAt), fake.writes)
	}
}
