package firmware

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func TestRunFirmwareInstallFailsClosed(t *testing.T) {
	err := Run([]string{"install", "/tmp/not-a-firmware-file"})
	if err == nil || !strings.Contains(err.Error(), HardwareWriteFlag) {
		t.Fatalf("firmware install error = %v, want explicit risk gate", err)
	}
}

func TestParseInstallArgsRequiresFileAndExplicitRisk(t *testing.T) {
	path, accepted, err := parseInstallArgs([]string{"firmware.zip", HardwareWriteFlag})
	if err != nil || path != "firmware.zip" || !accepted {
		t.Fatalf("parse install args = %q, %v, %v", path, accepted, err)
	}
	if _, _, err := parseInstallArgs([]string{HardwareWriteFlag}); err == nil {
		t.Fatal("install arguments without a file were accepted")
	}
	if _, accepted, err := parseInstallArgs([]string{"firmware.zip", legacyHardwareWriteFlag}); err != nil || !accepted {
		t.Fatalf("legacy risk flag compatibility = %v, %v", accepted, err)
	}
}

func TestFirmwareActionConfirmationIsExact(t *testing.T) {
	for _, accepted := range []string{"INSTALL\n", "  INSTALL  \n"} {
		if !confirmFirmwareAction(strings.NewReader(accepted), io.Discard, "INSTALL") {
			t.Errorf("confirmation %q was rejected", accepted)
		}
	}
	for _, rejected := range []string{"install\n", "yes\n", "RECOVER\n", ""} {
		if confirmFirmwareAction(strings.NewReader(rejected), io.Discard, "INSTALL") {
			t.Errorf("confirmation %q was accepted", rejected)
		}
	}
	if !confirmFirmwareAction(strings.NewReader("RECOVER\n"), io.Discard, "RECOVER") {
		t.Fatal("exact recovery confirmation was rejected")
	}
}

func TestValidateNativeCSRArchiveAcceptsPartitionedGnV(t *testing.T) {
	path := writeFirmwareArchiveFixture(t, `<buildVector version="1.2.3" productName="Test">
		<targetUsbPids><usbPid>0x1234</usbPid></targetUsbPids>
		<files>
			<file name="app.gnv"><partition>5</partition><crc>0x12345678</crc><language id="0x0409">English</language></file>
			<file name="footer.gnv"><partition>254</partition><crc>0xffffffff</crc><language id="0x0409">English</language></file>
		</files>
	</buildVector>`, map[string][]byte{"app.gnv": {1, 2, 3}, "footer.gnv": {4}})
	if err := validateNativeCSRArchive(path); err != nil {
		t.Fatalf("valid CSR/GNP archive rejected: %v", err)
	}
}

func TestValidateNativeCSRArchiveRejectsDifferentJabraProtocol(t *testing.T) {
	path := writeFirmwareArchiveFixture(t, `<buildVector version="4.1.3" productName="Test">
		<targetUsbPids><usbPid>0x4050</usbPid></targetUsbPids>
		<files><file name="controller.hex"><target>headset</target></file></files>
	</buildVector>`, map[string][]byte{"controller.hex": {1, 2, 3}})
	err := validateNativeCSRArchive(path)
	if err == nil || !strings.Contains(err.Error(), "not a CSR/GNP .gnv partition") {
		t.Fatalf("different firmware protocol error = %v", err)
	}
}

