package firmware

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"
)

// Three independent HEX flash endpoints, a CPU/controller model for the radio,
// and a BCCMD settings store share the real host's HID/HP transport. Geometry
// and application responses are simulated; radio application code is not run.
type engage75World struct {
	*sitelDECTWorld
	mmi             *sitelTestDevice
	radio           *internalFlashSim
	mailbox         *sitelMailboxPeer
	application     bool
	radioVersion    string
	settingsFailure bool
	settingsWrites  int
	versionFault    byte
	stages          []byte
	saveFailure     string
}

func syntheticEngage75Archive() *sitelDECTArchive {
	manifest := &BuildVector{ProductName: "Jabra Engage 75", Version: "5.20.1", TargetUSBPIDs: []string{"0x1111"}}
	e := &engage75Archive{Manifest: manifest, Radio: map[string]*sitelBluecoreImage{}, Settings: map[string][]sitelPSRRecord{}}
	for _, chip := range []string{"gordon", "rick"} {
		e.Radio[chip] = internalTestImage(chip)
		e.Settings[chip] = []sitelPSRRecord{{Chip: chip, Key: 0x123, Delete: true}, {Chip: chip, Key: 0x123, Words: []uint16{3, 4}}, {Chip: chip, Key: 0xf002, Words: []uint16{0, 0, 0, 0x1000}}}
		if chip == "rick" {
			e.Settings[chip][2].Words[3] = 0x2000
		}
	}
	for i, target := range engage75TargetOrder {
		file := GnVFile{Name: fmt.Sprintf("%d.hex", target), SitelHidTargetID: strconv.Itoa(int(target)), UpdateOrder: i + 1, Version: "5.20.1", GNPAddress: "1", Target: "base", Content: "firmware", Subtarget: "dect"}
		pid, base, header := uint16(0x1111), uint32(0x60000), 0x100
		switch target {
		case 1:
			pid, base, file.GNPAddress, file.Target, file.Version = 0x1116, 0x30000, "10", "headset", "5.19.3"
		case 28:
			pid, base, file.GNPAddress, file.Target, file.Content, file.RegionID = 0x1116, 0xc8000, "10", "headset", "tunepack", "1"
		case 23, 24:
			file.GNPAddress, file.Subtarget = "2", "bluecore"
			if target == 24 {
				file.Content = "ps_keys"
			}
			manifest.Files = append(manifest.Files, file)
			continue
		case 22:
			pid, base, header, file.GNPAddress, file.Subtarget = 0x1117, 0x08005000, 0x400, "3", "mmi"
		case 5:
			base, file.Content, file.RegionID = 0x170000, "langpack", "7"
		case 4:
			base, file.Content = 0x160000, "graphics"
		case 27:
			base, file.Content, file.RegionID = 0x380000, "tunepack", "1"
		}
		data := make([]byte, 2048)
		for j := range data {
			data[j] = byte(j*19 + int(target))
		}
		if file.Content != "firmware" {
			file.Version, file.Subtarget = "5.17.1", ""
			clear(data[11:21])
			copy(data[11:21], file.Version)
			binary.LittleEndian.PutUint32(data[30:], 0x0b0e0000|uint32(pid))
		} else {
			for j, offset := range []uint32{0x80, 0x90, 0xa0, 0xc0} {
				binary.LittleEndian.PutUint32(data[header+j*4:], base+offset)
			}
			binary.LittleEndian.PutUint32(data[0x80:], 0x0b0e0000|uint32(pid))
			copy(data[0xc0:], append([]byte(file.Version), 0))
		}
		manifest.Files = append(manifest.Files, file)
		e.HEX = append(e.HEX, sitelPlannedImage{File: file, Segments: []hexImageSegment{{Address: base, Data: data}}})
	}
	return &sitelDECTArchive{Manifest: manifest, Profile: engage75Profile, Images: e.HEX, Engage75: e}
}

