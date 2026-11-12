package firmware

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type engage75PreviewWorld struct {
	*engage75World
	runtimeDevice                                             USBDevice
	assessmentPeer                                            *dectAssessmentPeer
	assessments                                               int
	pending                                                   engage75Pending
	recovery                                                  *firmwareRecoveryState
	phases                                                    []string
	entryErr                                                  error
	fault                                                     string
	packets                                                   [][]byte
	afterPacket                                               func([]byte)
	afterEntry                                                func()
	successfulSPIPackets                                      int
	nakReplies, ambiguousReturns, returnRequests              []byte
	entryCalls, finalChecks                                   int
	bootWithoutSerial, returnWithoutSerial, returnWrongSerial bool
	saveFailure                                               string
}

func newEngage75PreviewWorld(archive *sitelDECTArchive, revision uint16) *engage75PreviewWorld {
	w := newEngage75World(archive, revision)
	w.base.pid = 0x1113
	for _, d := range []*sitelTestDevice{w.base, w.head, w.mmi} {
		d.version = "4.4.1"
		d.writeSize = 1001
		d.sectorSize = 1024
		for kind, area := range d.areas {
			area.Size = (area.Size + 1023) / 1024 * 1024
			d.areas[kind] = area
		}
	}
	// Runtime reports decoded65536/1001; this model deliberately exposes
	// fresh loader1024/1001. Runtime geometry is not loader authorization.
	for _, image := range archive.Images {
		endpoint := w.base
		if image.File.GNPAddress == "10" {
			endpoint = w.head
		}
		if image.File.GNPAddress == "3" {
			endpoint = w.mmi
		}
		for _, seg := range image.Segments {
			sector := endpoint.testSectorSize()
			first := seg.Address / sector * sector
			end := (seg.Address + uint32(len(seg.Data)) + sector - 1) / sector * sector
			for address := first; address < end; address++ {
				if _, ok := endpoint.memory[address]; !ok {
					endpoint.memory[address] = 255
				}
			}
		}
	}
	_, ap, _ := newDECTAssessmentPeer()
	ap.ident.world = w
	w.headSerial = "REPORTER_HEAD_SERIAL_SENTINEL"
	device := w.base.device()
	device.attachment.realPath = device.SysPath
	device.attachment.bus = 1
	// A nonempty serial is measured independently in both USB modes by this
	// model; the real transition is deliberately not assumed to use this format.
	p := &engage75PreviewWorld{engage75World: w, runtimeDevice: device, assessmentPeer: ap}
	for _, endpoint := range []*sitelTestDevice{w.base, w.head, w.mmi} {
		endpoint.checkpoint = func() bool { return p.recovery != nil && p.recovery.Phase == "transfer" }
	}
	return p
}
func enableEngage75PreviewTest(t *testing.T) {
	t.Helper()
	old := Engage75Compatibility
	Engage75Compatibility = "enabled"
	previous := commandLineRiskAccepted.Swap(true)
	t.Cleanup(func() { Engage75Compatibility = old; commandLineRiskAccepted.Store(previous) })
}
func (w *engage75PreviewWorld) runtime(ctx context.Context, _ USBDevice, archive *sitelDECTArchive) (sitelDECTIdentity, error) {
	w.assessments++
	if w.fault == "head-swap" && w.assessments == 2 {
		w.headSerial = "different-original-head"
	}
	a, err := (&sitelRuntime{io: w.assessmentPeer}).assessDECTRuntime(ctx, w.runtimeDevice, archive, func() error { return nil })
	return a.Identity, err
}
func (w *engage75PreviewWorld) previewEnter(ctx context.Context, _ USBDevice) error {
	w.entryCalls++
	if w.pending.Entry != "intent" || w.pending.Phase != "entry-intent" {
		return errors.New("entry without durable intent")
	}
	if errors.Is(w.entryErr, errManagementNotSent) || errors.Is(w.entryErr, errSitelRejected) {
		return w.entryErr
	}
	if err := ctx.Err(); err != nil {
		return managementNotSent(err)
	}
	w.entries++
	w.base.bootMode, w.head.bootMode, w.mmi.bootMode = true, true, true
	if w.afterEntry != nil {
		w.afterEntry()
	}
	return w.entryErr
}
func (w *engage75PreviewWorld) wait(ctx context.Context, previous USBDevice, pid uint16) (USBDevice, error) {
	if w.fault == "wait-timeout" {
		return USBDevice{}, context.DeadlineExceeded
	}
	d, err := w.sitelDECTWorld.wait(ctx, previous, pid)
	if w.fault == "port-swap" {
		d.SysPath += "-other"
	}
	if w.fault == "serial-change" {
		d.Serial = "loader-serial-unqualified"
	}
	if err != nil {
		return d, err
	}
	d.attachment.realPath = d.SysPath
	d.attachment.bus = 1
	if pid == 0x1111 && w.bootWithoutSerial {
		d.Serial = ""
	}
	if pid == 0x1113 && w.returnWithoutSerial {
		d.Serial = ""
	}
	if pid == 0x1113 && w.returnWrongSerial {
		d.Serial = "other-runtime-serial"
	}
	return waitSitelDevice(ctx, engage75NativeWaitResult{device: d}, previous, pid)
}

