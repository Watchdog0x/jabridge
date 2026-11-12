package firmware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

type dectPlanStep struct {
	role                              string
	address, op                       byte
	ordinal                           int
	usbSerialAbsent, regionDifference bool
}
type dectPlanPeer struct {
	peer    *engage75ManagementPeer
	target  dectPlanStep
	fault   string
	alias8  bool
	counts  map[[2]byte]int
	packets [][]byte
}

func (p *dectPlanPeer) Write(ctx context.Context, request []byte) error {
	p.packets = append(p.packets, append([]byte(nil), request...))
	address, op := request[1], request[6]
	key := [2]byte{address, op}
	p.counts[key]++
	fault := address == p.target.address && op == p.target.op && p.counts[key] == p.target.ordinal
	if fault {
		switch p.fault {
		case "timeout":
			return context.DeadlineExceeded
		case "nak":
			return errSitelRejected
		}
	}
	forwarded := append([]byte(nil), request...)
	if address == 8 && p.alias8 {
		forwarded[1] = 1
	}
	if err := p.peer.Write(ctx, forwarded); err != nil {
		return err
	}
	index := len(p.peer.queue) - 1
	reply := p.peer.queue[index]
	if address == 8 && p.alias8 {
		reply[2] = 8
	}
	if fault {
		data := append([]byte(nil), reply[7:]...)
		switch p.fault {
		case "short":
			data = []byte{0x13}
		case "wrong":
			switch op {
			case 0x11:
				data = append(data, 0)
			case 0x13:
				data = []byte{0x12, 0x11}
			case 1:
				data = []byte{1, 'X', 'Y'}
			case 2:
				data = []byte{1, 2}
			case 3, 6, 9, 0x21:
				data = append([]byte{7}, []byte("invalid")...)
			case 0x14:
				data = []byte{9}
			case 7:
				data = []byte{1}
			case 0x22:
				data = []byte{2}
			}
		}
		reply = append(append([]byte(nil), reply[:7]...), data...)
		reply[4] = 0xc0 | byte(6+len(data))
	}
	p.peer.queue[index] = reply
	return nil
}
func (p *dectPlanPeer) Read(ctx context.Context) ([]byte, error) { return p.peer.Read(ctx) }
func newDECTPlanPeer(w *engage75World, step dectPlanStep, fault string, alias8 bool) *dectPlanPeer {
	return &dectPlanPeer{peer: &engage75ManagementPeer{world: w}, target: step, fault: fault, alias8: alias8, counts: map[[2]byte]int{}}
}