func newEngage75World(archive *sitelDECTArchive, revision uint16) *engage75World {
	w := &engage75World{sitelDECTWorld: makeSitelDECTWorld(sitelDECTProfiles[1]), mmi: makeEngageWorld(false), radio: newInternalFlashSim(revision), mailbox: newSitelMailboxPeer(), radioVersion: "5.10.0"}
	w.archive = archive
	w.base.pid, w.base.bootPID = 0x1110, 0x1111
	w.head.pid, w.head.bootPID = 0x1116, 0x1116
	w.mmi.pid, w.mmi.bootPID, w.mmi.imageInfoOffset = 0x1117, 0x1117, 0x400
	w.mailbox.memory[0xfe81] = revision
	for _, endpoint := range []*sitelTestDevice{w.base, w.head, w.mmi} {
		endpoint.images = nil
		endpoint.areas = map[byte]sitelArea{}
		endpoint.memory = map[uint32]byte{}
		endpoint.version = "4.0.0"
	}
	for _, image := range archive.Images {
		endpoint := w.base
		if image.File.GNPAddress == "10" {
			endpoint = w.head
		}
		if image.File.GNPAddress == "3" {
			endpoint = w.mmi
		}
		kind := byte(0)
		switch image.File.Content {
		case "tunepack":
			kind = 3
		case "langpack":
			kind = 1
		case "graphics":
			kind = 2
		case "firmware":
			endpoint.wanted = image.File.Version
		}
		base := image.Segments[0].Address
		last := image.Segments[len(image.Segments)-1]
		size := (last.Address + uint32(len(last.Data)) - base + 255) &^ 255
		endpoint.areas[kind] = sitelArea{Address: base, Size: size}
		endpoint.images = append(endpoint.images, image)
		for i := uint32(0); i < size; i++ {
			endpoint.memory[base+i] = 255
		}
	}
	return w
}

func (w *engage75World) runtime(ctx context.Context, device USBDevice, archive *sitelDECTArchive) (sitelDECTIdentity, error) {
	return (&sitelRuntime{io: &engage75ManagementPeer{world: w}}).identifyDECT(ctx, device, archive)
}
func (w *engage75World) enter(ctx context.Context, device USBDevice, address byte) error {
	w.mmi.bootMode = true
	return w.sitelDECTWorld.enter(ctx, device, address)
}
func (w *engage75World) boot(ctx context.Context, _ USBDevice) (*sitelDECTConnection, error) {
	plain := &sitelBootPeer{world: w.base, endpoints: map[byte]*sitelTestDevice{1: w.base, 10: w.head, 3: w.mmi}, onBoot: func(address byte) error {
		if w.saved == nil || w.saved.Phase != "booting-runtime" || !w.saved.SitelDECT.SettingsVerified {
			return errors.New("application boot before all components completed")
		}
		w.boots = append(w.boots, address)
		if address == 1 {
			w.mmi.version = w.mmi.wanted
			w.mmi.bootMode = false
		}
		return nil
	}}
	peer := &sitelBootPeer{world: w.base, onPacket: func(packet []byte) ([]byte, error) {
		if len(packet) > 5 && packet[4] == 15 && packet[5] >= 0x0b && packet[5] <= 0x15 {
			return simulateSitelSPIPacket(packet, w)
		}
		return plain.handle(packet)
	}}
	layout := sitelHIDLayout{ReportID: 10, ReportBytes: 64, MaxMessage: 1024}
	link := &sitelLink{io: peer, in: layout, out: layout, timeout: 2 * time.Second}
	if err := link.start(ctx); err != nil {
		return nil, err
	}
	return &sitelDECTConnection{Root: &sitelRequester{link: link, address: 1, timeout: 30 * time.Second}, Close: func() error { return nil }}, nil
}
func (w *engage75World) read(ctx context.Context, address uint16, count int, verified bool) ([]uint16, error) {
	if w.application && address < 0xf000 {
		return w.mailbox.read(ctx, address, count, verified)
	}
	return w.radio.read(ctx, address, count, verified)
}
func (w *engage75World) write(ctx context.Context, address uint16, words []uint16, verified bool) error {
	if w.application && address < 0xf000 {
		isSetting := address == 0x320 && w.mailbox.memory[0x300] == 4 && w.mailbox.memory[0x400] == 2
		if err := w.mailbox.write(ctx, address, words, verified); err != nil {
			return err
		}
		if isSetting {
			w.settingsWrites++
			if w.settingsFailure {
				w.settingsFailure = false
				return errors.New("lost settings acknowledgement")
			}
		}
		return nil
	}
	if address == 0xf82f {
		w.application = false
		w.mailbox.memory[0x300] = 0
	}
	boots := w.radio.boots
	if err := w.radio.write(ctx, address, words, verified); err != nil {
		return err
	}
	if w.radio.boots != boots {
		chip := "gordon"
		if byte(w.radio.debug[0xfe81]) == 0x35 {
			chip = "rick"
		}
		for sector, wanted := range w.archive.Engage75.Radio[chip].Sectors {
			if !slices.Equal(w.radio.cpu.flash[int(sector)*4096:int(sector+1)*4096], wanted) {
				return errors.New("radio started with incomplete flash")
			}
		}
		w.application = true
		w.radioVersion = w.archive.Manifest.Version
	}
	return nil
}
func (w *engage75World) run(ctx context.Context, state *firmwareRecoveryState) error {
	previous := commandLineRiskAccepted.Swap(true)
	defer commandLineRiskAccepted.Store(previous)
	for _, endpoint := range []*sitelTestDevice{w.base, w.head, w.mmi} {
		endpoint.checkpoint = func() bool { return w.saved != nil && w.saved.Phase == "flashing" }
	}
	w.stages = nil
	return runSitelDECTInstall(ctx, w, w.base.device(), w.archive, state, func() error {
		if w.saveFailure == state.Phase {
			return errors.New("simulated checkpoint failure")
		}
		copyState := *state
		if state.SitelDECT != nil {
			copyDECT := *state.SitelDECT
			copyState.SitelDECT = &copyDECT
		}
		w.saved = &copyState
		return nil
	}, func(target byte, _, _ int) {
		if len(w.stages) == 0 || w.stages[len(w.stages)-1] != target {
			w.stages = append(w.stages, target)
		}
	})
}

