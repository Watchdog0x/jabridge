package firmware

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These peers use explicit catalog/manifest counterparts, independent of the
// production profile table. They model host transfers, not physical devices.
var evolve2SitelCases = []struct {
	name    string
	pids    []uint16
	bootPID uint16
}{
	{"Evolve2 40", []uint16{0x0e40, 0x0e41, 0x0e42, 0x0e43}, 0x0e44},
	{"Evolve2 40 SE", []uint16{0x2e40, 0x2e41, 0x2e42, 0x2e43}, 0x2e44},
	{"Evolve2 30 and Connect 4h", []uint16{0x0e30, 0x0e31, 0x0e32, 0x0e33, 0x0e35}, 0x0e34},
	{"Evolve2 30 SE", []uint16{0x0e36, 0x0e37, 0x0e38, 0x0e39}, 0x0e3a},
}

func makeEvolve2World(pid, bootPID uint16) *sitelTestDevice {
	w := makeEngageWorld(false)
	w.pid, w.bootPID, w.version, w.wanted = pid, bootPID, "1.15.0", "2.11.1"
	w.images = []sitelPlannedImage{w.images[0], w.images[2]}
	delete(w.areas, 4)
	for i := range w.images {
		image := &w.images[i]
		image.File.Version = w.wanted
		data := image.Segments[0].Data
		if i == 0 {
			binary.LittleEndian.PutUint32(data[0x80:], 0x0b0e0000|uint32(bootPID))
			copy(data[0xc0:], []byte(w.wanted+"\x00"))
		} else {
			binary.LittleEndian.PutUint32(data[30:], 0x0b0e0000|uint32(bootPID))
			copy(data[11:21], append([]byte(w.wanted), make([]byte, 10-len(w.wanted))...))
		}
	}
	return w
}

func TestEvolve2SitelUpdateEveryRuntimeVariant(t *testing.T) {
	for _, family := range evolve2SitelCases {
		for _, pid := range family.pids {
			t.Run(fmt.Sprintf("%s/%04x", family.name, pid), func(t *testing.T) {
				w := makeEvolve2World(pid, family.bootPID)
				state := firmwareRecoveryState{ArchiveSHA256: "fixture"}
				saved := false
				w.checkpoint = func() bool { return saved }
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := runSitelInstall(ctx, w, w.device(), w.images, w.wanted, &state, func() error { saved = true; return nil }, nil); err != nil {
					t.Fatal(err)
				}
				if w.version != w.wanted || w.bootMode || w.writes != 64 || w.erases != 32 || w.activations != 0 || state.BootPID != family.bootPID || state.RuntimePID != pid {
					t.Fatalf("wrong update result: version=%s writes=%d erases=%d activations=%d state=%+v", w.version, w.writes, w.erases, w.activations, state)
				}
				if !NativeFirmwareProtocolSupported(pid, 4) {
					t.Fatal("diagnostics still report this model as unsupported")
				}
			})
		}
	}
}

func TestEvolve2SitelRejectsWrongImagesBeforeErasing(t *testing.T) {
	for _, failure := range []string{"controller-image", "missing-tunes", "wrong-image-id", "wrong-bootloader-id", "wrong-order", "wrong-version", "wrong-address", "wrong-port"} {
		t.Run(failure, func(t *testing.T) {
			w := makeEvolve2World(0x0e41, 0x0e44)
			switch failure {
			case "controller-image":
				w.images = makeEngageWorld(false).images
			case "missing-tunes":
				w.images = w.images[:1]
			case "wrong-image-id":
				binary.LittleEndian.PutUint32(w.images[0].Segments[0].Data[0x80:], 0x0b0e4050)
			case "wrong-bootloader-id":
				w.bootPID = 0x4050
			case "wrong-order":
				w.images[1].File.UpdateOrder = w.images[0].File.UpdateOrder
			case "wrong-version":
				w.images[1].File.Version = "9.9.9"
			case "wrong-address":
				w.images[1].File.GNPAddress = "3"
			case "wrong-port":
				w.wrongPort = true
			}
			state := firmwareRecoveryState{ArchiveSHA256: "fixture"}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := runSitelInstall(ctx, w, w.device(), w.images, w.wanted, &state, func() error { return nil }, nil); err == nil || w.erases != 0 || w.writes != 0 {
				t.Fatal("invalid target or images reached flash writes", err, w.erases, w.writes)
			}
		})
	}
}

