package firmware

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type sitelOTAWorld struct {
	w                            *sitelTestDevice
	archive                      *sitelOTAArchive
	state                        *firmwareRecoveryState
	serial, tunes                string
	status                       byte
	entered, exited, inspections int
	swapAfter                    int
	exitFails, enterDenied       bool
	exitACKLost                  bool
}

func makeSitelOTAWorld() *sitelOTAWorld {
	w := makeEngageWorld(false)
	w.pid, w.bootPID, w.wanted, w.ota = 0x1131, sitelOTAImagePID, "5.19.3", true
	w.images = []sitelPlannedImage{w.images[0], w.images[2]}
	w.images[0].File = GnVFile{Content: "firmware", Target: "headset", SitelHidTargetID: "1", GNPAddress: "4", Version: "5.19.3", UpdateOrder: 1}
	w.images[1].File = GnVFile{Content: "tunepack", Target: "headset", SitelHidTargetID: "28", GNPAddress: "4", Version: "5.17.1", UpdateOrder: 2, RegionID: "1"}
	binary.LittleEndian.PutUint32(w.images[0].Segments[0].Data[0x80:], 0x0b0e1116)
	copy(w.images[0].Segments[0].Data[0xc0:], []byte("5.19.3\x00"))
	binary.LittleEndian.PutUint32(w.images[1].Segments[0].Data[30:], 0x0b0e1116)
	copy(w.images[1].Segments[0].Data[11:21], []byte("5.17.1\x00\x00\x00\x00"))
	archive := &sitelOTAArchive{Manifest: &BuildVector{Version: w.wanted, ProductName: "Engage fixture", TargetUSBPIDs: []string{"0x1116"}}, Images: w.images, Region: 1}
	return &sitelOTAWorld{w: w, archive: archive, serial: "wireless-fixture", tunes: "4.0.0"}
}

func (s *sitelOTAWorld) target() sitelOTATarget {
	return sitelOTATarget{Parent: s.w.device(), ParentAddress: 1, ParentIdentity: strings.Repeat("b", 64), Child: sitelIdentity{Address: 4, PID: 0x1116, BootPID: sitelOTAImagePID, Serial: s.serial, Variant: "0172", Version: s.w.version}, Region: 1, TunesVersion: s.tunes}
}
func (s *sitelOTAWorld) inspect(ctx context.Context, _ USBDevice) (sitelOTATarget, error) {
	if err := ctx.Err(); err != nil {
		return sitelOTATarget{}, err
	}
	s.inspections++
	if s.swapAfter > 0 && s.inspections >= s.swapAfter {
		s.serial = "replacement-headset"
	}
	return s.target(), nil
}
func (s *sitelOTAWorld) conditions(context.Context, sitelOTATarget) (byte, error) {
	return s.status, nil
}
func (s *sitelOTAWorld) enter(_ context.Context, _ sitelOTATarget) error {
	if s.state == nil || s.state.Phase != "entering-wireless" {
		return errors.New("permission request before checkpoint")
	}
	if s.enterDenied {
		return errors.New("permission denied")
	}
	s.entered++
	s.status = 4
	return nil
}
func (s *sitelOTAWorld) channel(ctx context.Context, _ sitelOTATarget) (sitelRequest, func() error, error) {
	if s.status != 4 {
		return nil, nil, errors.New("not in wireless update mode")
	}
	peer := &sitelBootPeer{world: s.w}
	layout := sitelHIDLayout{ReportID: 10, ReportBytes: 64, MaxMessage: 1024}
	link := &sitelLink{io: peer, in: layout, out: layout, timeout: time.Second}
	if err := link.start(ctx); err != nil {
		return nil, nil, err
	}
	return &sitelRequester{link: link, address: 4, timeout: time.Second}, func() error { return nil }, nil
}
func (s *sitelOTAWorld) exit(context.Context, sitelOTATarget) error {
	if s.state == nil || s.state.Phase != "leaving-wireless" {
		return errors.New("exit before durable completion")
	}
	if s.exitFails {
		return errors.New("exit acknowledgement lost")
	}
	for _, image := range s.archive.Images {
		for _, segment := range image.Segments {
			for i, b := range segment.Data {
				if s.w.memory[segment.Address+uint32(i)] != b {
					return errors.New("exit before every firmware byte verified")
				}
			}
		}
	}
	s.exited++
	s.status = 0
	s.w.version = s.archive.Manifest.Version
	s.tunes = s.archive.Images[1].File.Version
	if s.exitACKLost {
		return errors.New("exit reply lost after activation")
	}
	return nil
}
func (s *sitelOTAWorld) wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }
func (s *sitelOTAWorld) run(ctx context.Context, state *firmwareRecoveryState) error {
	s.w.checkpoint = func() bool { return s.state != nil && s.state.Phase == "flashing-wireless" }
	return runSitelOTAInstall(ctx, s, s.target(), s.archive, state, func() error { copyState := *state; s.state = &copyState; return nil }, nil)
}
func otaTestState() firmwareRecoveryState {
	return firmwareRecoveryState{FormatVersion: 1, ArchiveSHA256: strings.Repeat("a", 64), ProductName: "Engage fixture", FirmwareVersion: "5.19.3", TargetUSBPIDs: []string{"0x1116"}, Attempt: 1}
}

