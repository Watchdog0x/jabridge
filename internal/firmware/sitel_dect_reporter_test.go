package firmware

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These are semantic fixtures from issue #62's three docked reports and one
// undocked report, not captured packets. Serials are synthetic. Packet encoding
// follows the existing management peer; the reports do not contain raw traffic.
type reporterDECTPeer struct {
	*engage75ManagementPeer
	undocked bool
	rejected map[[2]byte]bool
}

func (p *reporterDECTPeer) Write(_ context.Context, packet []byte) error {
	if len(packet) < 7 || packet[0] != 5 || packet[4] != 0x46 || packet[5] != 2 {
		return errors.New("reporter fixture accepts identity reads only")
	}
	address, op := packet[1], packet[6]
	if p.rejected[[2]byte{address, op}] || p.undocked && address == 10 {
		p.queue = append(p.queue, []byte{5, 0, address, packet[3], 0xc5, 0xfe})
		return nil
	}
	var data []byte
	text := ""
	switch {
	case address == 1 && op == 0x11:
		data = []byte{0x13, 0x11}
	case address == 1 && op == 0x13:
		data = []byte{0x11, 0x11}
	case address == 1 && op == 0x14:
		data = []byte{4}
	case address == 10 && op == 1:
		text = p.world.headSerial
	case op == 2 && address == 1:
		data = []byte{1, 0, 0x1b}
	case op == 2 && address == 10:
		data = []byte{1, 1, 0x48}
	case op == 2 && address == 3:
		data = []byte{1, 3, 4}
	case op == 3 && (address == 1 || address == 2 || address == 3 || address == 10):
		text = "4.4.1"
	case address == 1 && (op == 6 || op == 9):
		text = "4.4.1"
	case address == 1 && op == 7:
		data = []byte{7}
	case (address == 1 || address == 10) && op == 0x22:
		data = []byte{1}
	case (address == 1 || address == 10) && op == 0x21:
		text = "1.7.7"
	default:
		return fmt.Errorf("unobserved reporter query %d/%02x", address, op)
	}
	if text != "" {
		data = append([]byte{byte(len(text))}, []byte(text)...)
	}
	reply := []byte{5, 0, address, packet[3], 0xc0 | byte(6+len(data)), 2, op}
	p.queue = append(p.queue, append(reply, data...))
	return nil
}

func newReporterDECT(undocked bool) (*engage75World, *reporterDECTPeer, USBDevice) {
	w := newEngage75World(syntheticEngage75Archive(), 0x35)
	w.base.pid = 0x1113
	w.headSerial = "REPORTER_HEAD_SERIAL_SENTINEL"
	device := w.base.device()
	device.Serial = "REPORTER_USB_SERIAL_SENTINEL"
	return w, &reporterDECTPeer{
		engage75ManagementPeer: &engage75ManagementPeer{world: w}, undocked: undocked,
		rejected: map[[2]byte]bool{{10, 0x11}: true, {10, 0x13}: true, {3, 0x11}: true, {3, 1}: true, {3, 0x13}: true},
	}, device
}

func TestDECTReporterDockedAndUndockedReplies(t *testing.T) {
	for _, undocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("undocked=%v", undocked), func(t *testing.T) {
			w, peer, device := newReporterDECT(undocked)
			transcript := &dectTranscriptIO{next: peer}
			report, err := inspectDECTComponents(context.Background(), device, transcript, func() error { return nil }, time.Second, time.Second)
			if err != nil || report.IdentityPassed {
				t.Fatalf("identity must remain blocked: %+v %v", report, err)
			}
			if !strings.Contains(report.InstallerIdentity, "headset (address 10) product ID (0x11): rejected") {
				t.Fatal("wrong first blocker", report.InstallerIdentity)
			}
			text := fmt.Sprintf("%+v", report)
			if undocked {
				if report.HeadsetPresenceState != "BLOCKED" || !strings.Contains(text, "no successful headset identity read at address 10; check docking and connection") {
					t.Fatal("undocked report needs qualified dock advice", text)
				}
			} else {
				if report.HeadsetPresenceState != "INFO" {
					t.Error("version evidence must not authorize installation", report.HeadsetPresenceState)
				}
				for _, want := range []string{"answered its version (4.4.1)", "docked per its version reply", "measured", "headset 10 boot ID (0x13), display 3 product ID (0x11), display 3 serial (0x01), display 3 boot ID (0x13)"} {
					if !strings.Contains(text, want) {
						t.Errorf("missing %q from report: %s", want, text)
					}
				}
				if strings.Contains(text, "check that the headset is docked") {
					t.Error("a rejected identity read is not evidence of undocking")
				}
			}
			if strings.Contains(text, device.Serial) || strings.Contains(text, w.headSerial) {
				t.Fatal("serial leaked")
			}
			want := [][2]byte{{1, 0x11}, {1, 0x11}, {1, 2}, {1, 3}, {1, 0x13}, {1, 0x14}, {10, 0x11}, {10, 1}, {10, 2}, {10, 3}, {10, 0x13}, {3, 0x11}, {3, 1}, {3, 2}, {3, 3}, {3, 0x13}, {2, 3}, {1, 9}, {1, 6}, {1, 7}, {10, 0x22}, {10, 0x21}, {1, 0x22}, {1, 0x21}}
			var got [][2]byte
			for _, packet := range transcript.packets {
				got = append(got, [2]byte{packet[1], packet[6]})
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("query set/order changed: got %v want %v", got, want)
			}
			if w.entries != 0 || w.saved != nil || len(w.boots) != 0 || w.base.erases+w.head.erases+w.mmi.erases != 0 {
				t.Fatal("diagnostics changed firmware or recovery")
			}
		})
	}
}