func TestEvolve2SitelInterruptedRecoveryFromDisk(t *testing.T) {
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	w := makeEvolve2World(0x0e41, 0x0e44)
	w.failWrite = 3
	state := firmwareRecoveryState{FormatVersion: 1, ArchiveSHA256: strings.Repeat("a", 64), ProductName: "Jabra_Evolve2_40", FirmwareVersion: w.wanted, TargetUSBPIDs: []string{"0x0E44"}, Attempt: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := runSitelInstall(ctx, w, w.device(), w.images, w.wanted, &state, func() error { return saveFirmwareRecoveryState(state) }, nil); err == nil || !w.bootMode {
		t.Fatal("expected interrupted bootloader transfer", err)
	}
	reloaded, err := loadFirmwareRecoveryState()
	if err != nil {
		t.Fatal(err)
	}
	wrong := reloaded
	wrong.BootPID = 0x4050
	if err := saveFirmwareRecoveryState(wrong); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFirmwareRecoveryState(); err == nil {
		t.Fatal("cross-model recovery record accepted")
	}
	before := w.writes
	replacement := w.device()
	replacement.Serial = "replacement"
	if err := runSitelInstall(ctx, w, replacement, w.images, w.wanted, &reloaded, func() error { return nil }, nil); err == nil || w.writes != before {
		t.Fatal("replacement device accepted for recovery")
	}
	w.failWrite = 0
	if err := runSitelInstall(ctx, w, w.device(), w.images, w.wanted, &reloaded, func() error { return saveFirmwareRecoveryState(reloaded) }, nil); err != nil {
		t.Fatal(err)
	}
	if w.version != w.wanted || w.bootMode || w.activations != 0 {
		t.Fatal("recovery did not verify the requested firmware")
	}
}

func TestLocalEvolve2OriginalArchivesThroughNativeUpdater(t *testing.T) {
	dir := os.Getenv("JABRIDGE_TEST_SITEL_ARCHIVE_DIR")
	if dir == "" {
		t.Skip("set JABRIDGE_TEST_SITEL_ARCHIVE_DIR for original-archive transfer checks")
	}
	for _, family := range evolve2SitelCases {
		pattern := strings.ReplaceAll(family.name, " ", "_")
		if family.bootPID == 0x0e34 {
			pattern = "Evolve2_30"
		}
		paths, err := filepath.Glob(filepath.Join(dir, "Jabra_"+pattern+"_*.zip"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("missing original %s archive: %v", family.name, err)
		}
		matched := 0
		for _, path := range paths {
			// Evolve2_30_*.zip also matches Evolve2_30_SE; that profile has
			// its own case and bootloader, so use the manifest identity.
			manifest, images, err := loadSitelImages(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.EqualFold(manifest.TargetUSBPIDs[0], fmt.Sprintf("0x%04x", family.bootPID)) {
				continue
			}
			matched++
			t.Run(filepath.Base(path), func(t *testing.T) {
				if err := ValidateInstallInput([]string{path}); err != nil {
					t.Fatal(err)
				}
				w := makeEvolve2World(family.pids[0], family.bootPID)
				if family.bootPID == 0x0e3a || family.bootPID == 0x2e44 {
					// SE image headers are at +0x80, verified separately with
					// the original updater. Production reads the live offset.
					w.imageInfoOffset = 0x80
				}
				w.images, w.wanted, w.memory = images, manifest.Version, map[uint32]byte{}
				for i, image := range images {
					base := image.Segments[0].Address
					last := image.Segments[len(image.Segments)-1]
					end := uint64(last.Address) + uint64(len(last.Data))
					size := uint32((end - uint64(base) + 255) / 256 * 256)
					// Explicit simulated geometry; hardware supplies its own.
					w.areas[[]byte{0, 3}[i]] = sitelArea{Address: base, Size: size}
					for j := uint32(0); j < size; j++ {
						w.memory[base+j] = 255
					}
				}
				state := firmwareRecoveryState{ArchiveSHA256: "fixture"}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := runSitelInstall(ctx, w, w.device(), images, manifest.Version, &state, func() error { return nil }, nil); err != nil {
					t.Fatal(err)
				}
				if w.version != manifest.Version || w.activations != 0 {
					t.Fatal("wrong final firmware or unexpected controller activation")
				}
				t.Logf("original firmware %s: %d writes, %d erases, no physical hardware", manifest.Version, w.writes, w.erases)
			})
		}
		if matched == 0 {
			t.Fatalf("no original archive matched %s bootloader", family.name)
		}
	}
}
