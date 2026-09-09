package firmware

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestInteractiveTargetIsUniqueDirectUSBAndNeverAnotherPID(t *testing.T) {
	selected := USBDevice{VendorID: JabraVendorID, ProductID: 0x24c7, SysPath: "port1"}
	other := USBDevice{VendorID: JabraVendorID, ProductID: 0x0422, SysPath: "port2"}
	if got, err := selectInteractiveUSBTarget([]USBDevice{other, selected}, 0x24c7); err != nil || got.SysPath != "port1" {
		t.Fatal(got, err)
	}
	child := selected
	child.ViaDongle = true
	for _, devices := range [][]USBDevice{nil, {other}, {child}, {selected, selected}} {
		if _, err := selectInteractiveUSBTarget(devices, 0x24c7); err == nil {
			t.Fatal("ambiguous or absent target accepted", devices)
		}
	}
}

func TestInteractiveBindingChangesOnReplugArchiveOrTarget(t *testing.T) {
	manifest := func(app []byte) string {
		return fmt.Sprintf(`<buildVector version="1.2.3" productName="Test"><targetUsbPids><usbPid>0x1234</usbPid></targetUsbPids><files><file name="app.gnv"><partition>5</partition><crc>0x%08x</crc><language id="0x0409">English</language></file><file name="footer.gnv"><partition>254</partition><crc>0x%08x</crc><language id="0x0409">English</language></file></files></buildVector>`, referenceStageCRC(app), referenceStageCRC([]byte{4, 5, 6}))
	}
	path := writeFirmwareArchiveFixture(t, manifest([]byte{1, 2, 3}), map[string][]byte{"app.gnv": {1, 2, 3}, "footer.gnv": {4, 5, 6}})
	port := t.TempDir()
	for name, value := range map[string]string{"busnum": "1", "devnum": "2"} {
		if err := os.WriteFile(filepath.Join(port, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	devices := []USBDevice{{VendorID: JabraVendorID, ProductID: 0x1234, SysPath: port}}
	before, err := interactiveInstallBindingForDevices(path, 0x1234, devices)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(port, "devnum"), []byte("3"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := interactiveInstallBindingForDevices(path, 0x1234, devices)
	if err != nil || after == before {
		t.Fatal("replug not detected", err)
	}
	other := writeFirmwareArchiveFixture(t, manifest([]byte{9, 8, 7}), map[string][]byte{"app.gnv": {9, 8, 7}, "footer.gnv": {4, 5, 6}})
	changed, err := interactiveInstallBindingForDevices(other, 0x1234, devices)
	if err != nil || changed == after {
		t.Fatal("archive change not detected", err)
	}
	devices[0].ProductID = 0x5678
	if _, err := interactiveInstallBindingForDevices(path, 0x5678, devices); err == nil {
		t.Fatal("wrong selected PID accepted")
	}
	if err := (&PreparedInstall{}).validate(); err == nil {
		t.Fatal("empty plan accepted")
	}
}
