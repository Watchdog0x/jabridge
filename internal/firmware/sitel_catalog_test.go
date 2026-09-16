package firmware

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"
)

func makeLegacySitelWorld() *sitelTestDevice {
	w := makeEngageWorld(false)
	w.pid, w.bootPID, w.runtimeAddress, w.noSerial = 0x0300, 0x0304, 8, true
	w.images = w.images[:1]
	binary.LittleEndian.PutUint32(w.images[0].Segments[0].Data[0x80:], 0x0b0e0304)
	return w
}

func TestLegacySitelRuntimeAddressAndAbsentSerial(t *testing.T) {
	w := makeLegacySitelWorld()
	state := firmwareRecoveryState{ArchiveSHA256: "fixture"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runSitelInstall(ctx, w, w.device(), w.images, w.wanted, &state, func() error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if state.USBSerialSHA256 != "" || w.activations != 0 || w.version != w.wanted || w.writes != 32 {
		t.Fatal("legacy device identity, routing or transfer was guessed")
	}
}

func TestEngageFamilyControllerRoutes(t *testing.T) {
	for _, boot := range []uint16{0x4000, 0x4050, 0x4060} {
		for variant := uint16(1); variant <= 6; variant++ {
			pid := boot + variant
			t.Run(strconv.FormatUint(uint64(pid), 16), func(t *testing.T) {
				w := makeEngageWorld(variant <= 4)
				w.pid, w.bootPID = pid, boot
				binary.LittleEndian.PutUint32(w.images[0].Segments[0].Data[0x80:], 0x0b0e0000|uint32(boot))
				binary.LittleEndian.PutUint32(w.images[2].Segments[0].Data[30:], 0x0b0e0000|uint32(boot))
				state := firmwareRecoveryState{ArchiveSHA256: "fixture"}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := runSitelInstall(ctx, w, w.device(), w.images, w.wanted, &state, func() error { return nil }, nil); err != nil {
					t.Fatal(err)
				}
				if (w.activations == 1) != (variant <= 4) {
					t.Fatal("wrong controller activation route", pid, w.activations)
				}
			})
		}
	}
}

func TestLocalSitelCatalogOriginalArchives(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_SITEL_IMAGE_AUDIT")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_SITEL_IMAGE_AUDIT for original-archive checks")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Path, MD5, Version string
		BootPID            string   `json:"boot_pid"`
		HeaderOffsets      []uint32 `json:"header_offsets"`
	}
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != len(sitelProfiles) {
		t.Fatalf("incomplete original-image evidence: %d archives for %d profiles: %v", len(rows), len(sitelProfiles), err)
	}
	checked := map[uint16]bool{}
	for _, row := range rows {
		t.Run(row.BootPID, func(t *testing.T) {
			boot, err := strconv.ParseUint(row.BootPID, 16, 16)
			if err != nil || len(row.HeaderOffsets) == 0 || checked[uint16(boot)] {
				t.Fatal("invalid or repeated original-image evidence", row.BootPID)
			}
			checked[uint16(boot)] = true
			manifest, images, err := loadSitelImages(row.Path)
			if err != nil {
				t.Fatal(err)
			}
			profile, err := sitelProfileForManifest(manifest)
			if err != nil || profile.BootPID != uint16(boot) || manifest.Version != row.Version {
				t.Fatal("original archive picked the wrong profile", err)
			}
			md5, err := firmwareFileMD5(row.Path)
			if err != nil || md5 != row.MD5 {
				t.Fatal("original archive checksum changed", err)
			}
			if err := ValidateInstallInput([]string{row.Path}); err != nil {
				t.Fatal(err)
			}
			for _, pid := range profile.RuntimePIDs {
				device := interactiveDFUTestDevice(t, pid)
				if _, err := interactiveInstallBindingForDevices(row.Path, pid, []USBDevice{device}); err != nil {
					t.Fatalf("runtime %04x cannot prepare its original archive: %v", pid, err)
				}
			}
			for _, offset := range row.HeaderOffsets {
				w := makeEngageWorld(false)
				w.pid, w.bootPID, w.wanted = profile.RuntimePIDs[0], profile.BootPID, manifest.Version
				w.images, w.imageInfoOffset, w.memory = images, offset, map[uint32]byte{}
				if profile.LegacySingleImage {
					w.runtimeAddress, w.noSerial = 8, true
				}
				for _, image := range images {
					target, err := strconv.Atoi(image.File.SitelHidTargetID)
					if err != nil {
						t.Fatal(err)
					}
					var kind byte
					switch target {
					case 3:
						kind = 0
					case 27:
						kind = 3
					case 29:
						kind = 4
					default:
						t.Fatal("unexpected original image target", target)
					}
					base := image.Segments[0].Address
					last := image.Segments[len(image.Segments)-1]
					size := uint32((uint64(last.Address) + uint64(len(last.Data)) - uint64(base) + 255) / 256 * 256)
					// Explicit simulated flash geometry. Production always
					// reads areas and header offsets from the real bootloader.
					w.areas[kind] = sitelArea{Address: base, Size: size}
					for i := uint32(0); i < size; i++ {
						w.memory[base+i] = 255
					}
				}
				state := firmwareRecoveryState{ArchiveSHA256: "fixture"}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err = runSitelInstall(ctx, w, w.device(), images, manifest.Version, &state, func() error { return nil }, nil)
				cancel()
				if err != nil || w.version != manifest.Version {
					t.Fatal("original-image transfer did not verify", err)
				}
				t.Logf("%s %s: header offset %x, %d simulated writes, %d controller activations", profile.Name, manifest.Version, offset, w.writes, w.activations)
			}
		})
	}
}
