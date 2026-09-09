package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func emptyDiagnosticFS() fstest.MapFS {
	return fstest.MapFS{
		"sys/bus/usb/devices": {Mode: fs.ModeDir | 0o755},
		"sys/class/hidraw":    {Mode: fs.ModeDir | 0o755},
		"sys/class/input":     {Mode: fs.ModeDir | 0o755},
	}
}

func connectionReport(inventory diagnosticInventory) string {
	var out bytes.Buffer
	writeConnectionDiagnostic(&out, inventory)
	return out.String()
}

func TestDebugControllerAbsentChecksConnectionNotPermissions(t *testing.T) {
	// The essential evidence from the RC24 controller-only report: all three
	// inventories empty, healthy service, bundled rule not installed.
	report := connectionReport(readDiagnosticInventory(emptyDiagnosticFS()))
	report += "Current device access rule installed: false\nIPC: ready\nActiveState=active\n"
	steps := strings.Join(reportNextSteps(report), "\n")
	for _, want := range []string{"USB plug, cable and port", "lsusb -d 0b0e:", "controller alone, headset alone and both connected"} {
		if !strings.Contains(steps, want) {
			t.Fatalf("missing %q: %s", want, steps)
		}
	}
	if !strings.Contains(report, connectionNone) || strings.Contains(steps, "run jabridge setup") || strings.Contains(steps, "scan may still be starting") {
		t.Fatal(report, steps)
	}
}

func TestDebugInventoryRecognizesAnyJabraProductWithoutPrivateFields(t *testing.T) {
	root := emptyDiagnosticFS()
	for name, data := range map[string]string{
		"sys/bus/usb/devices/1-2/idVendor":            "0B0E\n",
		"sys/bus/usb/devices/1-2/idProduct":           "f123\n",
		"sys/bus/usb/devices/1-2/serial":              "PRIVATE_SERIAL",
		"sys/bus/usb/devices/1-2/product":             "PRIVATE_NAME",
		"sys/bus/usb/devices/1-2:1.0/bInterfaceClass": "03",
		"sys/bus/usb/devices/1-3/idVendor":            "1234",
		"sys/class/hidraw/hidraw8/device/uevent":      "HID_ID=0003:00000B0E:0000F123\nHID_UNIQ=PRIVATE_SERIAL\nHID_NAME=PRIVATE_NAME",
		"sys/class/input/event14/device/id/vendor":    "0b0e\n",
		"sys/class/input/event15/device/id/vendor":    "1234\n",
	} {
		root[name] = &fstest.MapFile{Data: []byte(data)}
	}
	got := readDiagnosticInventory(root)
	if got.USBError != nil || got.HIDError != nil || got.InputError != nil || len(got.USBPIDs) != 1 || got.USBPIDs[0] != 0xf123 || len(got.HID) != 1 || len(got.Input) != 1 {
		t.Fatalf("inventory: %+v", got)
	}
	report := connectionReport(got) + got.HID[0].label()
	if strings.Contains(report, "PRIVATE") || strings.Contains(report, connectionNone) || strings.Contains(report, "access ready") {
		t.Fatal(report)
	}
}

type deniedInventoryFS struct {
	fs.FS
	denied string
}

func (root deniedInventoryFS) Open(name string) (fs.File, error) {
	if name == root.denied {
		return nil, &fs.PathError{Op: "open", Path: "PRIVATE_PATH", Err: fs.ErrPermission}
	}
	return root.FS.Open(name)
}

func TestDebugFailedScansAreNotEmptyDeviceProof(t *testing.T) {
	for _, dir := range []string{"sys/bus/usb/devices", "sys/class/hidraw", "sys/class/input"} {
		t.Run(dir, func(t *testing.T) {
			root := deniedInventoryFS{FS: emptyDiagnosticFS(), denied: dir}
			report := connectionReport(readDiagnosticInventory(root))
			steps := strings.Join(reportNextSteps(report), "\n")
			if !strings.Contains(report, connectionIncomplete) || !strings.Contains(report, "permission denied") || !strings.Contains(steps, "sysfs visibility") {
				t.Fatal(report, steps)
			}
			if strings.Contains(report, connectionNone) || strings.Contains(report+steps, "run jabridge setup") || strings.Contains(report, "PRIVATE") {
				t.Fatal(report, steps)
			}
		})
	}
	report := connectionReport(readDiagnosticInventory(fstest.MapFS{}))
	if !strings.Contains(report, connectionIncomplete) || strings.Contains(report, connectionNone) {
		t.Fatal(report)
	}
}