func TestDECTInstallerAndInspectionFaultEquivalence(t *testing.T) {
	steps := []dectPlanStep{{"base", 1, 0x11, 1, false, false}, {"base", 1, 0x11, 2, false, false}, {"base", 1, 1, 1, true, false}, {"base", 1, 2, 1, false, false}, {"base", 1, 3, 1, false, false}, {"base", 1, 0x13, 1, false, false}, {"base", 1, 0x14, 1, false, false},
		{"headset", 10, 0x11, 1, false, false}, {"headset", 10, 1, 1, false, false}, {"headset", 10, 2, 1, false, false}, {"headset", 10, 3, 1, false, false}, {"headset", 10, 0x13, 1, false, false},
		{"display", 3, 0x11, 1, false, false}, {"display", 3, 1, 1, false, false}, {"display", 3, 2, 1, false, false}, {"display", 3, 3, 1, false, false}, {"display", 3, 0x13, 1, false, false},
		{"radio", 2, 3, 1, false, false}, {"base", 1, 9, 1, false, false}, {"base", 1, 6, 1, false, false}, {"base", 1, 7, 1, false, false}, {"headset", 10, 0x22, 1, false, true}, {"headset", 10, 0x21, 1, false, false}, {"base", 1, 0x22, 1, false, true}, {"base", 1, 0x21, 1, false, false}}
	for _, step := range steps {
		for _, fault := range []string{"timeout", "nak", "wrong"} {
			t.Run(fmt.Sprintf("%s/%d/%02x/%d/%s", step.role, step.address, step.op, step.ordinal, fault), func(t *testing.T) {
				a := syntheticEngage75Archive()
				oldWorld := newEngage75World(a, 0x35)
				device := oldWorld.base.device()
				if step.usbSerialAbsent {
					device.Serial = ""
				}
				oldIO := newDECTPlanPeer(oldWorld, step, fault, false)
				_, identityErr := (&sitelRuntime{io: oldIO}).identifyDECT(context.Background(), device, a)
				world := newEngage75World(a, 0x35)
				probeIO := newDECTPlanPeer(world, step, fault, false)
				report, err := inspectDECTComponents(context.Background(), device, probeIO, func() error { return nil }, time.Millisecond, 250*time.Millisecond)
				if err != nil {
					t.Fatal(err)
				}
				radioTolerated := step.role == "radio" && (fault == "timeout" || fault == "nak")
				archiveOnlyRegion := step.regionDifference && fault == "wrong"
				switch {
				case radioTolerated:
					if identityErr != nil || report.IdentityPassed || !strings.Contains(report.InstallerIdentity, "new install would stop") {
						t.Fatal("radio recovery-only distinction lost", identityErr, report.InstallerIdentity)
					}
				case archiveOnlyRegion:
					if identityErr == nil || !report.IdentityPassed {
						t.Fatal("archive-specific region exception changed", identityErr, report.InstallerIdentity)
					}
				default:
					if (identityErr == nil) != report.IdentityPassed {
						t.Fatal("identity verdict drift", identityErr, report.InstallerIdentity)
					}
					if identityErr != nil {
						var detail *dectIdentityError
						if !errors.As(identityErr, &detail) || !strings.Contains(report.InstallerIdentity, detail.Error()) {
							t.Fatal("stopping role/address/query/cause drift", identityErr, report.InstallerIdentity)
						}
					}
				}
				for key, count := range probeIO.counts {
					limit := 1
					if key[1] == 0x11 && (key[0] == 1 || key[0] == 8) {
						limit = 2
					}
					if count > limit {
						t.Fatal("query step replayed", key, count)
					}
				}
				for _, w := range []*engage75World{world, oldWorld} {
					if w.entries != 0 || len(w.boots) != 0 || w.saved != nil || w.base.erases+w.head.erases+w.mmi.erases != 0 {
						t.Fatal("identity check changed firmware")
					}
				}
			})
		}
	}
}

func TestDECTInspectionExactLogicalTranscriptAndBaseFallback(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		for _, serial := range []bool{false, true} {
			t.Run(fmt.Sprintf("fallback%t/serial%t", fallback, serial), func(t *testing.T) {
				w := newEngage75World(syntheticEngage75Archive(), 0x35)
				device := w.base.device()
				if !serial {
					device.Serial = ""
				}
				step := dectPlanStep{}
				fault := ""
				base := byte(1)
				if fallback {
					step = dectPlanStep{address: 1, op: 0x11, ordinal: 1}
					fault = "timeout"
					base = 8
				}
				record := newDECTPlanPeer(w, step, fault, fallback)
				report, err := inspectDECTComponents(context.Background(), device, record, func() error { return nil }, time.Millisecond, time.Second)
				if err != nil || !report.IdentityPassed {
					t.Fatal(report.InstallerIdentity, err)
				}
				want := [][2]byte{{1, 0x11}}
				if fallback {
					want = append(want, [2]byte{8, 0x11})
				}
				want = append(want, [2]byte{base, 0x11})
				if !serial {
					want = append(want, [2]byte{base, 1})
				}
				want = append(want, [][2]byte{{base, 2}, {base, 3}, {base, 0x13}, {base, 0x14}, {10, 0x11}, {10, 1}, {10, 2}, {10, 3}, {10, 0x13}, {3, 0x11}, {3, 1}, {3, 2}, {3, 3}, {3, 0x13}, {2, 3}, {base, 9}, {base, 6}, {base, 7}, {10, 0x22}, {10, 0x21}, {base, 0x22}, {base, 0x21}}...)
				if len(record.packets) != len(want) {
					t.Fatal("logical step count changed", len(record.packets), len(want))
				}
				for i, p := range record.packets {
					if p[1] != want[i][0] || p[6] != want[i][1] {
						t.Fatal("logical step order changed", i, p[:7], want[i])
					}
				}
			})
		}
	}
}