func TestWirelessEngageCompleteAndResume(t *testing.T) {
	s := makeSitelOTAWorld()
	state := otaTestState()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.w.failWrite = 3
	if err := s.run(ctx, &state); err == nil || s.exited != 0 || s.status != 4 {
		t.Fatal("interrupted update was activated", err)
	}
	s.w.failWrite = 0
	if err := s.run(ctx, &state); err != nil {
		t.Fatal(err)
	}
	if s.entered != 1 || s.exited != 1 || state.Phase != "verifying-wireless" || s.w.version != "5.19.3" || s.tunes != "5.17.1" {
		t.Fatal("wrong wireless update lifecycle")
	}
	writes := s.w.writes
	if err := s.run(ctx, &state); err != nil || s.w.writes != writes || s.entered != 1 {
		t.Fatal("verification retry rewrote firmware", err)
	}
}

func TestWirelessEngageRefusesAnotherSessionAndChangedTargets(t *testing.T) {
	for _, status := range []byte{1, 2, 3, 4, 5, 8, 255} {
		s := makeSitelOTAWorld()
		s.status = status
		state := otaTestState()
		if err := s.run(context.Background(), &state); err == nil || s.entered != 0 || s.w.writes != 0 {
			t.Fatal("unsafe update conditions accepted", status, err)
		}
	}
	for _, change := range []func(*sitelOTAWorld){
		func(s *sitelOTAWorld) { s.swapAfter = 1 },
		func(s *sitelOTAWorld) { s.archive.Region = 2 },
		func(s *sitelOTAWorld) { s.w.wrongImageID = true },
		func(s *sitelOTAWorld) { s.w.badGeometry = true },
		func(s *sitelOTAWorld) { s.enterDenied = true },
	} {
		s := makeSitelOTAWorld()
		change(s)
		state := otaTestState()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := s.run(ctx, &state)
		cancel()
		if err == nil || s.w.writes != 0 || s.exited != 0 {
			t.Fatal("invalid wireless target changed firmware", err)
		}
	}
}

func TestWirelessEngageCheckpointAndCompletionFailures(t *testing.T) {
	s := makeSitelOTAWorld()
	state := otaTestState()
	if err := runSitelOTAInstall(context.Background(), s, s.target(), s.archive, &state, func() error { return errors.New("disk full") }, nil); err == nil || s.entered != 0 {
		t.Fatal("entered update mode without recovery record", err)
	}
	s = makeSitelOTAWorld()
	state = otaTestState()
	s.exitFails = true
	if err := s.run(context.Background(), &state); err == nil || state.Phase != "leaving-wireless" {
		t.Fatal("lost exit acknowledgement was ignored", err)
	}
	writes := s.w.writes
	s.exitFails = false
	if err := s.run(context.Background(), &state); err != nil || s.w.writes != writes {
		t.Fatal("exit retry repeated firmware writes", err)
	}
	s = makeSitelOTAWorld()
	state = otaTestState()
	s.w.badVerify = true
	if err := s.run(context.Background(), &state); err == nil || s.exited != 0 {
		t.Fatal("bad CRC reached activation", err)
	}
	s = makeSitelOTAWorld()
	state = otaTestState()
	s.exitACKLost = true
	if err := s.run(context.Background(), &state); err == nil || state.Phase != "leaving-wireless" || s.status != 0 {
		t.Fatal("lost final reply did not retain recovery", err)
	}
	writes = s.w.writes
	s.exitACKLost = false
	if err := s.run(context.Background(), &state); err != nil || s.w.writes != writes || s.exited != 1 {
		t.Fatal("completed update was repeated after its reply was lost", err)
	}
}

