package firmware

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type dectAssessmentPeer struct {
	ident   *reporterDECTPeer
	info    *dectInfoPeer
	current csrStageIO
	packets [][]byte
	mutate  func([]byte, []byte) []byte
}

func (p *dectAssessmentPeer) Write(ctx context.Context, packet []byte) error {
	p.packets = append(p.packets, append([]byte(nil), packet...))
	p.current = p.ident
	if packet[5] == 15 {
		p.current = p.info
	}
	return p.current.Write(ctx, packet)
}
func (p *dectAssessmentPeer) Read(ctx context.Context) ([]byte, error) {
	data, err := p.current.Read(ctx)
	if err == nil && p.mutate != nil {
		data = p.mutate(p.packets[len(p.packets)-1], data)
	}
	return data, err
}

func newDECTAssessmentPeer() (*engage75World, *dectAssessmentPeer, USBDevice) {
	w, ident, device := newReporterDECT(false)
	peer := &dectAssessmentPeer{ident: ident, info: &dectInfoPeer{fault: "rejected", address: 3}}
	peer.mutate = func(request, reply []byte) []byte {
		if request[1] == 10 && request[5] == 15 {
			// Normal management replies include report ID 5; their GNP
			// payload starts one byte later than the HP test replies.
			if peer.info.fault != "geometry" {
				binary.LittleEndian.PutUint32(reply[15:], 65536)
			}
			binary.LittleEndian.PutUint32(reply[19:], 1001)
		}
		return reply
	}
	return w, peer, device
}

func TestDECTRuntimeAssessmentKeepsMissingDisplayUnverified(t *testing.T) {
	w, peer, device := newDECTAssessmentPeer()
	assessment, err := (&sitelRuntime{io: peer}).assessDECTRuntime(context.Background(), device, w.archive, func() error { return nil })
	var failure *dectRuntimeAssessmentError
	if !errors.As(err, &failure) || assessment.State != dectAssessmentNeedsBootloaderIdentity || !strings.Contains(err.Error(), "display image identity needs bootloader verification") {
		t.Fatal("recognized old firmware was not staged safely", assessment.String(), err)
	}
	if assessment.Headset.PID.State != dectRejected || assessment.Headset.PID.Value != 0 || assessment.Headset.DFUPID.State != dectRejected || assessment.Headset.DFUPID.Value != 0 {
		t.Fatal("image ID fabricated a runtime PID/DFUPID")
	}
	if assessment.Headset.Image.State != dectMeasured || assessment.Headset.Image.Value.ID != 0x0b0e1116 || assessment.Display.Image.State != dectRejected || assessment.Display.Image.Value.ID != 0 || assessment.Display.ExpectedImageID != 0x0b0e1117 {
		t.Fatal("expected and measured image identities were confused")
	}
	if assessment.Identity.Headset.PID != 0 || assessment.Identity.Headset.BootPID != 0 || assessment.Identity.MMI.PID != 0 || assessment.Identity.MMI.BootPID != 0 || assessment.Identity.MMI.Serial != "" {
		t.Fatal("partial identity filled unsupported fields")
	}
	if strings.Contains(err.Error(), w.headSerial) || strings.Contains(err.Error(), device.Serial) {
		t.Fatal("assessment error leaked private identity")
	}
	if bindSitelDECTRecovery(&firmwareRecoveryState{}, assessment.Identity, device, w.archive) == nil {
		t.Fatal("incomplete assessment authorized recovery binding")
	}
	for _, packet := range peer.packets {
		if packet[4] != 0x46 || packet[5] != 2 && (packet[5] != 15 || packet[6] != 0) {
			t.Fatalf("assessment sent a state-changing command: %x", packet)
		}
	}
}

type dectAssessmentBackend struct {
	*engage75World
	peer     *dectAssessmentPeer
	device   USBDevice
	assessed dectRuntimeAssessment
}

func (b *dectAssessmentBackend) runtime(ctx context.Context, _ USBDevice, archive *sitelDECTArchive) (sitelDECTIdentity, error) {
	assessment, err := (&sitelRuntime{io: b.peer}).assessDECTRuntime(ctx, b.device, archive, func() error { return nil })
	b.assessed = assessment
	return assessment.Identity, err
}

func TestDECTRuntimeAssessmentStopsActualInstallBeforeEntryOrSave(t *testing.T) {
	w, peer, device := newDECTAssessmentPeer()
	backend := &dectAssessmentBackend{engage75World: w, peer: peer, device: device}
	state := dectTestState(w.sitelDECTWorld)
	saves := 0
	err := runSitelDECTInstall(context.Background(), backend, device, w.archive, &state, func() error { saves++; return nil }, nil)
	if err == nil || backend.assessed.State != dectAssessmentNeedsBootloaderIdentity || saves != 0 || w.entries != 0 || len(w.boots) != 0 || w.base.erases+w.head.erases+w.mmi.erases != 0 || w.saved != nil {
		t.Fatal("incomplete planning crossed into installation", err, saves, w.entries)
	}
	calls, pauses := 0, 0
	_, retryErr := retryDECTIdentity(context.Background(), func(context.Context) (sitelDECTIdentity, error) { calls++; return backend.assessed.Identity, err }, func(context.Context) error { pauses++; return nil })
	if retryErr != err || calls != 1 || pauses != 0 {
		t.Fatal("definite missing bootloader identity was retried", calls, pauses, retryErr)
	}
}

