package firmware

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirmwareSnapshotSurvivesOriginalReplacementAndCannotBeWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "firmware.zip")
	if err := os.WriteFile(path, []byte("approved bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := freezeFirmwareFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snapshot.Close() }()
	if err := os.WriteFile(path, []byte("different bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshot.path, []byte("tamper"), 0o600); err == nil {
		t.Fatal("sealed snapshot accepted a write")
	}
	data, err := os.ReadFile(snapshot.path)
	if err != nil || string(data) != "approved bytes" {
		t.Fatal(string(data), err)
	}
	digest, err := firmwareArchiveSHA256(snapshot.path)
	if err != nil || digest != snapshot.digest {
		t.Fatal("snapshot digest changed", err)
	}
	if _, err := firmwareFileMD5(snapshot.path); err != nil {
		t.Fatal(err)
	}
}

func TestFirmwareSnapshotRejectsUnsealedDescriptorAndSymlink(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString("input"); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(file.Name(), link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, fmt.Sprintf("/proc/self/fd/%d", file.Fd())} {
		if snapshot, err := freezeFirmwareFile(path); err == nil {
			_ = snapshot.Close()
			t.Fatal("unsealed/symlink input accepted")
		}
	}
	if err := file.Truncate(maxNativeArchive + 1); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := freezeFirmwareFile(file.Name()); err == nil {
		_ = snapshot.Close()
		t.Fatal("oversized snapshot accepted")
	}
}

func TestPreparedInstallDigestCheckedBeforeTargetCallback(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "changed.zip")
	if err := os.WriteFile(path, []byte("replaced archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	defer func() {
		failure, ok := recover().(commandFailure)
		if !ok || !strings.Contains(failure.message, "changed after selection") || called {
			t.Fatalf("selection digest was not enforced first: %#v, called=%t", failure, called)
		}
	}()
	cmdInstallChecked([]string{path, HardwareWriteFlag}, func() error { called = true; return errors.New("stop before any hardware discovery") }, strings.Repeat("0", 64))
}

func testBoundUSB(t *testing.T) USBDevice {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usb")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"busnum": "1", "devnum": "2", "idVendor": "0b0e", "idProduct": "24c7"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	device, err := bindUSBDevice(USBDevice{SysPath: path, VendorID: JabraVendorID, ProductID: 0x24c7})
	if err != nil {
		t.Fatal(err)
	}
	return device
}

func TestUSBDeviceBindingRejectsReplugRatherThanFollowingNewNode(t *testing.T) {
	device := testBoundUSB(t)
	path, err := usbDFUDevicePath(device)
	if err != nil || path != "/dev/bus/usb/001/002" {
		t.Fatal(path, err)
	}
	if err := os.WriteFile(filepath.Join(device.SysPath, "devnum"), []byte("3"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := usbDFUDevicePath(device); err == nil {
		t.Fatal("old selection followed replacement USB address")
	}
	if _, err := bindUSBDevice(device); err == nil {
		t.Fatal("rebinding silently replaced the old attachment")
	}
}

func TestUSBDeviceBindingRejectsSamePIDAndReusedAddressOnNewInstance(t *testing.T) {
	device := testBoundUSB(t)
	if err := os.Rename(device.SysPath, device.SysPath+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(device.SysPath, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"busnum": "1", "devnum": "2", "idVendor": "0b0e", "idProduct": "24c7"} {
		if err := os.WriteFile(filepath.Join(device.SysPath, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateUSBDevice(device); err == nil {
		t.Fatal("replacement with reused IDs inherited old confirmation")
	}
}

func TestUSBDeviceBindingRejectsOtherPortAndModel(t *testing.T) {
	device, other := testBoundUSB(t), testBoundUSB(t)
	changed := device
	changed.SysPath = other.SysPath
	if err := validateUSBDevice(changed); err == nil {
		t.Fatal("another port accepted")
	}
	if err := os.WriteFile(filepath.Join(device.SysPath, "idProduct"), []byte("24c8"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateUSBDevice(device); err == nil {
		t.Fatal("another PID accepted")
	}
}

func TestLocalBoundHIDIdentityWithoutDeviceCommands(t *testing.T) {
	if os.Getenv("JABRIDGE_TEST_BOUND_HID") != "1" {
		t.Skip("opt-in descriptor-only hardware check")
	}
	devices, err := enumerateBoundUSB()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, device := range devices {
		if device.ProductID != 0x24c7 && device.ProductID != 0x24c8 {
			continue
		}
		transport, err := openBoundCSR(device)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("USB %04x:%04x: opened-handle identity and %d-byte output descriptor verified; no GNP command sent", device.VendorID, device.ProductID, transport.reportSize)
		if err := transport.Close(); err != nil {
			t.Fatal(err)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no Link 380 available for descriptor-only check")
	}
}