func TestWirelessEngageRecoveryCannotMoveToAnotherHeadset(t *testing.T) {
	s := makeSitelOTAWorld()
	state := otaTestState()
	s.w.failWrite = 3
	if err := s.run(context.Background(), &state); err == nil {
		t.Fatal("expected interrupted transfer")
	}
	writes := s.w.writes
	s.w.failWrite = 0
	s.serial = "another-headset"
	if err := s.run(context.Background(), &state); err == nil || s.w.writes != writes || s.exited != 0 {
		t.Fatal("recovery moved to a different wireless headset", err)
	}
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	if err := saveFirmwareRecoveryState(state); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFirmwareRecoveryState(); err != nil {
		t.Fatal(err)
	}
	state.SitelOTA.ParentPID = 0x24c7
	if err := saveFirmwareRecoveryState(state); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFirmwareRecoveryState(); err == nil {
		t.Fatal("wireless recovery accepted an unrelated adapter")
	}
}

func TestLocalWirelessEngageOriginalArchive(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_SITEL_OTA_AUDIT")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_SITEL_OTA_AUDIT for original image checks")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Path, MD5     string
		HeaderOffsets []uint32 `json:"header_offsets"`
	}
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != 1 {
		t.Fatal("missing original wireless archive", err)
	}
	archive, err := loadSitelOTAArchive(rows[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	checksum, err := firmwareFileMD5(rows[0].Path)
	if err != nil || checksum != rows[0].MD5 {
		t.Fatal("original archive changed", err)
	}
	if err := ValidateInstallInput([]string{rows[0].Path}); err != nil {
		t.Fatal(err)
	}
	if len(rows[0].HeaderOffsets) != 1 {
		t.Fatal("ambiguous image metadata")
	}
	s := makeSitelOTAWorld()
	s.archive = archive
	s.w.images = archive.Images
	s.w.imageInfoOffset = rows[0].HeaderOffsets[0]
	s.w.memory = map[uint32]byte{}
	s.w.areas = map[byte]sitelArea{}
	for i, image := range archive.Images {
		kind := []byte{0, 3}[i]
		base := image.Segments[0].Address
		last := image.Segments[len(image.Segments)-1]
		size := uint32((uint64(last.Address) + uint64(len(last.Data)) - uint64(base) + 255) / 256 * 256)
		s.w.areas[kind] = sitelArea{Address: base, Size: size}
		for offset := uint32(0); offset < size; offset++ {
			s.w.memory[base+offset] = 255
		}
	}
	for _, pid := range []uint16{0x1116, 0x111c, 0x1145, 0x1149} {
		if os.Getenv("JABRIDGE_TEST_LIVE_RELEASES") == "1" {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := verifySitelOTARelease(ctx, rows[0].Path, archive, pid)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	state := otaTestState()
	state.ProductName = archive.Manifest.ProductName
	state.FirmwareVersion = archive.Manifest.Version
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.run(ctx, &state); err != nil {
		t.Fatal(err)
	}
	if s.w.writes == 0 || s.entered != 1 || s.exited != 1 {
		t.Fatal("original images did not reach the wireless transfer")
	}
	t.Logf("original wireless archive: %d data writes, separate firmware %s / sound prompts %s", s.w.writes, s.w.version, s.tunes)
}