type engage75NativeWaitResult struct {
	nativeSitelBackend
	device USBDevice
}

func (b engage75NativeWaitResult) wait(context.Context, USBDevice, uint16) (USBDevice, error) {
	return b.device, nil
}
func (w *engage75PreviewWorld) boot(ctx context.Context, _ USBDevice) (*sitelDECTConnection, error) {
	plain := &sitelBootPeer{world: w.base, endpoints: map[byte]*sitelTestDevice{1: w.base, 10: w.head, 3: w.mmi}, onBoot: func(address byte) error {
		if w.recovery == nil || w.recovery.Phase != "returning" || !w.recovery.SitelDECTV2.SettingsVerified {
			return errors.New("return before transfer verified")
		}
		w.boots = append(w.boots, address)
		if address == 10 {
			if w.fault != "head-ack-no-transition" {
				w.head.applicationMode = true
			}
			if w.fault == "head-return-wrong-image" {
				w.head.wrongImageID = true
			}
		}
		if address == 1 {
			w.mmi.version = w.mmi.wanted
			w.mmi.bootMode = false
		}
		return nil
	}}
	peer := &sitelBootPeer{world: w.base, onPacket: func(packet []byte) ([]byte, error) {
		w.packets = append(w.packets, slices.Clone(packet))
		if packet[5] == 5 && w.recovery != nil && w.recovery.Phase == "returning" {
			w.returnRequests = append(w.returnRequests, packet[0])
		}
		if len(w.boots) > 0 && packet[0] == 10 && packet[5] == 0 && w.fault == "head-return-info-timeout" {
			return nil, context.DeadlineExceeded
		}
		if packet[5] == 5 && (packet[0] == 10 && w.fault == "head-return-rejected" || packet[0] == 1 && w.fault == "base-return-rejected") {
			w.nakReplies = append(w.nakReplies, packet[0])
			return []byte{0, packet[0], packet[2], 0xc6, 0xfe, 1}, nil
		}
		if packet[4] == 15 && packet[5] >= 0x0b && packet[5] <= 0x15 {
			reply, err := simulateSitelSPIPacket(packet, w.engage75World)
			if err == nil {
				w.successfulSPIPackets++
				if w.afterPacket != nil {
					w.afterPacket(packet)
				}
			}
			return reply, err
		}
		role := map[byte]string{1: "base", 10: "head", 3: "display"}[packet[0]]
		if packet[5] == 0 {
			switch w.fault {
			case role + "-missing":
				return nil, errors.New("display NAK")
			case role + "-timeout":
				return nil, context.DeadlineExceeded
			}
		}
		reply, err := plain.handle(packet)
		if err == nil && packet[5] == 0 {
			if w.fault == role+"-wrong" {
				reply[6] ^= 1
			}
			if w.fault == role+"-malformed" {
				reply = reply[:len(reply)-1]
			}
			if w.fault == role+"-mode2" {
				reply[22] = 2
			}
			if w.fault == "head-mode3" && packet[0] == 10 {
				reply[22] = 3
			}
		}
		if w.afterPacket != nil {
			w.afterPacket(packet)
		}
		if err == nil && packet[5] == 5 && len(w.boots) > 0 && (w.fault == "return-no-reply" && packet[0] == 1 || w.fault == "head-return-no-reply" && packet[0] == 10) {
			w.ambiguousReturns = append(w.ambiguousReturns, packet[0])
			return nil, context.DeadlineExceeded
		}
		return reply, err
	}}
	layout := sitelHIDLayout{ReportID: 10, ReportBytes: 64, MaxMessage: 1024}
	link := &sitelLink{io: peer, in: layout, out: layout, timeout: 2 * time.Second}
	if err := link.start(ctx); err != nil {
		return nil, err
	}
	return &sitelDECTConnection{Root: &sitelRequester{link: link, address: 1, timeout: time.Second}, Close: func() error { return nil }, Validate: func() error {
		if w.fault == "binding-lost" || w.fault == "head-return-binding-lost" && len(w.boots) == 1 {
			return ErrDECTInspectionChanged
		}
		return nil
	}}, nil
}
func (w *engage75PreviewWorld) previewVerifyRuntime(ctx context.Context, device USBDevice, selected engage75SelectedIdentity, archive *sitelDECTArchive) error {
	w.finalChecks++
	p := &engage75PreviewFinalPeer{engage75ManagementPeer: &engage75ManagementPeer{world: w.engage75World}, fault: w.fault}
	return (&sitelRuntime{io: p}).verifyEngage75SelectedRuntime(ctx, device, selected, archive, func() error {
		if w.fault == "final-binding-lost" && p.imageRead {
			return ErrDECTInspectionChanged
		}
		return nil
	})
}

type engage75PreviewFinalPeer struct {
	*engage75ManagementPeer
	fault     string
	imageRead bool
}

