package firmware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

type dectIdentityFaultIO struct {
	peer             *engage75ManagementPeer
	address, op      byte
	occurrence, seen int
	fault            string
	readTimeout      bool
}

func (p *dectIdentityFaultIO) Write(ctx context.Context, packet []byte) error {
	match := len(packet) > 6 && packet[1] == p.address && packet[6] == p.op
	if match {
		p.seen++
	}
	match = match && p.seen == p.occurrence
	if match {
		switch p.fault {
		case "timeout":
			return context.DeadlineExceeded
		case "read-timeout":
			p.readTimeout = true
			return nil
		case "nak":
			return errSitelRejected
		case "transport":
			return errors.New("fixture transport failure")
		}
	}
	if err := p.peer.Write(ctx, packet); err != nil {
		return err
	}
	if match && p.fault == "extra-byte" {
		i := len(p.peer.queue) - 1
		p.peer.queue[i][4]++
		p.peer.queue[i] = append(p.peer.queue[i], 0)
	}
	return nil
}
func (p *dectIdentityFaultIO) Read(ctx context.Context) ([]byte, error) {
	if p.readTimeout {
		<-ctx.Done()
		p.readTimeout = false
		return nil, ctx.Err()
	}
	return p.peer.Read(ctx)
}

func TestDECTIdentityErrorsNameComponent(t *testing.T) {
	for _, tc := range []struct {
		name        string
		address, op byte
		occurrence  int
		fault       string
		want        []string
		dock        bool
	}{
		{"headset-timeout", 10, 0x11, 1, "timeout", []string{"headset", "address 10", "0x11", "timeout"}, true},
		{"base-second-PID", 1, 0x11, 2, "extra-byte", []string{"base", "address 1", "0x11", "3 bytes, expected 2"}, false},
		{"display-boot-rejected", 3, 0x13, 1, "nak", []string{"engage 75 display identity:", "display", "address 3", "0x13", "rejected"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := syntheticEngage75Archive()
			w := newEngage75World(archive, 0x35)
			io := &dectIdentityFaultIO{peer: &engage75ManagementPeer{world: w}, address: tc.address, op: tc.op, occurrence: tc.occurrence, fault: tc.fault}
			_, err := (&sitelRuntime{io: io}).identifyDECT(context.Background(), w.base.device(), archive)
			if err == nil {
				t.Fatal("fault was accepted")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q in %v", want, err)
				}
			}
			if strings.Contains(err.Error(), "dock") != tc.dock {
				t.Error("dock advice attributed to wrong component", err)
			}
			if tc.fault == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Error("identity lost underlying timeout", err)
			}
			if tc.fault == "nak" && !errors.Is(err, errSitelRejected) {
				t.Error("identity lost rejection cause", err)
			}
			if w.entries != 0 || len(w.boots) != 0 || w.saved != nil || w.base.erases+w.head.erases+w.mmi.erases != 0 {
				t.Fatal("identity check changed firmware or recovery")
			}
		})
	}
}

type dectTranscriptIO struct {
	next    csrStageIO
	packets [][]byte
}

func (p *dectTranscriptIO) Write(ctx context.Context, data []byte) error {
	p.packets = append(p.packets, append([]byte(nil), data...))
	return p.next.Write(ctx, data)
}
func (p *dectTranscriptIO) Read(ctx context.Context) ([]byte, error) { return p.next.Read(ctx) }