func TestDebugDisappearingSysfsEntryMakesScanIncomplete(t *testing.T) {
	root := emptyDiagnosticFS()
	root["sys/bus/usb/devices/1-2"] = &fstest.MapFile{Mode: fs.ModeDir | 0o755}
	got := readDiagnosticInventory(root)
	if !errors.Is(got.USBError, fs.ErrNotExist) || !strings.Contains(connectionReport(got), connectionIncomplete) {
		t.Fatalf("inventory: %+v", got)
	}
}

func TestDebugInvalidPIDDoesNotBecomeAnotherProduct(t *testing.T) {
	for _, pid := range []string{"10000", "PRIVATE", ""} {
		root := emptyDiagnosticFS()
		root["sys/bus/usb/devices/1-2/idVendor"] = &fstest.MapFile{Data: []byte("0b0e")}
		root["sys/bus/usb/devices/1-2/idProduct"] = &fstest.MapFile{Data: []byte(pid)}
		got := readDiagnosticInventory(root)
		if len(got.USBPIDs) != 1 || got.USBPIDs[0] != 0 || got.USBError == nil || strings.Contains(connectionReport(got), "PRIVATE") {
			t.Fatal(connectionReport(got))
		}
	}
}

func TestDebugConnectionStatesDoNotConflateMissingHIDButtonsOrBluetooth(t *testing.T) {
	for _, test := range []struct {
		name      string
		inventory diagnosticInventory
		want      string
	}{
		{"USB without HID", diagnosticInventory{USBPIDs: []uint16{0x4052}}, connectionNoHID},
		{"headset without Linux buttons", diagnosticInventory{USBPIDs: []uint16{0x4056}, HID: []diagnosticHIDNode{{PID: 0x4056, Bus: 3}}}, "device entries found"},
		{"Bluetooth HID only", diagnosticInventory{HID: []diagnosticHIDNode{{PID: 0x0422, Bus: 5}}}, "device entries found"},
		{"input only", diagnosticInventory{Input: []string{"/dev/input/event15"}}, "device entries found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := connectionReport(test.inventory)
			steps := strings.Join(reportNextSteps(report+"Current device access rule installed: false\n"), "\n")
			if !strings.Contains(report, test.want) || strings.Contains(report, connectionNone) || strings.Contains(steps, "run jabridge setup") {
				t.Fatal(report, steps)
			}
		})
	}
}

func TestDebugActualAccessDenialsStillGetScopedAdvice(t *testing.T) {
	for _, test := range []struct{ line, want string }{
		{"hidraw7: permission denied", "Device control access is denied"},
		{"Jabra input event14: permission denied", "Button/call-event access is denied"},
		{"USB firmware node 001/002: permission denied", "USB firmware access is denied"},
	} {
		report := connectionReport(diagnosticInventory{USBPIDs: []uint16{0x4052}, HID: []diagnosticHIDNode{{PID: 0x4052, Bus: 3}}}) + test.line
		steps := strings.Join(reportNextSteps(report), "\n")
		if !strings.Contains(steps, test.want) || !strings.Contains(steps, "run jabridge setup") {
			t.Fatal(steps)
		}
	}
}

func TestLiveReadOnlyDebugConnectionInventory(t *testing.T) {
	if os.Getenv("JABRIDGE_DIAGNOSTIC_LIVE_TEST") != "1" {
		t.Skip("opt-in read-only host inventory check")
	}
	inventory := readDiagnosticInventory(os.DirFS("/"))
	if inventory.USBError != nil || inventory.HIDError != nil || inventory.InputError != nil {
		t.Fatalf("scan errors: USB=%v HID=%v input=%v", inventory.USBError, inventory.HIDError, inventory.InputError)
	}
	devices, err := enumerateJabraUSB()
	if err != nil || len(inventory.USBPIDs) != len(devices) || len(inventory.HID) != len(diagnosticHIDNodes()) || len(inventory.Input) != len(jabraInputPaths()) {
		t.Fatal("independent read-only inventories disagree; check for a reconnect during the test")
	}
	t.Log(connectionReport(inventory))
}
