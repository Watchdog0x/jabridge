package firmware

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func syntheticEvolve2Archive(t *testing.T, bootPID uint16) string {
	t.Helper()
	manifest := fmt.Sprintf(`<buildVector version="2.11.1" productName="Jabra_Evolve2_40"><targetUsbPids><usbPid>0x%04X</usbPid></targetUsbPids><files>
<file name="headset.hex"><version>2.11.1</version><sitelHidTargetId>03</sitelHidTargetId><gnpAddress>1</gnpAddress><updateOrder>1</updateOrder></file>
<file name="tunes.hex"><version>2.11.1</version><sitelHidTargetId>27</sitelHidTargetId><gnpAddress>1</gnpAddress><updateOrder>20</updateOrder></file>
</files></buildVector>`, bootPID)
	image := []byte(hexRecord(0, 0, 1, 2, 3, 4) + hexRecord(0, 1))
	return writeFirmwareArchiveFixture(t, manifest, map[string][]byte{"headset.hex": image, "tunes.hex": image})
}

func TestSitelUnsupportedModelDoesNotSelectEngageOrCSR(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			path := syntheticEvolve2Archive(t, 0x4060)
			if legacy {
				manifest := `<buildVector version="3.8.0" productName="Jabra_Evolve_20"><targetUsbPids><usbPid>0x0304</usbPid></targetUsbPids><files><file name="headset.hex"><content>firmware</content></file></files></buildVector>`
				path = writeFirmwareArchiveFixture(t, manifest, map[string][]byte{"headset.hex": []byte(hexRecord(0, 0, 1, 2) + hexRecord(0, 1))})
			}
			manifest, err := parseFirmwareManifest(path)
			if err != nil || !isSitelManifest(manifest) {
				t.Fatal("Sitel archive fell through to CSR", err)
			}
			err = ValidateInstallInput([]string{path})
			if err == nil || !strings.Contains(err.Error(), "not implemented for "+manifest.ProductName) || strings.Contains(err.Error(), "select an Engage") {
				t.Fatal("missing model-specific unsupported message", err)
			}
		})
	}
}

// Issue #43: the archive names the bootloader, while the selected device is
// the UC runtime PID. Both CLI preflight and TUI preparation must accept it.
func TestEvolve2FirmwarePreflightAndInteractiveSelection(t *testing.T) {
	path := syntheticEvolve2Archive(t, 0x0e44)
	if err := ValidateInstallInput([]string{path}); err != nil {
		t.Fatal(err)
	}
	port := t.TempDir()
	for name, value := range map[string]string{"busnum": "1", "devnum": "2"} {
		if err := os.WriteFile(filepath.Join(port, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	devices := []USBDevice{{VendorID: JabraVendorID, ProductID: 0x0e41, SysPath: port}}
	if _, err := interactiveInstallBindingForDevices(path, 0x0e41, devices); err != nil {
		t.Fatal(err)
	}
	devices[0].ProductID = 0x4052
	if _, err := interactiveInstallBindingForDevices(path, 0x4052, devices); err == nil {
		t.Fatal("Evolve2 firmware accepted for Engage")
	}
}
