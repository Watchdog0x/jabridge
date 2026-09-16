package firmware

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

type sitelDECTWorld struct {
	base, head, usb *sitelTestDevice
	archive         *sitelDECTArchive
	saved           *firmwareRecoveryState
	headSerial      string
	boots           []byte
	entries         int
}

func makeSitelDECTWorld(profile sitelDECTProfile) *sitelDECTWorld {
	base, head := makeEngageWorld(false), makeEngageWorld(false)
	base.pid, base.bootPID, base.wanted = profile.RuntimePIDs[0], profile.BootPID, "5.19.3"
	head.pid, head.bootPID, head.wanted = profile.HeadsetImagePID, profile.HeadsetImagePID, base.wanted
	if profile.Legacy {
		base.wanted, head.wanted = "4.7.0", "4.7.0"
		base.imageInfoOffset, head.imageInfoOffset = 0x10, 0x10
	}
	baseOriginal, headOriginal := base.images, head.images
	var usb *sitelTestDevice
	var usbOriginal []sitelPlannedImage
	if profile.USBImagePID != 0 {
		usb = makeEngageWorld(false)
		usb.pid, usb.bootPID, usb.wanted = profile.USBImagePID, profile.USBImagePID, base.wanted
		usb.imageInfoOffset = 0x10
		usbOriginal = usb.images
		usb.images = nil
		base.version, head.version, usb.version = "1.0.0", "1.0.0", "1.0.0"
	}
	base.images = nil
	head.images = nil
	archive := &sitelDECTArchive{Manifest: &BuildVector{Version: base.wanted, ProductName: profile.Name, TargetUSBPIDs: []string{fmt.Sprintf("0x%04x", profile.BootPID)}}, Profile: profile}
	for i, target := range profile.Targets {
		w, source, address, role := base, baseOriginal, "1", "base"
		if target == 1 || target == 28 {
			w, source, address, role = head, headOriginal, "10", "headset"
		}
		if target == 7 {
			w, source, address, role = usb, usbOriginal, "2", "base"
		}
		image := source[0]
		version, content, region := w.wanted, "firmware", ""
		if target == 27 || target == 28 {
			image = source[2]
			version, content, region = "5.17.1", "tunepack", "1"
			binary.LittleEndian.PutUint32(image.Segments[0].Data[30:], uint32(JabraVendorID)<<16|uint32(w.bootPID))
			copy(image.Segments[0].Data[11:21], []byte("5.17.1\x00\x00\x00\x00"))
		} else {
			data := image.Segments[0].Data
			if profile.Legacy {
				copy(data[0x10:0x20], data[0x100:0x110])
			}
			binary.LittleEndian.PutUint32(data[0x80:], uint32(JabraVendorID)<<16|uint32(w.bootPID))
			copy(data[0xc0:], append([]byte(version), 0))
		}
		image.File = GnVFile{Name: fmt.Sprintf("%d.hex", target), Content: content, Target: role, SitelHidTargetID: strconv.Itoa(int(target)), GNPAddress: address, Version: version, RegionID: region, UpdateOrder: i + 1}
		w.images = append(w.images, image)
		archive.Images = append(archive.Images, image)
	}
	return &sitelDECTWorld{base: base, head: head, usb: usb, archive: archive, headSerial: "docked-fixture"}
}