type engage75ManagementPeer struct {
	world *engage75World
	queue [][]byte
}

func (p *engage75ManagementPeer) Write(ctx context.Context, packet []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(packet) != 64 || packet[0] != 5 || packet[5] != 2 || packet[4] != 0x46 {
		return errors.New("unexpected Engage 75 management query")
	}
	w := p.world
	address, op := packet[1], packet[6]
	endpoint := map[byte]*sitelTestDevice{1: w.base, 10: w.head, 3: w.mmi}[address]
	var data []byte
	if address == 2 && op == 3 {
		if w.radioVersion == "" {
			return errors.New("radio application is unavailable")
		}
		data = []byte(w.radioVersion)
	} else if endpoint == nil {
		return errors.New("unknown Engage 75 management address")
	} else {
		switch op {
		case 0x11:
			data = []byte{byte(endpoint.pid), byte(endpoint.pid >> 8)}
		case 0x13:
			data = []byte{byte(endpoint.bootPID), byte(endpoint.bootPID >> 8)}
		case 0x14:
			data = []byte{4}
		case 1:
			data = []byte(fmt.Sprintf("component-%d", address))
			if address == 10 {
				data = []byte(w.headSerial)
			}
		case 2:
			data = []byte{1, 2, address}
		case 3:
			data = []byte(endpoint.version)
		case 0x22:
			data = []byte{1}
		case 7:
			data = []byte{7}
		case 6, 9, 0x21:
			version := "4.0.0"
			if endpoint.version == endpoint.wanted {
				version = "5.17.1"
			}
			data = []byte(version)
		default:
			return fmt.Errorf("unexpected Engage 75 IDENT subcommand %d", op)
		}
	}
	if w.versionFault == op && len(w.boots) > 0 {
		data = []byte("0.0.1")
	}
	if op == 1 || op == 3 || op == 6 || op == 9 || op == 0x21 {
		data = append([]byte{byte(len(data))}, data...)
	}
	reply := []byte{5, 0, address, packet[3], 0xc0 | byte(6+len(data)), 2, op}
	reply = append(reply, data...)
	p.queue = append(p.queue, reply)
	return nil
}