func TestDECTBadSecondBasePIDSkipsBaseAndQueriesIndependentParts(t *testing.T) {
	for _, fault := range []string{"timeout", "wrong", "short"} {
		w := newEngage75World(syntheticEngage75Archive(), 0x35)
		record := newDECTPlanPeer(w, dectPlanStep{address: 1, op: 0x11, ordinal: 2}, fault, false)
		report, err := inspectDECTComponents(context.Background(), w.base.device(), record, func() error { return nil }, time.Millisecond, time.Second)
		if err != nil || report.IdentityPassed {
			t.Fatal(report, err)
		}
		for _, p := range record.packets {
			if p[1] == 1 && p[6] != 0x11 {
				t.Fatal("unidentified base queried again", p[:7])
			}
		}
		if record.counts[[2]byte{10, 3}] != 1 || record.counts[[2]byte{3, 3}] != 1 || record.counts[[2]byte{2, 3}] != 1 {
			t.Fatal("independent reads not performed", record.counts)
		}
		for _, c := range report.Checks {
			if c.Role == "base" && c.Step != "base probe" && c.Query != "product ID (0x11)" && c.State != "NOT TESTED" {
				t.Fatal("unidentified base step claimed tested", c)
			}
		}
	}
}

func TestDECTMissingRadioBlocksFreshInstallButNotIdentityRecovery(t *testing.T) {
	w := newEngage75World(syntheticEngage75Archive(), 0x35)
	w.radioVersion = ""
	report, err := inspectDECTComponents(context.Background(), w.base.device(), &engage75ManagementPeer{world: w}, func() error { return nil }, time.Millisecond, time.Second)
	if err != nil || report.IdentityPassed || !strings.Contains(report.InstallerIdentity, "only an unfinished, bound recovery") {
		t.Fatal(report.InstallerIdentity, err)
	}
	state := dectTestState(w.sitelDECTWorld)
	err = w.run(context.Background(), &state)
	if err == nil || !strings.Contains(err.Error(), "radio version is unavailable") || w.entries != 0 || w.saved != nil || w.base.erases+w.head.erases+w.mmi.erases != 0 {
		t.Fatal("fresh radio precondition changed", err)
	}
}
func TestDECTTransportCausesRemainDistinctAndPrivate(t *testing.T) {
	first := dectFailure("headset", 10, 0x11, &os.PathError{Op: "read", Path: "/PRIVATE_SERIAL/private", Err: syscall.ENODEV})
	last := dectFailure("headset", 10, 0x11, syscall.EIO)
	n := 0
	_, err := retryDECTIdentity(context.Background(), func(context.Context) (sitelDECTIdentity, error) {
		n++
		if n == 1 {
			return sitelDECTIdentity{}, first
		}
		return sitelDECTIdentity{}, last
	}, func(context.Context) error {
		if n == 2 {
			return context.DeadlineExceeded
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "no such device") || !strings.Contains(err.Error(), "input/output error") || !strings.Contains(err.Error(), "first failure") || !strings.Contains(err.Error(), "last failure") || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal(err)
	}
	if !errors.Is(first, syscall.ENODEV) {
		t.Fatal("transport cause lost")
	}
	a := dectFailure("base", 1, 0x11, errors.New("PRIVATE_A"))
	b := dectFailure("base", 1, 0x11, errors.New("PRIVATE_B"))
	if sameDECTFailure(a, b) || strings.Contains(a.Error(), "PRIVATE") {
		t.Fatal("unclassified failures collapsed or leaked")
	}
}