func TestDECTRuntimeAssessmentRejectsBadImageAndBinding(t *testing.T) {
	for _, fault := range []string{"wrong-id", "timeout", "geometry", "mode", "missing-serial", "changed"} {
		t.Run(fault, func(t *testing.T) {
			w, peer, device := newDECTAssessmentPeer()
			if fault == "missing-serial" {
				peer.ident.rejected[[2]byte{10, 1}] = true
			} else if fault != "changed" {
				peer.info.fault, peer.info.address = fault, 10
			}
			validateCalls := 0
			validate := func() error {
				validateCalls++
				if fault == "changed" && validateCalls == 3 {
					return errors.New("temporary replacement")
				}
				return nil
			}
			assessment, err := (&sitelRuntime{io: peer}).assessDECTRuntime(context.Background(), device, w.archive, validate)
			if err == nil || assessment.State != dectAssessmentBlocked {
				t.Fatal("bad evidence qualified for continuation", assessment.String(), err)
			}
			if fault == "changed" && (!errors.Is(err, ErrDECTInspectionChanged) || assessment.Headset.Image.State != dectNotAttempted) {
				t.Fatal("stale evidence survived a changed binding", err)
			}
		})
	}
}

func TestDECTRuntimeAssessmentPreservesStrictSupportedPath(t *testing.T) {
	w := newEngage75World(syntheticEngage75Archive(), 0x35)
	direct := &dectTranscriptIO{next: &engage75ManagementPeer{world: w}}
	wanted, err := (&sitelRuntime{io: direct}).identifyDECT(context.Background(), w.base.device(), w.archive)
	if err != nil {
		t.Fatal(err)
	}
	assessed := &dectTranscriptIO{next: &engage75ManagementPeer{world: w}}
	plan, err := (&sitelRuntime{io: assessed}).assessDECTRuntime(context.Background(), w.base.device(), w.archive, func() error { return nil })
	if err != nil || plan.State != dectAssessmentComplete || plan.Identity.Base.PID != wanted.Base.PID || plan.Identity.Headset.BootPID != wanted.Headset.BootPID {
		t.Fatal("supported identity changed", plan.String(), err)
	}
	if len(direct.packets) != len(assessed.packets) {
		t.Fatal("supported path gained queries", len(direct.packets), len(assessed.packets))
	}
	for i := range direct.packets {
		if string(direct.packets[i]) != string(assessed.packets[i]) {
			t.Fatal("strict supported wire sequence changed", i)
		}
	}
}

func TestDECTRuntimeAssessmentDiscardsStrictIdentityOnPostReadChange(t *testing.T) {
	for _, oldFirmware := range []bool{false, true} {
		w, peer, device := newDECTAssessmentPeer()
		var input csrStageIO = peer
		if !oldFirmware {
			w = newEngage75World(syntheticEngage75Archive(), 0x35)
			input = &engage75ManagementPeer{world: w}
			device = w.base.device()
		}
		calls := 0
		validate := func() error {
			calls++
			if calls == 2 {
				return errors.New("replacement immediately after strict reads")
			}
			return nil
		}
		assessment, err := (&sitelRuntime{io: input}).assessDECTRuntime(context.Background(), device, w.archive, validate)
		if !errors.Is(err, ErrDECTInspectionChanged) || assessment.State != dectAssessmentBlocked || !reflect.DeepEqual(assessment.Identity, sitelDECTIdentity{}) || assessment.Headset.Image.State != dectNotAttempted {
			t.Fatalf("strict identity survived a changed binding (old=%t): state=%d identity=%+v error=%v", oldFirmware, assessment.State, assessment.Identity, err)
		}
	}
}

func TestDECTRuntimeAssessmentRejectsWrongScopeOrRegion(t *testing.T) {
	for _, fault := range []string{"base-variant", "head-variant", "display-variant", "region", "strict-timeout"} {
		t.Run(fault, func(t *testing.T) {
			w, peer, device := newDECTAssessmentPeer()
			original := peer.mutate
			peer.mutate = func(request, reply []byte) []byte {
				reply = original(request, reply)
				if request[5] != 2 {
					return reply
				}
				if request[6] == 2 && (fault == "base-variant" && request[1] == 1 || fault == "head-variant" && request[1] == 10 || fault == "display-variant" && request[1] == 3) {
					reply[9] ^= 1
				}
				if fault == "region" && request[1] == 1 && request[6] == 7 {
					reply[7] = 6
				}
				return reply
			}
			var input csrStageIO = peer
			if fault == "strict-timeout" {
				input = &dectAssessmentHeadTimeout{dectAssessmentPeer: peer}
			}
			plan, err := (&sitelRuntime{io: input}).assessDECTRuntime(context.Background(), device, w.archive, func() error { return nil })
			if err == nil || plan.State != dectAssessmentBlocked {
				t.Fatal("unsupported evidence allowed continuation", fault, plan.String(), err)
			}
			if fault == "strict-timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("timeout became a compatibility NAK", err)
			}
		})
	}
}

type dectAssessmentHeadTimeout struct{ *dectAssessmentPeer }

func (p *dectAssessmentHeadTimeout) Write(ctx context.Context, packet []byte) error {
	if packet[1] == 10 && packet[5] == 2 && packet[6] == 0x11 {
		return context.DeadlineExceeded
	}
	return p.dectAssessmentPeer.Write(ctx, packet)
}