func (w *sitelDECTWorld) runtime(ctx context.Context, device USBDevice, archive *sitelDECTArchive) (sitelDECTIdentity, error) {
	if err := ctx.Err(); err != nil {
		return sitelDECTIdentity{}, err
	}
	id := sitelDECTIdentity{Base: sitelIdentity{Address: 1, PID: w.base.pid, BootPID: w.base.bootPID, Port: device.SysPath, Serial: "base-fixture", Variant: "0172", Version: w.base.version}, Headset: sitelIdentity{Address: 10, PID: w.head.pid, BootPID: w.head.bootPID, Port: device.SysPath, Serial: w.headSerial, Variant: "0372", Version: w.head.version}}
	if w.usb != nil {
		id.USB = sitelIdentity{Address: 2, PID: w.usb.pid, BootPID: w.usb.bootPID, Port: device.SysPath, Serial: "usb-fixture", Variant: "0572", Version: w.usb.version}
	}
	if !archive.Profile.Legacy {
		id.BaseRegion, id.HeadsetRegion = 1, 1
		id.BaseTunes, id.HeadsetTunes = "4.0.0", "4.0.0"
		if w.base.version == w.base.wanted {
			id.BaseTunes = "5.17.1"
		}
		if w.head.version == w.head.wanted {
			id.HeadsetTunes = "5.17.1"
		}
	}
	return id, nil
}
func (w *sitelDECTWorld) enter(context.Context, USBDevice, byte) error {
	if w.saved == nil || w.saved.Phase != "entering-bootloader" {
		return errors.New("DECT entry before checkpoint")
	}
	w.entries++
	w.base.bootMode, w.head.bootMode = true, true
	if w.usb != nil {
		w.usb.bootMode = true
	}
	return nil
}
func (w *sitelDECTWorld) wait(ctx context.Context, previous USBDevice, pid uint16) (USBDevice, error) {
	if err := ctx.Err(); err != nil {
		return USBDevice{}, err
	}
	w.base.generation++
	device := w.base.device()
	if device.ProductID != pid || device.SysPath != previous.SysPath {
		return USBDevice{}, errors.New("DECT reattached on a different port or model")
	}
	return device, nil
}
func (w *sitelDECTWorld) sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }
func (w *sitelDECTWorld) boot(ctx context.Context, _ USBDevice) (*sitelDECTConnection, error) {
	endpoints := map[byte]*sitelTestDevice{1: w.base, 10: w.head}
	if w.usb != nil {
		endpoints[2] = w.usb
	}
	peer := &sitelBootPeer{world: w.base, endpoints: endpoints, onBoot: func(address byte) error {
		if w.saved == nil || w.saved.Phase != "booting-runtime" {
			return errors.New("boot before durable verification")
		}
		w.boots = append(w.boots, address)
		if address == 2 {
			for _, image := range w.base.images {
				for _, segment := range image.Segments {
					for i, value := range segment.Data {
						if w.base.memory[segment.Address+uint32(i)] != value {
							return errors.New("USB reboot before DECT image bytes were correct")
						}
					}
				}
			}
			w.base.bootMode = false
			w.base.version = w.base.wanted
		}
		return nil
	}}
	layout := sitelHIDLayout{ReportID: 10, ReportBytes: 64, MaxMessage: 1024}
	link := &sitelLink{io: peer, in: layout, out: layout, timeout: time.Second}
	if err := link.start(ctx); err != nil {
		return nil, err
	}
	return &sitelDECTConnection{Root: &sitelRequester{link: link, address: 1, timeout: time.Second}, Close: func() error { return nil }}, nil
}
func (w *sitelDECTWorld) run(ctx context.Context, state *firmwareRecoveryState) error {
	// This authorizes only the independent peer; no native backend or device
	// handle is present in these tests.
	previous := commandLineRiskAccepted.Swap(true)
	defer commandLineRiskAccepted.Store(previous)
	check := func() bool { return w.saved != nil && w.saved.Phase == "flashing" }
	w.base.checkpoint, w.head.checkpoint = check, check
	if w.usb != nil {
		w.usb.checkpoint = check
	}
	return runSitelDECTInstall(ctx, w, w.base.device(), w.archive, state, func() error { copyState := *state; w.saved = &copyState; return nil }, nil)
}
func dectTestState(w *sitelDECTWorld) firmwareRecoveryState {
	return firmwareRecoveryState{FormatVersion: 1, ArchiveSHA256: strings.Repeat("a", 64), ProductName: w.archive.Manifest.ProductName, FirmwareVersion: w.archive.Manifest.Version, TargetUSBPIDs: w.archive.Manifest.TargetUSBPIDs, Attempt: 1}
}

func TestDECTComponentsCompleteInOrderAndResume(t *testing.T) {
	for _, profile := range sitelDECTProfiles {
		t.Run(profile.Name, func(t *testing.T) {
			w := makeSitelDECTWorld(profile)
			state := dectTestState(w)
			w.base.failWrite = 3
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := w.run(ctx, &state); err == nil || len(w.boots) != 0 {
				t.Fatal("incomplete base transfer activated a component", err)
			}
			headWrites := w.head.writes
			w.base.failWrite = 0
			if err := w.run(ctx, &state); err != nil {
				t.Fatal(err)
			}
			if w.head.writes != headWrites || len(w.boots) != 2 || w.boots[0] != 10 || w.boots[1] != profile.carrier() || w.entries != 1 {
				t.Fatal("wrong DECT retry or boot order", w.boots)
			}
			baseWrites := w.base.writes
			if err := w.run(ctx, &state); err != nil || w.base.writes != baseWrites || w.head.writes != headWrites {
				t.Fatal("completed DECT update was rewritten", err)
			}
		})
	}
}