func TestDECTReporterPresenceUnknownWithoutVersion(t *testing.T) {
	_, peer, device := newReporterDECT(false)
	peer.rejected[[2]byte{10, 3}] = true
	report, err := inspectDECTComponents(context.Background(), device, peer, func() error { return nil }, time.Second, time.Second)
	if err != nil || report.IdentityPassed || report.HeadsetPresenceState != "INFO" || !strings.Contains(report.HeadsetPresence, "presence unknown") || strings.Contains(report.HeadsetPresence, "docked") {
		t.Fatalf("partial identity must not establish docking or authorize installation: %+v %v", report, err)
	}
	empty := DECTComponentReport{}
	empty.summarizeEvidence(nil)
	if empty.HeadsetPresenceState != "INFO" || !strings.Contains(empty.HeadsetPresence, "presence unknown") {
		t.Fatal("no reads must not be presented as an absent headset", empty)
	}
}

func TestDECTPresenceDoesNotDenyInvalidRepliesArrived(t *testing.T) {
	report := DECTComponentReport{Checks: []DECTComponentCheck{
		{Role: "headset", Address: 10, Query: "product ID (0x11)", State: "BLOCKED", Detail: "invalid product ID"},
		{Role: "headset", Address: 10, Query: "version (0x03)", State: "BLOCKED", Detail: "invalid version format"},
	}}
	report.summarizeEvidence(dectFailure("headset", 10, 0x11, dectValueError("invalid product ID")))
	if report.IdentityPassed || !strings.Contains(report.HeadsetPresence, "no successful headset identity read") || strings.Contains(report.HeadsetPresence, "no headset answer") {
		t.Fatal("invalid replies were labelled absent traffic", report)
	}
}

func TestDECTReporterLaterBlockersAfterBaseProbeFailure(t *testing.T) {
	_, peer, device := newReporterDECT(false)
	peer.rejected[[2]byte{1, 0x11}] = true
	peer.rejected[[2]byte{8, 0x11}] = true
	report, err := inspectDECTComponents(context.Background(), device, peer, func() error { return nil }, time.Second, time.Second)
	if err != nil || report.IdentityPassed || !strings.Contains(report.InstallerIdentity, "base identity does not match USB") {
		t.Fatalf("failed base must remain blocked: %+v %v", report, err)
	}
	for _, want := range []string{"headset 10 product ID (0x11)", "display 3 serial (0x01)"} {
		if !strings.Contains(report.LaterIdentityBlockers, want) {
			t.Errorf("later blocker missing after INFO base probes: %s", want)
		}
	}
}

func TestDECTReporterPublicResultFixture(t *testing.T) {
	w, peer, device := newReporterDECT(false)
	report, err := inspectDECTComponents(context.Background(), device, peer, func() error { return nil }, time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for i := range report.Checks {
		report.Checks[i].ElapsedMS = 0
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(device.Serial)) || bytes.Contains(data, []byte(w.headSerial)) {
		t.Fatal("reporter fixture leaks a synthetic serial")
	}
	expected, err := os.ReadFile("testdata/dect-reporter-public-result.json")
	if err != nil {
		t.Fatalf("Missing reporter fixture; GENERATED_FIXTURE=%s", base64.StdEncoding.EncodeToString(data))
	}
	if strings.TrimSpace(string(expected)) != string(data) {
		t.Fatal("reporter fixture must match the real inspection result")
	}
}

type reporterDECTBackend struct {
	*sitelDECTWorld
}

func (b reporterDECTBackend) runtime(ctx context.Context, _ USBDevice, archive *sitelDECTArchive) (sitelDECTIdentity, error) {
	_, peer, device := newReporterDECT(false)
	return retryDECTIdentity(ctx, func(ctx context.Context) (sitelDECTIdentity, error) {
		return (&sitelRuntime{io: peer}).identifyDECT(ctx, device, archive)
	}, func(context.Context) error {
		return context.DeadlineExceeded
	})
}

func TestDECTReporterInstallerStillStopsBeforeWrites(t *testing.T) {
	w, peer, device := newReporterDECT(false)
	_, err := (&sitelRuntime{io: peer}).identifyDECT(context.Background(), device, w.archive)
	var failure *dectIdentityError
	if !errors.As(err, &failure) || failure.role != "headset" || failure.address != 10 || failure.op != 0x11 || !errors.Is(err, errSitelRejected) {
		t.Fatal("installer guard changed", err)
	}
	if strings.Contains(err.Error(), "dock") {
		t.Error("per-query error gives false docking advice", err)
	}
	state := dectTestState(w.sitelDECTWorld)
	err = runSitelDECTInstall(context.Background(), reporterDECTBackend{w.sitelDECTWorld}, device, w.archive, &state, func() error {
		t.Error("unexpected recovery save")
		return nil
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "Run jabridge debug for a read-only component check.") || strings.Contains(err.Error(), "dock") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("missing actionable identity failure", err)
	}
	if w.entries != 0 || w.saved != nil || len(w.boots) != 0 || w.base.erases+w.head.erases+w.mmi.erases != 0 {
		t.Fatal("blocked installer changed firmware or recovery")
	}
}