func (p *engage75PreviewFinalPeer) Write(ctx context.Context, packet []byte) error {
	address, op := packet[1], packet[6]
	if packet[5] == 15 && address == 10 && op == 0 {
		p.imageRead = true
		if p.fault == "final-image-timeout" {
			return context.DeadlineExceeded
		}
		body := make([]byte, 19)
		binary.LittleEndian.PutUint32(body, 0x0b0e1116)
		binary.LittleEndian.PutUint32(body[4:], 0x100)
		// Changed Main geometry after upgrade is allowed; only its actual image
		// and reported Main mode must match this final observation.
		binary.LittleEndian.PutUint32(body[8:], 2048)
		binary.LittleEndian.PutUint32(body[12:], 512)
		body[16], body[17] = 1, 1
		if p.fault == "wrong-final-head-image" {
			body[0] ^= 1
		}
		if p.fault == "wrong-final-head-mode" {
			body[16] = 0
		}
		reply := []byte{5, 0, address, packet[3], 0xc0 | byte(6+len(body)), 15, 0}
		p.queue = append(p.queue, append(reply, body...))
		return nil
	}
	if address != 1 && op == 0x11 && p.fault != "new-runtime-pid" || address == 3 && op == 1 {
		p.queue = append(p.queue, []byte{5, 0, address, packet[3], 0xc5, 0xfe})
		return nil
	}
	if err := p.engage75ManagementPeer.Write(ctx, packet); err != nil {
		return err
	}
	reply := p.queue[len(p.queue)-1]
	if op == 2 {
		variant := map[byte][]byte{1: {1, 0, 0x1b}, 10: {1, 1, 0x48}, 3: {1, 3, 4}}[address]
		copy(reply[7:], variant)
	}
	if p.fault == "final-head-swap" && address == 10 && op == 1 {
		reply[8] ^= 1
	}
	if p.fault == "wrong-final-version" && address == 3 && op == 3 {
		reply[len(reply)-1] ^= 1
	}
	if p.fault == "wrong-new-dfu" && address == 3 && op == 0x13 {
		reply[7] ^= 1
	}
	return nil
}
func (w *engage75PreviewWorld) store() engage75PreviewStore {
	return engage75PreviewStore{pending: func(p engage75Pending) error {
		if err := p.validate(); err != nil {
			return err
		}
		if w.saveFailure == p.Phase {
			return errors.New("simulated pending persistence failure")
		}
		w.pending = p
		w.phases = append(w.phases, p.Phase)
		return nil
	}, recovery: func(state firmwareRecoveryState) error {
		if err := validEngage75RecoveryV2(state); err != nil {
			return err
		}
		if w.saveFailure == "recovery" {
			return errors.New("simulated recovery persistence failure")
		}
		data, _ := json.Marshal(state)
		var copyState firmwareRecoveryState
		if err := json.Unmarshal(data, &copyState); err != nil {
			return err
		}
		w.recovery = &copyState
		return nil
	}}
}
func (w *engage75PreviewWorld) run(ctx context.Context) error {
	return runEngage75Preview(ctx, w, w.runtimeDevice, w.archive, strings.Repeat("a", 64), w.store(), func(target byte, _, _ int) {
		if len(w.stages) == 0 || w.stages[len(w.stages)-1] != target {
			w.stages = append(w.stages, target)
		}
	})
}

func TestEngage75PreviewReachesMeasuredDisplayThenAllComponents(t *testing.T) {
	enableEngage75PreviewTest(t)
	for _, revision := range []uint16{0x28, 0x35} {
		t.Run(fmt.Sprintf("%02x", revision), func(t *testing.T) {
			w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), revision)
			if err := w.run(context.Background()); err != nil {
				t.Fatal(err, "phases", w.phases, "stages", w.stages)
			}
			if w.entryCalls != 1 || w.assessments != 2 || w.finalChecks != 1 || w.pending.Phase != "final-verified" || w.recovery.SitelDECT != nil || !slices.Equal(w.boots, []byte{10, 1}) || !slices.Equal(w.stages, engage75TargetOrder) {
				t.Fatal("incomplete or legacy-derived update", w.entryCalls, w.pending.Phase, w.boots, w.stages)
			}
			if w.recovery.SitelDECTV2.Selected.HeadRuntimeInfo.SectorSize != 65536 || w.recovery.SitelDECTV2.Selected.HeadRuntimeInfo.WriteSize != 1001 {
				t.Fatal("runtime evidence changed")
			}
			sawLarge, sawTail := false, false
			for _, packet := range w.packets {
				if packet[0] == 10 && packet[5] == 3 {
					n := len(packet) - 10
					sawLarge = sawLarge || n == 1001
					sawTail = sawTail || n > 0 && n < 1001
				}
			}
			if !sawLarge || !sawTail {
				t.Fatal("1001-byte and partial write blocks not exercised")
			}
		})
	}
}