func TestEngage75RejectsFaultsBeforeActivation(t *testing.T) {
	for _, fault := range []string{"radio-id", "mmi-id", "head-geometry", "settings-chip", "checkpoint", "radio-readback", "version", "graphics", "language", "tunes", "missing-radio-version"} {
		t.Run(fault, func(t *testing.T) {
			w := newEngage75World(syntheticEngage75Archive(), 0x35)
			state := dectTestState(w.sitelDECTWorld)
			switch fault {
			case "radio-id":
				w.radio.debug[0xfe81] = 0x34
			case "mmi-id":
				w.mmi.wrongImageID = true
			case "head-geometry":
				w.head.badGeometry = true
			case "settings-chip":
				w.archive.Engage75.Settings["rick"][2].Chip = "gordon"
			case "checkpoint":
				w.saveFailure = "flashing"
			case "radio-readback":
				w.radio.badReadback = true
			case "version":
				w.versionFault = 3
			case "graphics":
				w.versionFault = 9
			case "language":
				w.versionFault = 6
			case "tunes":
				w.versionFault = 0x21
			case "missing-radio-version":
				w.radioVersion = ""
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := w.run(ctx, &state); err == nil {
				t.Fatal("invalid update reported success")
			}
			if w.versionFault == 0 && len(w.boots) != 0 {
				t.Fatal("failed transfer booted the base")
			}
			if fault != "radio-readback" && w.versionFault == 0 && w.base.erases+w.head.erases+w.mmi.erases != 0 {
				t.Fatal("invalid plan erased flash")
			}
		})
	}
}

func TestEngage75RecoveryBindingAndBrokenRadio(t *testing.T) {
	for _, change := range []string{"broken-radio", "serial", "missing-serial", "port", "radio", "headset"} {
		t.Run(change, func(t *testing.T) {
			w := newEngage75World(syntheticEngage75Archive(), 0x35)
			state := dectTestState(w.sitelDECTWorld)
			w.settingsFailure = true
			if err := w.run(context.Background(), &state); err == nil {
				t.Fatal("missing injected interruption")
			}
			// Resume in application mode, as after a base power cycle. The
			// radio application can be broken while the base still enumerates.
			w.base.bootMode = false
			before := w.head.erases + w.base.erases + w.mmi.erases
			switch change {
			case "broken-radio":
				w.radioVersion = ""
			case "serial":
				state.USBSerialSHA256 = fmt.Sprintf("%064x", 1)
			case "missing-serial":
				w.base.noSerial = true
			case "port":
				w.base.wrongPort = true
				w.base.generation++
			case "radio":
				w.radio.debug[0xfe81] = 0x28
			case "headset":
				w.headSerial = "replacement"
			}
			err := w.run(context.Background(), &state)
			if change == "broken-radio" {
				if err != nil {
					t.Fatal("bound broken-radio recovery failed", err)
				}
			} else if err == nil || before != w.head.erases+w.base.erases+w.mmi.erases {
				t.Fatal("replacement reached erase", err)
			}
		})
	}
}
func (p *engage75ManagementPeer) Read(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(p.queue) == 0 {
		return nil, errors.New("no management reply")
	}
	v := p.queue[0]
	p.queue = p.queue[1:]
	return v, nil
}

func TestEngage75CompleteSimulationAndSettingsRecovery(t *testing.T) {
	for _, revision := range []uint16{0x28, 0x35} {
		t.Run(fmt.Sprintf("%02x", revision), func(t *testing.T) {
			w := newEngage75World(syntheticEngage75Archive(), revision)
			state := dectTestState(w.sitelDECTWorld)
			w.settingsFailure = true
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := w.run(ctx, &state); err == nil || len(w.boots) != 0 || state.SitelDECT == nil || state.SitelDECT.SettingsVerified {
				t.Fatal("incomplete settings were accepted", err)
			}
			if w.radioVersion != w.archive.Manifest.Version {
				t.Fatal("fault did not happen after radio boot")
			}
			writes := w.radio.cpu.programs
			if err := w.run(ctx, &state); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(w.stages, engage75TargetOrder) || !slices.Equal(w.boots, []byte{10, 1}) || w.radio.cpu.programs != writes {
				t.Fatal("incorrect resume or component order", w.stages, w.boots)
			}
			wanted := uint16(0x1000)
			if revision == 0x35 {
				wanted = 0x2000
			}
			if !slices.Equal(w.mailbox.keys[0xf002], []uint16{0, 0, 0, wanted}) {
				t.Fatal("wrong chip settings")
			}
			settingsWrites := w.settingsWrites
			if err := w.run(ctx, &state); err != nil || w.settingsWrites != settingsWrites || w.entries != 1 {
				t.Fatal("completed update repeated", err)
			}
		})
	}
}

func TestLocalEngage75WholeArchiveSimulation(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_ENGAGE75_ARCHIVE")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_ENGAGE75_ARCHIVE for full original nine-component simulation")
	}
	archive, err := loadSitelDECTArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range engage75RuntimePIDs {
		if !NativeFirmwareProtocolSupported(pid, 4) {
			t.Fatalf("missing protocol route %04x", pid)
		}
		if _, err := interactiveInstallBindingForDevices(path, pid, []USBDevice{interactiveDFUTestDevice(t, pid)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, revision := range []uint16{0x28, 0x35} {
		w := newEngage75World(archive, revision)
		state := dectTestState(w.sitelDECTWorld)
		protected := slices.Clone(w.radio.cpu.flash[251*4096:])
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		err := w.run(ctx, &state)
		cancel()
		if err != nil {
			t.Fatal(err, "completed stages", w.stages, "HEX writes", w.base.writes, w.head.writes, w.mmi.writes, "radio words", w.radio.cpu.programs)
		}
		if !slices.Equal(w.stages, engage75TargetOrder) || !slices.Equal(w.radio.cpu.flash[251*4096:], protected) {
			t.Fatal("wrong original-package stages or reserved flash")
		}
		t.Logf("revision %02x: all nine original components, %d base / %d headset / %d display writes, %d radio words, %d settings operations; final versions verified", revision, w.base.writes, w.head.writes, w.mmi.writes, w.radio.cpu.programs, w.settingsWrites)
	}
}
