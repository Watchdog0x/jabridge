package firmware

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEngageControllerWithoutOwnSerial(t *testing.T) {
	w := makeEngageWorld(true)
	w.controllerHasNoSerial = true
	w.controllerReenumerates = true
	state := firmwareRecoveryState{ArchiveSHA256: "fixture"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runSitelInstall(ctx, w, w.device(), w.images, "4.1.3", &state, func() error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if w.activations != 1 || w.controllerVersion != "4.1.3" {
		t.Fatal("controller without serial was not verified")
	}
}

func TestSitelCRCOriginalUpdaterVectors(t *testing.T) {
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}
	for _, test := range []struct {
		data              []byte
		legacy, corrected uint16
	}{{nil, 65535, 65535}, {[]byte("123456789"), 28340, 10673}, {data, 60832, 16317}} {
		if sitelCRC(test.data, true) != test.legacy || sitelCRC(test.data, false) != test.corrected {
			t.Fatal("original updater CRC mismatch")
		}
	}
}

func TestSitelLinkRetriesWithoutRepeatingRequest(t *testing.T) {
	w := makeEngageWorld(false)
	peer := &sitelBootPeer{world: w, dropAck: 2}
	layout := sitelHIDLayout{ReportID: 10, ReportBytes: 64, MaxMessage: 1024}
	link := &sitelLink{io: peer, in: layout, out: layout, timeout: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := link.start(ctx); err != nil {
		t.Fatal(err)
	}
	r := &sitelRequester{link: link, address: 1, timeout: 100 * time.Millisecond}
	body, err := r.request(ctx, 0, nil)
	if err != nil || len(body) != 19 || peer.message != 1 {
		t.Fatal("reply-before-ACK or duplicate request failed", err, peer.message)
	}
	peer.dropAck = 100
	if _, err := r.request(ctx, 0, nil); err == nil || link.ready {
		t.Fatal("retry exhaustion did not stop link")
	}
}

func TestSitelZeroIDAndWriteBufferNotices(t *testing.T) {
	w := makeEngageWorld(false)
	w.zeroSequence = true
	w.bufferNotice = true
	state := firmwareRecoveryState{ArchiveSHA256: "fixture"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runSitelInstall(ctx, w, w.device(), w.images, "4.1.3", &state, func() error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if w.writes != 96 {
		t.Fatal("write-buffer availability caused an extra write", w.writes)
	}
}

func TestEngageControllerEventTypeIsNotSubscriptionMask(t *testing.T) {
	w := makeEngageWorld(true)
	w.badControllerType = true
	r := &sitelRuntime{io: &sitelRuntimePeer{world: w}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	id, err := r.identify(ctx, w.device())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.activateController(ctx, id, "4.1.3"); err == nil || w.activations != 0 {
		t.Fatal("subscription mask mistaken for controller type")
	}
}

func TestEngageControllerDowngradeRefusedBeforeBootloader(t *testing.T) {
	w := makeEngageWorld(true)
	w.controllerVersion = "5.0.0"
	state := firmwareRecoveryState{ArchiveSHA256: "fixture"}
	if err := runSitelInstall(context.Background(), w, w.device(), w.images, "4.1.3", &state, func() error { return nil }, nil); err == nil || w.bootMode || w.writes != 0 {
		t.Fatal("controller downgrade path guessed")
	}
}

func TestEngageControllerUSBRestartDoesNotReplayActivation(t *testing.T) {
	w := makeEngageWorld(true)
	w.controllerReenumerates = true
	state := firmwareRecoveryState{ArchiveSHA256: "fixture"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runSitelInstall(ctx, w, w.device(), w.images, "4.1.3", &state, func() error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if w.activations != 1 || w.generation != 3 || w.controllerVersion != "4.1.3" {
		t.Fatal("controller restart was not verified", w.activations, w.generation)
	}
}

func TestEngageRecoverySurvivesSavedRecordReload(t *testing.T) {
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	w := makeEngageWorld(true)
	w.failWrite = 3
	state := firmwareRecoveryState{FormatVersion: 1, ArchiveSHA256: strings.Repeat("a", 64), ProductName: "Engage fixture", FirmwareVersion: "4.1.3", TargetUSBPIDs: []string{"0x4050"}, Attempt: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runSitelInstall(ctx, w, w.device(), w.images, "4.1.3", &state, func() error { return saveFirmwareRecoveryState(state) }, nil); err == nil {
		t.Fatal("interruption expected")
	}
	reloaded, err := loadFirmwareRecoveryState()
	if err != nil {
		t.Fatal(err)
	}
	before := w.writes
	wrong := w.device()
	wrong.Serial = "replacement"
	if err := runSitelInstall(ctx, w, wrong, w.images, "4.1.3", &reloaded, func() error { return nil }, nil); err == nil || w.writes != before {
		t.Fatal("replaced bootloader accepted")
	}
	w.failWrite = 0
	if err := runSitelInstall(ctx, w, w.device(), w.images, "4.1.3", &reloaded, func() error { return saveFirmwareRecoveryState(reloaded) }, nil); err != nil {
		t.Fatal(err)
	}
	if err := clearFirmwareRecoveryState(); err != nil {
		t.Fatal(err)
	}
}

func TestEngageBootloaderReportSelection(t *testing.T) {
	report := func(id byte, kind string, size int, page uint32) HIDReport {
		return HIDReport{ID: id, Kind: kind, Bytes: size, Fields: []HIDField{{SizeBits: 8, Count: uint32(size - 1), UsagePage: page}}}
	}
	in, out, err := selectSitelLayouts([]HIDReport{report(10, "input", 64, 0xff54), report(11, "output", 33, 0xff55)})
	if err != nil || in.ReportID != 10 || out.ReportID != 11 || out.ReportBytes != 33 {
		t.Fatal(in, out, err)
	}
	if _, _, err := selectSitelLayouts([]HIDReport{report(5, "input", 64, 0xff00), report(5, "output", 64, 0xff00)}); err == nil {
		t.Fatal("runtime interface accepted as bootloader")
	}
	a := report(0, "input", 65, 0xff54)
	a.Bytes = 64
	b := a
	b.Kind = "output"
	in, out, err = selectSitelLayouts([]HIDReport{a, b})
	if err != nil || in.ReportBytes != 65 || out.ReportID != 0 {
		t.Fatal("unnumbered descriptor", in, out, err)
	}
}

func TestSitelCollectionCannotBeMistakenForRuntimeGNP(t *testing.T) {
	descriptor := []byte{0x06, 0x54, 0xff, 0x09, 1, 0xa1, 1, 0x85, 10, 0x06, 0, 0xff, 0x09, 1, 0x75, 8, 0x95, 63, 0x81, 2, 0x09, 1, 0x91, 2, 0xc0}
	reports, err := parseHIDReports(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := selectSitelLayouts(reports); err != nil {
		t.Fatal("Sitel collection identity lost", err)
	}
	if _, err := SelectControlLayout(reports); err == nil {
		t.Fatal("ordinary GNP selected a bootloader collection")
	}
}

func FuzzSitelReplyMetadata(f *testing.F) {
	f.Add([]byte{0})
	f.Add(make([]byte, 19))
	f.Add(make([]byte, 17))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = decodeSitelInfo(data)
		_, _ = decodeSitelArea(data)
		if len(data) <= 65 {
			a := sitelHIDAssembler{layout: sitelHIDLayout{ReportID: 10, ReportBytes: 64, MaxMessage: 1024}}
			_, _ = a.Push(data)
		}
	})
}

func TestLocalEngageOriginalImagesThroughNativeUpdater(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_ENGAGE_ARCHIVE")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_ENGAGE_ARCHIVE for an isolated real-image transfer")
	}
	manifest, images, err := loadSitelImages(path)
	if err != nil {
		t.Fatal(err)
	}
	// Geometry here is an explicit test fixture. Production obtains these values
	// from the actual bootloader. Metadata locations were independently checked
	// by executing the original updater against the same three original images.
	for _, controller := range []bool{false, true} {
		t.Run(fmt.Sprint(controller), func(t *testing.T) {
			w := makeEngageWorld(controller)
			w.images = images
			w.memory = map[uint32]byte{}
			for _, image := range images {
				var kind byte
				switch image.File.SitelHidTargetID {
				case "03":
					kind = 0
				case "29":
					kind = 4
				case "27":
					kind = 3
				default:
					t.Fatal("wrong image target")
				}
				segments := image.Segments
				base := segments[0].Address
				last := segments[len(segments)-1]
				end := uint64(last.Address) + uint64(len(last.Data))
				size := uint32((end - uint64(base) + 255) / 256 * 256)
				w.areas[kind] = sitelArea{Address: base, Size: size}
				for i := uint32(0); i < size; i++ {
					w.memory[base+i] = 255
				}
			}
			state := firmwareRecoveryState{ArchiveSHA256: "fixture"}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := runSitelInstall(ctx, w, w.device(), images, manifest.Version, &state, func() error { return nil }, nil); err != nil {
				t.Fatal(err)
			}
			t.Logf("original images verified and transferred: writes=%d erases=%d controller activations=%d; no physical hardware", w.writes, w.erases, w.activations)
		})
	}
}