func TestEngage75PreviewStopsBeforeAnyChildOrTargetOnMissingDisplay(t *testing.T) {
	enableEngage75PreviewTest(t)
	for _, fault := range []string{"display-missing", "display-timeout", "display-wrong", "display-malformed", "display-mode2", "head-mode3", "base-wrong", "base-missing", "base-malformed", "base-mode2", "head-wrong", "head-missing", "head-malformed", "head-mode2", "binding-lost", "port-swap", "serial-change", "head-swap"} {
		t.Run(fault, func(t *testing.T) {
			w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
			w.fault = fault
			if err := w.run(context.Background()); err == nil {
				t.Fatal("bad evidence installed")
			}
			for _, packet := range w.packets {
				if packet[4] != 15 || packet[5] != 0 {
					t.Fatalf("effect before image verification: %x", packet)
				}
			}
			if w.recovery != nil || w.base.erases+w.head.erases+w.mmi.erases != 0 || w.settingsWrites != 0 || len(w.boots) != 0 {
				t.Fatal("pre-identity failure created transfer authority or guessed cleanup")
			}
		})
	}
}

func TestEngage75PreviewEntryAndPersistenceFailures(t *testing.T) {
	enableEngage75PreviewTest(t)
	for _, fault := range []string{"pending-inspection", "entry-intent", "unsent", "rejected", "possibly-sent", "wait-timeout", "recovery"} {
		t.Run(fault, func(t *testing.T) {
			w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
			switch fault {
			case "unsent":
				w.entryErr = managementNotSent(context.Canceled)
			case "rejected":
				w.entryErr = errSitelRejected
			case "possibly-sent":
				w.entryErr = context.DeadlineExceeded
			case "wait-timeout":
				w.fault = fault
			default:
				w.saveFailure = fault
			}
			err := w.run(context.Background())
			if fault == "possibly-sent" {
				if err != nil || w.entryCalls != 1 || w.pending.Entry != "possibly-sent" {
					t.Fatal("ambiguous entry was replayed or not observed", err)
				}
				return
			}
			if err == nil || w.base.erases+w.head.erases+w.mmi.erases != 0 {
				t.Fatal("failed checkpoint/entry flashed", err)
			}
			if (fault == "pending-inspection" || fault == "entry-intent") && w.entryCalls != 0 {
				t.Fatal("entry before durable journal")
			}
			if fault == "unsent" && w.pending.Entry != "not-sent" || fault == "rejected" && w.pending.Entry != "rejected" {
				t.Fatal("entry delivery outcomes were merged", w.pending.Entry)
			}
			if w.entryCalls > 1 {
				t.Fatal("entry replayed")
			}
		})
	}
}

func TestEngage75PreviewFinalVerificationAndReturnFailures(t *testing.T) {
	enableEngage75PreviewTest(t)
	for _, fault := range []string{"wrong-final-version", "final-head-swap", "wrong-new-dfu", "new-runtime-pid", "head-return-no-reply", "return-no-reply"} {
		t.Run(fault, func(t *testing.T) {
			w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
			w.fault = fault
			err := w.run(context.Background())
			if !slices.Equal(w.stages, engage75TargetOrder) {
				t.Fatal("failure occurred before all nine stages", err, w.stages)
			}
			if fault == "return-no-reply" {
				if err != nil || w.finalChecks != 1 || !slices.Equal(w.ambiguousReturns, []byte{1}) || !slices.Equal(w.returnRequests, []byte{10, 1}) || !slices.Equal(w.boots, []byte{10, 1}) || w.pending.Entry != "replied" || w.pending.HeadReturn != "replied" || !w.pending.HeadMainVerified || w.pending.BaseReturn != "possibly-sent" || w.pending.Phase != "final-verified" {
					t.Fatal("ambiguous base return was replayed or accepted without final evidence", err, w.pending, w.returnRequests)
				}
				return
			}
			if err == nil || !w.pending.Unresolved || w.pending.Phase != "unresolved" {
				t.Fatal("ACK/base presence was accepted as complete", err)
			}
			if fault == "head-return-no-reply" {
				if !errors.Is(err, context.DeadlineExceeded) || !slices.Equal(w.ambiguousReturns, []byte{10}) || !slices.Equal(w.returnRequests, []byte{10}) || !slices.Equal(w.boots, []byte{10}) || w.pending.HeadReturn != "possibly-sent" || w.pending.HeadMainVerified || w.pending.BaseReturn != "not-sent" || w.finalChecks != 0 {
					t.Fatal("ambiguous head outcome was merged, replayed or advanced", err, w.pending, w.returnRequests)
				}
			}
		})
	}
}