func TestDECTInspectionKeepsStrictIdentityAndContinues(t *testing.T) {
	for _, tc := range []struct {
		addr, op    byte
		n           int
		fault, role string
	}{
		{10, 0x11, 1, "read-timeout", "headset"}, {1, 0x11, 2, "read-timeout", "base"},
		{1, 0x11, 2, "extra-byte", "base"}, {10, 0x11, 1, "nak", "headset"},
		{3, 0x13, 1, "nak", "display"}, {10, 2, 1, "transport", "headset"},
	} {
		t.Run(fmt.Sprintf("%d/%d/%s", tc.addr, tc.op, tc.fault), func(t *testing.T) {
			archive := syntheticEngage75Archive()
			w := newEngage75World(archive, 0x35)
			fault := &dectIdentityFaultIO{peer: &engage75ManagementPeer{world: w}, address: tc.addr, op: tc.op, occurrence: tc.n, fault: tc.fault}
			transcript := &dectTranscriptIO{next: fault}
			report, err := inspectDECTComponents(context.Background(), w.base.device(), transcript, func() error { return nil }, time.Millisecond, 100*time.Millisecond)
			if err != nil || report.IdentityPassed {
				t.Fatalf("fault accepted or report lost: %+v %v", report, err)
			}
			for _, want := range []string{tc.role, fmt.Sprintf("address %d", tc.addr), fmt.Sprintf("0x%02x", tc.op)} {
				if !strings.Contains(report.InstallerIdentity, want) {
					t.Error("wrong stop", report.InstallerIdentity, want)
				}
			}
			display, radio := false, false
			for _, check := range report.Checks {
				display = display || check.Role == "display"
				radio = radio || check.Role == "radio"
			}
			if !display || !radio {
				t.Fatal("independent components omitted")
			}
			counts := map[[2]byte]int{}
			for _, packet := range transcript.packets {
				if packet[4] != 0x46 || packet[5] != 2 || !allowedDECTIdentityQuery(packet[1], packet[6], w.base.device().Serial != "") {
					t.Fatal("non-read packet", packet[:7])
				}
				counts[[2]byte{packet[1], packet[6]}]++
			}
			for key, count := range counts {
				limit := 1
				if key[1] == 0x11 && (key[0] == 1 || key[0] == 8) {
					limit = 2
				}
				if count > limit {
					t.Fatal("step resent", key, count)
				}
			}
			if w.entries != 0 || len(w.boots) != 0 || w.saved != nil || w.base.erases+w.head.erases+w.mmi.erases != 0 {
				t.Fatal("inspection changed firmware")
			}
		})
	}
}

func TestDECTInspectionSuccessfulAttemptAndSerialRedaction(t *testing.T) {
	w := newEngage75World(syntheticEngage75Archive(), 0x35)
	w.headSerial = "PRIVATE_COMPONENT_SERIAL"
	device := w.base.device()
	device.Serial = "PRIVATE_USB_SERIAL"
	transcript := &dectTranscriptIO{next: &engage75ManagementPeer{world: w}}
	report, err := inspectDECTComponents(context.Background(), device, transcript, func() error { return nil }, time.Millisecond, 100*time.Millisecond)
	if err != nil || !report.IdentityPassed {
		t.Fatalf("valid attempt refused: %+v %v", report, err)
	}
	if strings.Contains(fmt.Sprint(report), "PRIVATE_") {
		t.Fatal("serial leaked")
	}
	counts := map[[2]byte]int{}
	for _, p := range transcript.packets {
		counts[[2]byte{p[1], p[6]}]++
	}
	if counts[[2]byte{1, 0x11}] != 2 || counts[[2]byte{8, 0x11}] != 0 || counts[[2]byte{1, 1}] != 0 {
		t.Fatal("base query plan changed", counts)
	}
	if w.saved != nil || w.entries != 0 {
		t.Fatal("inspection prepared an installation")
	}
}

func TestDECTInspectionRejectsCommandsBeforeWriting(t *testing.T) {
	for _, change := range []string{"class", "flags", "op", "address"} {
		t.Run(change, func(t *testing.T) {
			w := newEngage75World(syntheticEngage75Archive(), 0x35)
			record := &dectTranscriptIO{next: &engage75ManagementPeer{world: w}}
			guard := &dectReadOnlyIO{next: record, validate: func() error { return nil }}
			p := make([]byte, 64)
			copy(p, []byte{5, 1, 0, 1, 0x46, 2, 0x11})
			switch change {
			case "class":
				p[5] = 7
			case "flags":
				p[4] = 0x86
			case "op":
				p[6] = 0x80
			case "address":
				p[1] = 4
			}
			if err := guard.Write(context.Background(), p); !errors.Is(err, errManagementNotSent) || len(record.packets) != 0 {
				t.Fatal("unsafe query reached IO", err)
			}
		})
	}
}