func TestDECTAllImagesCheckedBeforeErase(t *testing.T) {
	for _, change := range []func(*sitelDECTWorld){
		func(w *sitelDECTWorld) { w.head.wrongImageID = true },
		func(w *sitelDECTWorld) { w.base.wrongImageID = true },
		func(w *sitelDECTWorld) { w.base.badGeometry = true },
		func(w *sitelDECTWorld) { w.archive.Images[2].Segments[0].Data[0x80] ^= 1 },
		func(w *sitelDECTWorld) { w.archive.Images[1].File.GNPAddress = "1" },
	} {
		w := makeSitelDECTWorld(sitelDECTProfiles[0])
		change(w)
		state := dectTestState(w)
		if err := w.run(context.Background(), &state); err == nil || w.base.erases != 0 || w.head.erases != 0 {
			t.Fatal("invalid DECT image reached erase", err)
		}
	}
}

func TestDECTModeTransitionsAndRecoveryIdentity(t *testing.T) {
	w := makeSitelDECTWorld(sitelDECTProfiles[0])
	state := dectTestState(w)
	w.base.applicationMode, w.head.applicationMode = true, true
	if err := w.run(context.Background(), &state); err != nil {
		t.Fatal(err)
	}
	if len(w.boots) != 2 || w.boots[0] != 10 || w.boots[1] != 1 {
		t.Fatal("application-to-bootloader transition skipped final order")
	}
	w.headSerial = "different-headset"
	writes := w.head.writes + w.base.writes
	if err := w.run(context.Background(), &state); err == nil || writes != w.head.writes+w.base.writes {
		t.Fatal("recovery moved to another docked headset", err)
	}
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	if err := saveFirmwareRecoveryState(state); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFirmwareRecoveryState(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalDECTOriginalArchives(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_SITEL_DECT_AUDIT")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_SITEL_DECT_AUDIT for original DECT archives")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Path, MD5 string
		Images    []struct {
			Target   int
			Address  byte
			Metadata []struct{ Offset, ID uint32 }
		}
	}
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != len(sitelDECTProfiles) {
		t.Fatal("incomplete DECT original-image evidence", err)
	}
	for _, row := range rows {
		archive, err := loadSitelDECTArchive(row.Path)
		if err != nil {
			t.Fatal(err)
		}
		checksum, err := firmwareFileMD5(row.Path)
		if err != nil || checksum != row.MD5 {
			t.Fatal("original DECT archive changed", err)
		}
		if err := ValidateInstallInput([]string{row.Path}); err != nil {
			t.Fatal(err)
		}
		for _, pid := range archive.Profile.RuntimePIDs {
			device := interactiveDFUTestDevice(t, pid)
			if _, err := interactiveInstallBindingForDevices(row.Path, pid, []USBDevice{device}); err != nil {
				t.Fatal(err)
			}
			if os.Getenv("JABRIDGE_TEST_LIVE_RELEASES") == "1" {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := verifySitelDECTRelease(ctx, row.Path, archive, pid)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		w := makeSitelDECTWorld(archive.Profile)
		w.archive = archive
		w.base.wanted = archive.Manifest.Version
		for _, endpoint := range []*sitelTestDevice{w.base, w.head, w.usb} {
			if endpoint == nil {
				continue
			}
			endpoint.images = nil
			endpoint.areas = map[byte]sitelArea{}
			endpoint.memory = map[uint32]byte{}
		}
		for _, image := range archive.Images {
			endpoint := w.base
			if image.File.GNPAddress == "10" {
				endpoint = w.head
			}
			if image.File.GNPAddress == "2" {
				endpoint = w.usb
			}
			if image.File.Content == "firmware" {
				endpoint.wanted = image.File.Version
			}
			target, _ := strconv.Atoi(image.File.SitelHidTargetID)
			kind := byte(0)
			if target == 27 || target == 28 {
				kind = 3
			}
			for _, evidence := range row.Images {
				if evidence.Target == target && kind == 0 {
					if len(evidence.Metadata) != 1 {
						t.Fatal("ambiguous DECT image metadata")
					}
					endpoint.imageInfoOffset = evidence.Metadata[0].Offset
				}
			}
			base := image.Segments[0].Address
			last := image.Segments[len(image.Segments)-1]
			size := uint32((uint64(last.Address) + uint64(len(last.Data)) - uint64(base) + 255) / 256 * 256)
			endpoint.areas[kind] = sitelArea{Address: base, Size: size}
			endpoint.images = append(endpoint.images, image)
			for i := uint32(0); i < size; i++ {
				endpoint.memory[base+i] = 255
			}
		}
		state := dectTestState(w)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = w.run(ctx, &state)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		usbWrites := 0
		if w.usb != nil {
			usbWrites = w.usb.writes
		}
		t.Logf("%s: %d base writes, %d headset writes, %d USB controller writes; all %d runtime IDs passed preflight", archive.Profile.Name, w.base.writes, w.head.writes, usbWrites, len(archive.Profile.RuntimePIDs))
	}
}