func TestEngage75PreviewFreshInfoBeforeEffects(t *testing.T) {
	enableEngage75PreviewTest(t)
	for _, fault := range []string{"mode", "image", "geometry"} {
		t.Run(fault, func(t *testing.T) {
			w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
			sweeps := 0
			w.afterPacket = func(packet []byte) {
				if packet[0] == 3 && packet[5] == 0 {
					sweeps++
					if sweeps == 2 {
						switch fault {
						case "mode":
							w.head.applicationMode = true
						case "image":
							w.head.wrongImageID = true
						case "geometry":
							w.head.writeSize = 999
						}
					}
				}
			}
			err := w.run(context.Background())
			if fault == "mode" { // A genuinely current Main child can switch once, then
				// must be re-read in loader mode; this fixture models that transition.
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if fault == "geometry" { // A fresh loader geometry during preparation is valid.
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || w.head.erases+w.base.erases+w.mmi.erases != 0 {
				t.Fatal("stale image authorized transfer", err)
			}
		})
	}
}

func TestEngage75PreviewGuardDefaultAndFlags(t *testing.T) {
	old := Engage75Compatibility
	defer func() { Engage75Compatibility = old }()
	for _, value := range []string{"disabled", "", "ENABLED", "true", "enabled"} {
		Engage75Compatibility = value
		for _, ack := range []bool{false, true} {
			args := []string{"x.zip", engage75PreviewFlag}
			if ack {
				args = append(args, HardwareWriteFlag)
			}
			_, preview, err := extractEngage75Preview(args)
			if (err == nil) != (value == "enabled" && ack) || err == nil && !preview {
				t.Fatal("guard accepted invalid opt-in", value, ack, err)
			}
		}
	}
	Engage75Compatibility = "disabled"
	if _, _, err := extractEngage75Preview([]string{"x.zip", HardwareWriteFlag}); err != nil {
		t.Fatal("ordinary install changed", err)
	}
	if err := ValidateInstallInput([]string{"nonexistent.zip", engage75PreviewFlag, HardwareWriteFlag}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatal("disabled preview reached file/device work", err)
	}
}

func TestEngage75PreviewJournalAndV2CannotAuthorizeRestart(t *testing.T) {
	enableEngage75PreviewTest(t)
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := saveEngage75Pending(w.pending); err != nil {
		t.Fatal(err)
	}
	got, err := loadEngage75Pending()
	if err != nil || !reflect.DeepEqual(got, w.pending) {
		t.Fatal("journal reload", err)
	}
	if err := checkEngage75PreviewStart(); err == nil {
		t.Fatal("pending allowed entry replay")
	}
	// A crash after intent is never treated as definitely unsent on reload.
	got.Version, got.Attempt, got.LastPhase, got.BootSerial = 1, 0, "", ""
	got.Phase, got.Entry = "entry-intent", "intent"
	got.HeadReturn, got.BaseReturn = "not-sent", "not-sent"
	got.HeadMainVerified = false
	if err := saveEngage75Pending(got); err != nil {
		t.Fatal(err)
	}
	if err := checkEngage75PreviewStart(); err == nil {
		t.Fatal("entry intent replayed")
	}
	if err := saveFirmwareRecoveryState(*w.recovery); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFirmwareRecoveryState(); err != nil {
		t.Fatal("valid V2 reload failed", err)
	}
	if err := clearEngage75Pending(); err != nil {
		t.Fatal(err)
	}
	if err := checkEngage75PreviewStart(); err == nil {
		t.Fatal("V2 authority resumed without live headset continuity")
	}
	if err := validSitelDECTRecovery(*w.recovery); err == nil {
		t.Fatal("V2 accepted by legacy loader")
	}
	path, _ := firmwareRecoveryStatePath()
	original, _ := os.ReadFile(path)
	for _, bad := range []string{
		strings.Replace(string(original), `"version": 2`, `"version": 99,"Version": 2`, 1),
		strings.Replace(string(original), `"runtimePid": 4371`, `"runtimePid": 1,"RuntimePID": 4371`, 1),
		strings.Replace(string(original), `"sitelDectV2": {`, `"sitelDectV2": null,"SitelDectV2": {`, 1),
		strings.TrimSuffix(strings.TrimSpace(string(original)), "}") + `,"SitelDectV2":null}`,
		strings.Replace(string(original), `"version": 2`, `"version": 2,"version": 2`, 1), strings.Replace(string(original), `"version": 2`, `"version": 99`, 1), strings.Replace(string(original), engage75SelectionV2, "caller-weakened-mask", 1), strings.Replace(string(original), `"sitelDectV2": {`, `"sitelDectV2": {"unknown": 1,`, 1)} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadFirmwareRecoveryState(); err == nil {
			t.Fatal("unknown V2 schema accepted")
		}
	}
	pendingPath, _ := engage75PendingPath()
	encoded, _ := json.Marshal(got)
	for _, bad := range []string{
		strings.Replace(string(encoded), `"version":1`, `"version":99,"Version":1`, 1),
		strings.Replace(string(encoded), `"runtimePid":4371`, `"runtimePid":1,"RuntimePID":4371`, 1),
		strings.Replace(string(encoded), `"version":1`, `"version":1,"version":1`, 1), strings.Replace(string(encoded), `"version":1`, `"version":2`, 1), strings.Replace(string(encoded), `"purpose":`, `"extra":1,"purpose":`, 1)} {
		if err := os.WriteFile(pendingPath, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadEngage75Pending(); err == nil {
			t.Fatal("unknown pending schema accepted")
		}
	}
}

func TestLocalEngage75PreviewWholeArchive(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_ENGAGE75_ARCHIVE")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_ENGAGE75_ARCHIVE for original-package preview model")
	}
	enableEngage75PreviewTest(t)
	archive, err := loadSitelDECTArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, revision := range []uint16{0x28, 0x35} {
		t.Run(fmt.Sprintf("%02x", revision), func(t *testing.T) {
			w := newEngage75PreviewWorld(archive, revision)
			w.bootWithoutSerial = true
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			if err := w.run(ctx); err != nil {
				t.Fatal(err, "stages", w.stages)
			}
			if !slices.Equal(w.stages, engage75TargetOrder) || w.pending.Phase != "final-verified" {
				t.Fatal("original full package did not finish")
			}
			t.Logf("revision %02x: all nine original components; runtime head decoded65536/1001, fresh loader1024/1001; HEX writes %d/%d/%d; final identities and versions verified", revision, w.base.writes, w.head.writes, w.mmi.writes)
		})
	}
}

