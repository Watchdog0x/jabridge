package firmware

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func syntheticDFU(pid uint16, version string) []byte {
	data := make([]byte, 160+16)
	copy(data, "CSR-dfu2")
	binary.LittleEndian.PutUint16(data[8:10], 3)
	binary.LittleEndian.PutUint32(data[10:14], uint32(len(data)-16))
	binary.LittleEndian.PutUint16(data[14:16], 32)
	copy(data[16:32], strings.Repeat(" ", 16))
	copy(data[16:32], "1 "+version)
	suffix := data[len(data)-16:]
	binary.LittleEndian.PutUint16(suffix[2:4], pid)
	binary.LittleEndian.PutUint16(suffix[4:6], JabraVendorID)
	binary.LittleEndian.PutUint16(suffix[6:8], 0x0100)
	copy(suffix[8:12], []byte{'U', 'F', 'D', 16})
	repairSyntheticDFUCRC(data)
	return data
}

func repairSyntheticDFUCRC(data []byte) {
	binary.LittleEndian.PutUint32(data[len(data)-4:], ^crc32.ChecksumIEEE(data[:len(data)-4]))
}

func syntheticDFUZip(t *testing.T, target uint16, data []byte, extra string) []byte {
	t.Helper()
	var out bytes.Buffer
	archive := zip.NewWriter(&out)
	manifest := fmt.Sprintf(`<buildVector version="1.2.3" productName="Synthetic"><targetUsbPids><usbPid>0x%04x</usbPid></targetUsbPids><files><file name="image.dfu"><content>firmware</content><version>1.2.3</version><target>headset</target><partition>0</partition></file></files></buildVector>`, target)
	for _, entry := range []struct {
		name string
		data []byte
	}{{"info.xml", []byte(manifest)}, {"image.dfu", data}, {extra, []byte("extra")}} {
		if entry.name == "" {
			continue
		}
		writer, err := archive.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestJabraDFUProfilesAcceptRawAndWrappedImages(t *testing.T) {
	for _, profile := range usbDFUProfiles {
		data := syntheticDFU(profile.DFUPID, "1.2.3")
		for _, file := range [][]byte{data, syntheticDFUZip(t, profile.DFUPID, data, "")} {
			image, err := parseJabraDFUImage(file)
			if err != nil {
				t.Fatal(profile.Name, err)
			}
			if image.Profile.Name != profile.Name || image.Manifest.Version != "1.2.3" ||
				!bytes.Equal(image.Payload, data[:len(data)-16]) || len(image.SHA) != 64 || len(image.MD5) != 32 {
				t.Fatal("wrong image/target/suffix handling")
			}
		}
	}
}

func TestJabraDFURejectsCorruptionWrongTargetsAndAmbiguousArchives(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func([]byte)
	}{
		{"CRC", func(data []byte) { data[40] ^= 1 }},
		{"wrong vendor", func(data []byte) { data[len(data)-12] = 1; repairSyntheticDFUCRC(data) }},
		{"wrong target", func(data []byte) { data[len(data)-14] = 1; repairSyntheticDFUCRC(data) }},
		{"suffix signature", func(data []byte) { data[len(data)-8] = 0; repairSyntheticDFUCRC(data) }},
		{"suffix size", func(data []byte) { data[len(data)-5] = 15; repairSyntheticDFUCRC(data) }},
		{"header version", func(data []byte) { data[8] = 9; repairSyntheticDFUCRC(data) }},
		{"declared size", func(data []byte) { data[10] ^= 1; repairSyntheticDFUCRC(data) }},
		{"header length", func(data []byte) { data[14] = 255; repairSyntheticDFUCRC(data) }},
		{"image version", func(data []byte) { data[18] = '9'; repairSyntheticDFUCRC(data) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := syntheticDFU(0x0421, "1.2.3")
			test.edit(data)
			if _, err := parseJabraDFUImage(syntheticDFUZip(t, 0x0421, data, "")); err == nil {
				t.Fatal("accepted invalid image")
			}
		})
	}
	for _, extra := range []string{"info.xml", "../image.dfu", "image.dfu", "other.bin"} {
		if _, err := parseJabraDFUImage(syntheticDFUZip(t, 0x0421, syntheticDFU(0x0421, "1.2.3"), extra)); err == nil {
			t.Fatal("accepted extra/duplicate archive entry", extra)
		}
	}
	if _, err := parseJabraDFUImage(syntheticDFUZip(t, 0x0982, syntheticDFU(0x0421, "1.2.3"), "")); err == nil {
		t.Fatal("accepted manifest/image model mismatch")
	}
}

func TestUSBDFUTargetSelectionDoesNotUseDonglesOrOtherModels(t *testing.T) {
	profile, _ := usbDFUProfileForPID(0x0422)
	link := USBDevice{SysPath: "/link", VendorID: JabraVendorID, ProductID: 0x24c7}
	headset := USBDevice{SysPath: "/speak", VendorID: JabraVendorID, ProductID: 0x0422}
	if _, err := selectUSBDFUTarget([]USBDevice{link}, profile); err == nil {
		t.Fatal("accepted Link for Speak firmware")
	}
	if got, err := selectUSBDFUTarget([]USBDevice{link, headset}, profile); err != nil || got.SysPath != headset.SysPath {
		t.Fatal(got, err)
	}
	if _, err := selectUSBDFUTarget([]USBDevice{headset, headset}, profile); err == nil {
		t.Fatal("accepted ambiguous target")
	}
	headset.ViaDongle = true
	if _, err := selectUSBDFUTarget([]USBDevice{headset}, profile); err == nil {
		t.Fatal("accepted dongle-routed update")
	}
}

func TestUSBDFUDescriptorSelection(t *testing.T) {
	configuration := []byte{9, 2, 0, 0, 1, 1, 0, 0x80, 50}
	intf := []byte{9, 4, 3, 0, 0, 0xfe, 1, 2, 0}
	functional := []byte{9, 0x21, 1, 0xe8, 3, 0x40, 0, 0, 1}
	good := append(append(append([]byte{}, configuration...), intf...), functional...)
	got, err := parseDFUInterface(good, 1)
	if err != nil || got != testDFUInterfaceWithDetach() {
		t.Fatal(got, err)
	}
	for _, bad := range [][]byte{good[:len(good)-1], append(append([]byte{}, good...), intf...), {0, 2}, {1}} {
		if len(bad) == len(good)+len(intf) {
			bad = append(bad, functional...) // duplicate target
		}
		if _, err := parseDFUInterface(bad, 1); err == nil {
			t.Fatal("accepted malformed/ambiguous descriptors")
		}
	}
	for _, offset := range []int{12, 14, 15, 16, 20, 24, 26} {
		bad := append([]byte{}, good...)
		bad[offset] = 0xff
		if _, err := parseDFUInterface(bad, 1); err == nil {
			t.Fatalf("accepted invalid descriptor at %d", offset)
		}
	}
	if _, err := parseDFUInterface(good, 2); err == nil {
		t.Fatal("selected inactive configuration")
	}
}

func testDFUInterfaceWithDetach() dfuInterface {
	intf := testDFUInterface()
	intf.DetachMS = 1000
	return intf
}

func TestFirmwareInstallationLock(t *testing.T) {
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	lock, err := acquireFirmwareInstallLock()
	if err != nil {
		t.Fatal(err)
	}
	if other, err := acquireFirmwareInstallLock(); err == nil {
		_ = other.Close()
		t.Fatal("two installers acquired the same lock")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := acquireFirmwareInstallLock()
	if err != nil {
		t.Fatal(err)
	}
	_ = next.Close()
}

func TestDeviceOwnersAndFirmwareCannotOverlap(t *testing.T) {
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	owner, err := AcquireDeviceAccess()
	if err != nil {
		t.Fatal(err)
	}
	if installer, err := acquireFirmwareInstallLock(); err == nil {
		_ = installer.Close()
		t.Fatal("installer overlapped running device owner")
	}
	_ = owner.Close()
	installer, err := acquireFirmwareInstallLock()
	if err != nil {
		t.Fatal(err)
	}
	if other, err := AcquireDeviceAccess(); err == nil {
		_ = other.Close()
		t.Fatal("device owner started during firmware transfer")
	}
	_ = installer.Close()
	owner, err = AcquireDeviceAccess()
	if err != nil {
		t.Fatal("device owner could not restart after firmware", err)
	}
	_ = owner.Close()
}

// Optional local check: vendor files remain outside the repository and CI.
func TestOfficialUSBDFUFilesWhenProvided(t *testing.T) {
	dir := os.Getenv("JABRIDGE_TEST_DFU_DIR")
	if dir == "" {
		t.Skip("no local firmware fixture directory")
	}
	for _, name := range []string{"Jabra_SPEAK_410_USB-1-12-0.dfu", "Jabra_SPEAK_510_USB-v2.32.8-vector.zip", "Jabra_Speak_710-v1.40.0-default-vector.zip", "Jabra_SPEAK_810-v1.9.0-default-vector.zip"} {
		t.Run(name, func(t *testing.T) {
			image, err := loadJabraDFUImage(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("%s firmware %s, suffix/CRC/header/profile passed, payload=%d bytes", image.Profile.Name, image.Manifest.Version, len(image.Payload))
		})
	}
}

func FuzzJabraDFUImage(f *testing.F) {
	f.Add(syntheticDFU(0x0421, "1.2.3"))
	f.Add([]byte("PK\x03\x04"))
	f.Fuzz(func(t *testing.T, data []byte) {
		image, err := parseJabraDFUImage(data)
		if err == nil && (image.Manifest == nil || len(image.Payload) < 32 || image.Profile.DFUPID == 0) {
			t.Fatal("parser accepted an incomplete image")
		}
	})
}

func FuzzUSBDFUDescriptors(f *testing.F) {
	f.Add([]byte{9, 2, 0, 0, 1, 1, 0, 0x80, 50, 9, 4, 3, 0, 0, 0xfe, 1, 2, 0, 9, 0x21, 1, 0xe8, 3, 0x40, 0, 0, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		intf, err := parseDFUInterface(data, 1)
		if err == nil && (intf.TransferSize == 0 || intf.TransferSize > 4096 || intf.Alternate != 0) {
			t.Fatal("parser accepted an unusable interface")
		}
	})
}