func TestDECTInspectionDiscardsEverChangedDevice(t *testing.T) {
	w := newEngage75World(syntheticEngage75Archive(), 0x35)
	count := 0
	record := &dectTranscriptIO{next: &engage75ManagementPeer{world: w}}
	report, err := inspectDECTComponents(context.Background(), w.base.device(), record, func() error {
		count++
		if count == 5 {
			return errors.New("changed")
		}
		return nil
	}, time.Millisecond, 100*time.Millisecond)
	if !errors.Is(err, ErrDECTInspectionChanged) || report.InstallerIdentity != "" || len(report.Checks) != 0 {
		t.Fatal("changed device kept a verdict", report, err)
	}
	if len(record.packets) > 2 {
		t.Fatal("queries continued after replacement", len(record.packets))
	}
}

func TestDECTInspectionRejectsUnsupportedWithoutIO(t *testing.T) {
	for _, pid := range []uint16{0x1111, 0x2e57, 0x24c9, 0xffff} {
		calls := 0
		_, err := inspectDECTComponents(context.Background(), USBDevice{VendorID: JabraVendorID, ProductID: pid}, nil, func() error { calls++; return nil }, time.Second, time.Second)
		if err == nil || calls != 0 {
			t.Fatal("unsupported target reached binding", pid, calls)
		}
	}
}

func TestDECTReadinessErrorRetainsFirstAndLast(t *testing.T) {
	n := 0
	_, err := retryDECTIdentity(context.Background(), func(context.Context) (sitelDECTIdentity, error) {
		n++
		if n == 1 {
			return sitelDECTIdentity{}, dectFailure("headset", 10, 0x11, context.DeadlineExceeded)
		}
		return sitelDECTIdentity{}, dectFailure("base", 1, 0x11, errors.New("transport"))
	}, func(context.Context) error {
		if n == 2 {
			return context.DeadlineExceeded
		}
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "first failure: DECT headset (address 10)") || !strings.Contains(err.Error(), "last failure: DECT base (address 1)") {
		t.Fatal("readiness lost its earlier cause", err)
	}
}

func TestDECTProductIDDecoderKeepsExactGuard(t *testing.T) {
	for _, tc := range []struct {
		data  []byte
		valid bool
	}{{nil, false}, {[]byte{0x13}, false}, {[]byte{0x13, 0x11}, true}, {[]byte{0x13, 0x11, 0}, false}, {[]byte{0, 0}, false}, {[]byte{255, 255}, false}} {
		_, err := decodeDECTProductID(tc.data)
		if (err == nil) != tc.valid {
			t.Fatal("product ID guard changed", len(tc.data), tc.valid, err)
		}
	}
}
func TestDECTInspectionBudgetAndRiskState(t *testing.T) {
	state := t.TempDir()
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", state)
	risk := commandLineRiskAccepted.Load()
	w := newEngage75World(syntheticEngage75Archive(), 0x35)
	fault := &dectIdentityFaultIO{peer: &engage75ManagementPeer{world: w}, address: 1, op: 0x11, occurrence: 1, fault: "read-timeout"}
	record := &dectTranscriptIO{next: fault}
	start := time.Now()
	report, err := inspectDECTComponents(context.Background(), w.base.device(), record, func() error { return nil }, time.Second, 5*time.Millisecond)
	if err != nil || report.IdentityPassed || time.Since(start) > 250*time.Millisecond || len(record.packets) != 1 {
		t.Fatal("inspection exceeded budget or replayed", err, time.Since(start), len(record.packets))
	}
	if commandLineRiskAccepted.Load() != risk {
		t.Fatal("inspection changed hardware-write risk state")
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatal("inspection created a recovery file", entries, err)
	}
}
func TestDECTInspectionReportsMissingRadioWithoutInventingIdentity(t *testing.T) {
	w := newEngage75World(syntheticEngage75Archive(), 0x35)
	w.radioVersion = ""
	report, err := inspectDECTComponents(context.Background(), w.base.device(), &engage75ManagementPeer{world: w}, func() error { return nil }, time.Millisecond, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range report.Checks {
		if check.Role == "radio" {
			found = true
			if check.State == "PASS" {
				t.Fatal("missing radio marked as read", check)
			}
		}
	}
	if !found {
		t.Fatal("radio observation missing")
	}
}