// A real uint32 imageID is compared in full. Expected metadata cannot turn a
// wrong vendor with the same product suffix into a measured match.
func TestEngage75PreviewImageEvidenceCannotBeSynthesized(t *testing.T) {
	images := []engage75MeasuredImage{{1, 0x0b0e1111, 0}, {10, 0x0b0e1116, 1}, {3, 0x0b0e1117, 0}}
	if !validEngage75Images(images) {
		t.Fatal("valid measured IDs rejected")
	}
	copyImages := slices.Clone(images)
	copyImages[2].ImageID = uint32(binary.LittleEndian.Uint16([]byte{0x17, 0x11}))
	if validEngage75Images(copyImages) || validEngage75Images(append(images, images[0])) {
		t.Fatal("missing vendor or extra image accepted")
	}
}

func TestEngage75PreviewCancellationAfterEntryDoesNotReplayOrFlash(t *testing.T) {
	enableEngage75PreviewTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
	w.afterEntry = cancel
	if err := w.run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation", err)
	}
	if w.entryCalls != 1 || len(w.packets) != 0 || !w.pending.Unresolved || w.recovery != nil {
		t.Fatal("cancelled entry continued or replayed", w.entryCalls, w.pending)
	}
}

func TestEngage75PreviewChecksGeometryAgainBeforeFirstErase(t *testing.T) {
	enableEngage75PreviewTest(t)
	for _, fault := range []string{"geometry", "mode", "image"} {
		t.Run(fault, func(t *testing.T) {
			w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
			changed := false
			w.afterPacket = func(packet []byte) {
				// CRC is read-only and follows all area preparation. Change the current
				// loader just after that response, before the first requested erase.
				if !changed && packet[0] == 10 && packet[5] == 4 {
					changed = true
					switch fault {
					case "geometry":
						w.head.writeSize = 999
					case "mode":
						w.head.applicationMode = true
					case "image":
						w.head.wrongImageID = true
					}
				}
			}
			err := w.run(context.Background())
			if err == nil || !changed || w.head.erases+w.base.erases+w.mmi.erases != 0 || len(w.boots) != 0 {
				t.Fatal("stale prepared geometry/mode/image authorized effects", err, changed)
			}
		})
	}
}

func TestEngage75PreviewPendingSchemaNeverLoadsAsInstallAuthority(t *testing.T) {
	enableEngage75PreviewTest(t)
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
	w.fault = "display-missing"
	if err := w.run(context.Background()); err == nil {
		t.Fatal("fixture did not stop")
	}
	if err := saveEngage75Pending(w.pending); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareFirmwareTransfer("nonexistent.zip", w.archive.Manifest); err == nil || !strings.Contains(err.Error(), "unfinished") {
		t.Fatal("ordinary install ignored pending inspection", err)
	}
	encoded, _ := json.Marshal(w.pending)
	path, _ := firmwareRecoveryStatePath()
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFirmwareRecoveryState(); err == nil {
		t.Fatal("pending inspection became installation authority")
	}
	for _, phase := range []string{"entry-intent", "bound-boot-attachment", "images-verified", "recovery-bound", "transfer", "returning", "unresolved"} {
		p := w.pending
		p.Version, p.Attempt, p.LastPhase, p.BootSerial = 1, 0, "", ""
		p.Phase = phase
		p.Entry = "possibly-sent"
		p.Unresolved = phase == "unresolved"
		if err := saveEngage75Pending(p); err != nil {
			t.Fatal(phase, err)
		}
		if err := checkEngage75PreviewStart(); err == nil {
			t.Fatal("restart automatically resumed", phase)
		}
	}
}

func syntheticEngage75PreviewArchive() *sitelDECTArchive {
	archive := syntheticEngage75Archive()
	archive.Manifest.ProductName = "Jabra_Engage_75"
	return archive
}

