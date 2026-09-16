package firmware

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestUSBDFUEvolve75eUsesExactProfile(t *testing.T) {
	for _, pid := range []uint16{0x246c, 0x246d, 0x246e, 0x097e} {
		profile, ok := usbDFUProfileForPID(pid)
		if !ok || profile.DFUPID != 0x097e || !NativeFirmwareProtocolSupported(pid, 1) {
			t.Fatal("wrong Evolve 75e profile", pid, profile)
		}
	}
	for _, pid := range []uint16{0x246b, 0x246f, 0x24a3, 0x24c7} {
		if profile, ok := usbDFUProfileForPID(pid); ok && profile.Name == "Jabra Evolve 75e" {
			t.Fatal("guessed sibling accepted", pid)
		}
	}
	profile, _ := usbDFUProfileForPID(0x246c)
	if _, err := selectUSBDFUTarget([]USBDevice{{VendorID: JabraVendorID, ProductID: 0x246c, SysPath: "test", ViaDongle: true}}, profile); err == nil {
		t.Fatal("wireless child accepted for direct USB DFU")
	}
}

func TestLocalEvolve75eUSBDFUArchive(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_E75E_FILE")
	checkLocalUSBDFUArchive(t, path, 0x097e, "2.31.0")
}

func TestLocalSpeak510USBDFUArchive(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_SPEAK510_FILE")
	checkLocalUSBDFUArchive(t, path, 0x0421, "2.32.8")
}

func checkLocalUSBDFUArchive(t *testing.T, path string, dfuPID uint16, version string) {
	t.Helper()
	if path == "" {
		t.Skip("local read-only firmware archive not provided")
	}
	image, err := loadJabraDFUImage(path)
	if err != nil {
		t.Fatal(err)
	}
	if image.Profile.DFUPID != dfuPID || image.Manifest.Version != version {
		t.Fatal("wrong reference release")
	}
	t.Logf("%s %s: suffix, header, CRC and bootloader target passed; payload=%d bytes", image.Profile.Name, version, len(image.Payload))
	if os.Getenv("JABRIDGE_TEST_OFFICIAL_CATALOG") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, pid := range image.Profile.RuntimePIDs {
			if err := verifyUSBDFURelease(ctx, image, USBDevice{VendorID: JabraVendorID, ProductID: pid}); err != nil {
				t.Fatal(err)
			}
			t.Logf("official protocol-1 release bytes match runtime PID %04x", pid)
		}
	}
}
