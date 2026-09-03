package firmware

import (
	"strings"
	"testing"
)

func TestRunFirmwareInstallFailsClosed(t *testing.T) {
	err := Run([]string{"install", "/tmp/not-a-firmware-file"})
	if err == nil || !strings.Contains(err.Error(), "installation is locked") {
		t.Fatalf("firmware install error = %v, want disabled gate", err)
	}
}

func TestRunUnknownCommandReturnsError(t *testing.T) {
	if err := Run([]string{"unknown"}); err == nil {
		t.Fatal("unknown firmware command returned nil")
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