func TestEngage75PreviewActualNormalizedLoader65536Write1001(t *testing.T) {
	enableEngage75PreviewTest(t)
	w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
	w.head.sectorSize, w.head.infoVersionZero = 65536, true
	area := w.head.areas[0]
	area.Size = 65536
	w.head.areas[0] = area
	w.head.checkpoint = func() bool { return true }
	connection, err := w.boot(context.Background(), w.runtimeDevice)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	rawPeer := connection.peer(10)
	raw, err := rawPeer.request(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := decodeSitelInfo(raw)
	if err != nil || info.SectorSize != 65536 || info.WriteSize != 1001 || info.Version != 0 {
		t.Fatal("actual decoded loader geometry lost", info, err)
	}
	peer := engage75PreviewPeer{next: rawPeer, address: 10, loaderInfo: &info}
	image := w.archive.Images[0]
	if image.File.SitelHidTargetID != "1" {
		t.Fatal("fixture order changed")
	}
	prepared, err := prepareSitelTargets(context.Background(), peer, []sitelPlannedImage{image}, info, 3, "10")
	if err != nil {
		t.Fatal(err)
	}
	if err := transferSitelImage(context.Background(), peer, prepared[0], info, nil); err != nil {
		t.Fatal(err)
	}
	if w.head.writes != 3 || w.head.erases != 1 {
		t.Fatal("65536 sector or 1001/1001/46 writes changed", w.head.erases, w.head.writes)
	}
	if err := verifySitelImage(context.Background(), peer, prepared[0], info.SectorSize); err != nil {
		t.Fatal(err)
	}
}

func TestEngage75PreviewVerifiesHeadReturnBeforeBaseAndFinalMain(t *testing.T) {
	enableEngage75PreviewTest(t)
	for _, fault := range []string{"head-ack-no-transition", "head-return-wrong-image", "head-return-info-timeout", "head-return-binding-lost", "wrong-final-head-image", "wrong-final-head-mode", "final-image-timeout", "final-binding-lost", "head-return-rejected", "base-return-rejected"} {
		t.Run(fault, func(t *testing.T) {
			w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
			w.fault = fault
			err := w.run(context.Background())
			if err == nil || !w.pending.Unresolved || !slices.Equal(w.stages, engage75TargetOrder) {
				t.Fatal("did not reach and reject invalid return/final observation", err, w.stages)
			}
			if fault == "head-return-rejected" {
				if !errors.Is(err, errSitelRejected) || !slices.Equal(w.nakReplies, []byte{10}) || !slices.Equal(w.returnRequests, []byte{10}) || w.pending.HeadReturn != "rejected" || w.pending.HeadMainVerified || w.pending.BaseReturn != "not-sent" || len(w.boots) != 0 || w.finalChecks != 0 {
					t.Fatal("head NAK was not witnessed/classified or was replayed", err, w.pending, w.nakReplies, w.returnRequests)
				}
				return
			}
			if fault == "base-return-rejected" {
				if !errors.Is(err, errSitelRejected) || !slices.Equal(w.nakReplies, []byte{1}) || !slices.Equal(w.returnRequests, []byte{10, 1}) || w.pending.HeadReturn != "replied" || !w.pending.HeadMainVerified || w.pending.BaseReturn != "rejected" || !slices.Equal(w.boots, []byte{10}) || w.finalChecks != 0 {
					t.Fatal("base NAK was not witnessed/classified or final validation continued", err, w.pending, w.nakReplies, w.returnRequests)
				}
				return
			}
			if strings.HasPrefix(fault, "head-") {
				if !slices.Equal(w.returnRequests, []byte{10}) || !slices.Equal(w.boots, []byte{10}) || w.pending.BaseReturn != "not-sent" || w.finalChecks != 0 {
					t.Fatal("base returned before fresh headset Main verification", w.returnRequests, w.pending)
				}
			} else if !slices.Equal(w.returnRequests, []byte{10, 1}) || !slices.Equal(w.boots, []byte{10, 1}) || w.finalChecks != 1 || !w.pending.HeadMainVerified {
				t.Fatal("final-read rejection did not reach final stage", w.returnRequests, w.finalChecks)
			}
		})
	}
}

func TestEngage75PreviewDefinitelyUnsentReturnAndSPILoss(t *testing.T) {
	enableEngage75PreviewTest(t)
	for _, fault := range []string{"head-unsent", "base-unsent", "spi-loss"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
			cancelled, lossInjected := false, false
			lossBoundary := 0
			var firstSuccessfulSPI []byte
			store := w.store()
			persist := store.pending
			store.pending = func(p engage75Pending) error {
				if err := persist(p); err != nil {
					return err
				}
				if !cancelled && p.Phase == "returning" && (fault == "head-unsent" && p.HeadReturn == "not-sent" || fault == "base-unsent" && p.HeadMainVerified && p.BaseReturn == "not-sent") {
					cancelled = true
					cancel()
				}
				return nil
			}
			if fault == "spi-loss" {
				w.afterPacket = func(packet []byte) {
					if !lossInjected && packet[4] == 15 && packet[5] >= 0x0b && packet[5] <= 0x15 {
						// The model only invokes this SPI hook after a successful simulated
						// response. Prove that it was a read and no flash/RAM write preceded it.
						if w.successfulSPIPackets != 1 || packet[3]&0xc0 != 0x40 || packet[5] != 0x0b && packet[5] != 0x0d && packet[5] != 0x12 && packet[5] != 0x14 {
							t.Fatalf("loss hook did not follow the first successful SPI read: %x", packet)
						}
						if w.base.erases+w.head.erases+w.mmi.erases+w.base.writes+w.head.writes+w.mmi.writes != 0 || w.settingsWrites != 0 || w.radio.cpu.programs != 0 {
							t.Fatal("write occurred before loss injection")
						}
						firstSuccessfulSPI = slices.Clone(packet)
						lossBoundary = len(w.packets)
						lossInjected = true
						w.fault = "binding-lost"
					}
				}
			}
			err := runEngage75Preview(ctx, w, w.runtimeDevice, w.archive, strings.Repeat("a", 64), store, func(target byte, _, _ int) {
				if len(w.stages) == 0 || w.stages[len(w.stages)-1] != target {
					w.stages = append(w.stages, target)
				}
			})
			if err == nil || !w.pending.Unresolved || w.finalChecks != 0 {
				t.Fatal("unsafe interrupted return/SPI continued", err)
			}
			if fault != "spi-loss" {
				if !cancelled || !errors.Is(err, context.Canceled) || !slices.Equal(w.stages, engage75TargetOrder) {
					t.Fatal("unsent-return fault was not reached after all nine stages", cancelled, err, w.stages)
				}
				if fault == "head-unsent" {
					if len(w.returnRequests) != 0 || len(w.boots) != 0 || w.pending.HeadReturn != "not-sent" || w.pending.BaseReturn != "not-sent" || w.pending.HeadMainVerified {
						t.Fatal("head request was sent after cancellation", w.returnRequests, w.pending)
					}
				} else {
					if !slices.Equal(w.returnRequests, []byte{10}) || !slices.Equal(w.boots, []byte{10}) || w.pending.HeadReturn != "replied" || !w.pending.HeadMainVerified || w.pending.BaseReturn != "not-sent" {
						t.Fatal("base request was sent after cancellation", w.returnRequests, w.pending)
					}
				}
				return
			}
			if !lossInjected || !errors.Is(err, ErrDECTInspectionChanged) || cancelled || w.successfulSPIPackets != 1 || lossBoundary <= 0 || !slices.Equal(w.packets[lossBoundary-1], firstSuccessfulSPI) {
				t.Fatal("SPI loss was not reached at the recorded successful-read boundary", lossInjected, err, lossBoundary, w.successfulSPIPackets)
			}
			for _, packet := range w.packets[lossBoundary:] {
				opcode := packet[5]
				if packet[3]&0xc0 == 0x80 || opcode == 2 || opcode == 3 || opcode == 5 || opcode == 0x0c || opcode == 0x0e || opcode == 0x13 || opcode == 0x15 {
					t.Fatalf("write/erase/mode operation after binding loss: %x", packet)
				}
			}
			if w.base.erases+w.head.erases+w.mmi.erases+w.base.writes+w.head.writes+w.mmi.writes != 0 || w.settingsWrites != 0 || w.radio.cpu.programs != 0 || len(w.returnRequests) != 0 {
				t.Fatal("lost base continuity allowed a write/return")
			}
		})
	}
}