func writeFirmwareArchiveFixture(t *testing.T, manifest string, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "firmware.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	entries := make(map[string][]byte, len(files)+1)
	entries["info.xml"] = []byte(manifest)
	for name, data := range files {
		entries[name] = data
	}
	for name, data := range entries {
		entry, createErr := archive.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := entry.Write(data); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunUnknownCommandReturnsError(t *testing.T) {
	if err := Run([]string{"unknown"}); err == nil {
		t.Fatal("unknown firmware command returned nil")
	}
}

func TestValidateCachedFirmwareArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "firmware.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	entry, err := archive.Create("info.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(`<buildVector version="1.2.3" productName="Test"><targetUsbPids><usbPid>1234</usbPid></targetUsbPids></buildVector>`)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCachedFirmware(path, Release{Version: "1.2.3"}, info.Size()); err != nil {
		t.Fatalf("valid cached archive rejected: %v", err)
	}
	if err := validateCachedFirmware(path, Release{Version: "9.9.9"}, info.Size()); err == nil {
		t.Fatal("wrong cached firmware version was accepted")
	}
	if err := validateCachedFirmware(path, Release{Version: "1.2.3"}, info.Size()+1); err == nil {
		t.Fatal("wrong cached firmware size was accepted")
	}
}

func TestValidateCachedFirmwareRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.bin")
	link := filepath.Join(directory, "firmware.bin")
	if err := os.WriteFile(target, []byte("not firmware"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := validateCachedFirmware(link, Release{}, 0); err == nil {
		t.Fatal("firmware symlink was accepted")
	}
}

func TestFirmwareFileMD5UsesPublishedBase64Form(t *testing.T) {
	path := filepath.Join(t.TempDir(), "firmware.bin")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := firmwareFileMD5(path)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "XUFAKrxLKna5cZ2REBfFkg==" {
		t.Fatalf("MD5 = %q", digest)
	}
	if _, err := decodeOfficialMD5(digest); err != nil {
		t.Fatalf("published checksum rejected: %v", err)
	}
	if _, err := decodeOfficialMD5("AQIDBA=="); err == nil {
		t.Fatal("short published checksum was accepted")
	}
}

func TestValidateCachedFirmwareChecksOfficialDigest(t *testing.T) {
	path := writeFirmwareArchiveFixture(t,
		`<buildVector version="1.2.3" productName="Test"><targetUsbPids><usbPid>1234</usbPid></targetUsbPids></buildVector>`, nil)
	digest, err := firmwareFileMD5(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	release := Release{Version: "1.2.3", OfficialMD5: digest}
	if err := validateCachedFirmware(path, release, info.Size()); err != nil {
		t.Fatalf("valid checksum rejected: %v", err)
	}
	release.OfficialMD5 = "6ZoYxCjLONXyYIU2eJIuAw=="
	if err := validateCachedFirmware(path, release, info.Size()); err == nil {
		t.Fatal("wrong official checksum was accepted")
	}
}

// TestCompareVersions guards the numeric-semver comparison that sorts
// Jabra firmware releases newest-first. String-sort would put "1.10.0"
// *before* "1.2.0" and make `download` pick a stale release — this is
// the exact bug flagged in the compareVersions doc comment.
func TestCompareVersions(t *testing.T) {
	sign := func(n int) int {
		switch {
		case n > 0:
			return 1
		case n < 0:
			return -1
		}
		return 0
	}
	cases := []struct {
		a, b string
		want int
	}{
		{"1.10.0", "1.2.0", 1}, // numeric, not lexical
		{"1.2.0", "1.10.0", -1},
		{"1.0.0", "1.0.0", 0},
		{"2.0.0", "1.99.9", 1},
		{"1.14.0", "1.13.9", 1}, // BIZ 2400 II real release pair
		{"1.2", "1.2.0", -1},    // shorter version is "less"
	}
	for _, c := range cases {
		got := compareVersions(c.a, c.b)
		if sign(got) != sign(c.want) {
			t.Errorf("compareVersions(%q, %q) = %d, want sign %d", c.a, c.b, got, c.want)
		}
	}
}

// TestRouteFor pins the format-to-backend dispatch table. This is what
// makes `jabridge firmware dev flash` work across every Jabra firmware format without
// having to teach each call site the routing rules.
func TestRouteFor(t *testing.T) {
	cases := []struct {
		format FirmwareFormat
		want   flashRoute
	}{
		{FormatUSBDFU11, routeDfuUtil},
		{FormatCSRDFU2, routeJfwu},
		{FormatGnVArchive, routeJfwu},
		{FormatDfuSeSTM, routeJfwu},
		{FormatPlainZIP, routeJfwu},
		{FormatUnknown, routeNone},
	}
	for _, c := range cases {
		if got := routeFor(c.format); got != c.want {
			t.Errorf("routeFor(%s) = %s, want %s", c.format.String(), got.String(), c.want.String())
		}
	}
}

// TestFlashByFormatUnknown verifies that an unidentified firmware file
// produces an error rather than silently falling through to a backend.
// The "no flasher available" message is what the user actually sees.
func TestFlashByFormatUnknown(t *testing.T) {
	t.Setenv(HardwareWriteEnv, HardwareWriteAck)
	err := flashByFormat("/nonexistent/file", FormatUnknown)
	if err == nil {
		t.Fatal("flashByFormat(FormatUnknown) returned nil, want error")
	}
	want := "no flasher available"
	if got := err.Error(); len(got) < len(want) || got[:len(want)] != want {
		t.Errorf("flashByFormat error = %q, want prefix %q", got, want)
	}
}

func TestHardwareWritesFailClosed(t *testing.T) {
	t.Setenv(HardwareWriteEnv, "")
	if err := requireHardwareWrites(); err == nil {
		t.Fatal("hardware writes unexpectedly enabled")
	}
}

func TestHardwareWritesRequireExactAcknowledgement(t *testing.T) {
	t.Setenv(HardwareWriteEnv, "yes")
	if err := requireHardwareWrites(); err == nil {
		t.Fatal("partial acknowledgement unexpectedly enabled hardware writes")
	}
	t.Setenv(HardwareWriteEnv, HardwareWriteAck)
	if err := requireHardwareWrites(); err != nil {
		t.Fatalf("exact acknowledgement rejected: %v", err)
	}
}

func TestParseTargetPIDs(t *testing.T) {
	got, err := parseTargetPIDs([]string{"0x24C7", "24c7", "0x24C8"})
	if err != nil {
		t.Fatalf("parseTargetPIDs: %v", err)
	}
	if len(got) != 2 || got[0] != 0x24c7 || got[1] != 0x24c8 {
		t.Fatalf("parseTargetPIDs = %#v, want [0x24c7 0x24c8]", got)
	}
	if _, err := parseTargetPIDs(nil); err == nil {
		t.Fatal("empty target PID list accepted")
	}
}

func TestFirmwareTargetsAttachedDevice(t *testing.T) {
	targets := []uint16{0x24c7}
	if !firmwareTargetsAttachedDevice(targets, []USBDevice{{VendorID: JabraVendorID, ProductID: 0x24c7}}) {
		t.Fatal("matching Link 380 target was rejected")
	}
	if firmwareTargetsAttachedDevice(targets, []USBDevice{{VendorID: JabraVendorID, ProductID: 0x24b9}}) {
		t.Fatal("Evolve2 85 was accepted for Link 380 firmware")
	}
	if firmwareTargetsAttachedDevice(targets, []USBDevice{{VendorID: 0x1234, ProductID: 0x24c7}}) {
		t.Fatal("non-Jabra device was accepted by PID alone")
	}
}

func TestFirmwareReleaseMatchesOfficialSiblingPID(t *testing.T) {
	evidence := &modelcatalog.ReleaseEvidence{
		MD5Checksum:    "published",
		CompatiblePIDs: []uint16{0x24c7, 0x24c8},
	}
	if !firmwareReleaseMatchesDevice("published", 0x24c8, evidence) {
		t.Fatal("official sibling PID was rejected")
	}
	if firmwareReleaseMatchesDevice("different", 0x24c8, evidence) {
		t.Fatal("wrong firmware bytes were accepted")
	}
	if firmwareReleaseMatchesDevice("published", 0x24b9, evidence) {
		t.Fatal("unrelated PID was accepted")
	}
}

func TestDisplaySerialIsRedactedByDefault(t *testing.T) {
	t.Setenv("JABRIDGE_FIRMWARE_SHOW_SERIAL", "")
	if got := displaySerial("A1B2C3D4E5F6"); got != "<redacted>" {
		t.Fatalf("displaySerial = %q, want redacted", got)
	}
	t.Setenv("JABRIDGE_FIRMWARE_SHOW_SERIAL", "1")
	if got := displaySerial("A1B2C3D4E5F6"); got != "A1B2C3D4E5F6" {
		t.Fatalf("displaySerial opt-in = %q", got)
	}
}