func TestEngage75PreviewNullAliasCannotHideV2InsideValidLegacy(t *testing.T) {
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	w := newEngage75World(syntheticEngage75Archive(), 0x35)
	device := w.base.device()
	identity, err := w.runtime(context.Background(), device, w.archive)
	if err != nil {
		t.Fatal(err)
	}
	state := dectTestState(w.sitelDECTWorld)
	if err := bindSitelDECTRecovery(&state, identity, device, w.archive); err != nil {
		t.Fatal(err)
	}
	if err := saveFirmwareRecoveryState(state); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFirmwareRecoveryState(); err != nil {
		t.Fatal("unchanged legacy record rejected", err)
	}
	path, _ := firmwareRecoveryStatePath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	alias := strings.TrimSuffix(strings.TrimSpace(string(data)), "}") + `,"sitelDectV2":{"version":99},"SitelDectV2":null}`
	if err := os.WriteFile(path, []byte(alias), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFirmwareRecoveryState(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatal("null alias hid the V2 marker and fell back to valid legacy", err)
	}
}

func TestEngage75PreviewAbsentBootSerialPassesNativeWaitAndFullTransfer(t *testing.T) {
	enableEngage75PreviewTest(t)
	for _, revision := range []uint16{0x28, 0x35} {
		t.Run(fmt.Sprintf("%02x", revision), func(t *testing.T) {
			w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), revision)
			w.bootWithoutSerial = true
			if err := w.run(context.Background()); err != nil {
				t.Fatal("native reconnect accepts absent boot serial, preview must continue", err)
			}
			if w.pending.Phase != "final-verified" || !slices.Equal(w.stages, engage75TargetOrder) || w.finalChecks != 1 {
				t.Fatal("did not verify all components", w.stages, w.pending.Phase)
			}
		})
	}
}
