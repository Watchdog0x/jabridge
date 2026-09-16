package firmware

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func conexantTestReports(id byte) []HIDReport {
	reports, err := parseHIDReports([]byte{
		0x05, 0x0c, 0x09, 1, 0xa1, 1, 0x85, id, 0x15, 0, 0x26, 0xff, 0,
		0x75, 8, 0x95, 2, 0x81, 1, // Constant prefix before the input value array.
		0x09, 0, 0x95, 16, 0x81, 2,
		0x95, 1, 0x91, 1, // Constant prefix before the output value array.
		0x09, 0, 0x95, 20, 0x91, 2, 0xc0,
	})
	if err != nil {
		panic(err)
	}
	return reports
}

func TestConexantHIDFieldSelection(t *testing.T) {
	for _, id := range []byte{4, 8} {
		layout, err := selectConexantLayout(conexantTestReports(id))
		if err != nil || layout.Plus != (id == 4) || layout.Input.Offset != 3 || layout.Input.Size != 16 || layout.Output.Offset != 2 || layout.Output.Size != 20 {
			t.Fatalf("wrong field layout: %+v %v", layout, err)
		}
	}
	combined := append(conexantTestReports(8), conexantTestReports(4)...)
	layout, err := selectConexantLayout(combined)
	if err != nil || !layout.Plus {
		t.Fatal("did not prefer the advertised Plus interface", err)
	}
	for _, change := range []func([]HIDReport){
		func(r []HIDReport) {
			for i := range r {
				r[i].ID = 5
			}
		},
		func(r []HIDReport) { r[1].Fields[1].UsagePage = 0xff00 },
		func(r []HIDReport) { r[1].Fields[1].collectionPage = 0x0b },
		func(r []HIDReport) { r[1].Fields[1].Usages = []uint32{1} },
		func(r []HIDReport) { r[1].Fields[0].Flags = 0 },
		func(r []HIDReport) { r[0].Fields[1].OffsetBits = 1 },
	} {
		reports := conexantTestReports(4)
		change(reports)
		if _, err := selectConexantLayout(reports); err == nil {
			t.Fatal("accepted a wrong or overlapping firmware field")
		}
	}
}

func TestConexantHardwareWritesRequireAuthorization(t *testing.T) {
	previous := commandLineRiskAccepted.Swap(false)
	defer commandLineRiskAccepted.Store(previous)
	for _, plus := range []bool{false, true} {
		h := &conexantHID{layout: conexantHIDLayout{Plus: plus, Output: conexantHIDField{Size: 20}}}
		packet := []byte{0xa0, 0x14, 0, 0, 0}
		if plus {
			packet = []byte{0x60, 1, 0, 0x14, 0}
		}
		if err := h.Write(context.Background(), packet); err == nil || !strings.Contains(err.Error(), "explicit") {
			t.Fatal("write reached an unbound device before authorization", err)
		}
	}
}

func TestConexantRecoveryBindingAndRoundTrip(t *testing.T) {
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	device := interactiveDFUTestDevice(t, 0x0342)
	for name, value := range map[string]string{"idVendor": "0b0e", "idProduct": "0342"} {
		if err := os.WriteFile(filepath.Join(device.SysPath, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	device, err := bindUSBDevice(device)
	if err != nil {
		t.Fatal(err)
	}
	device.Serial = "test-device"
	state := firmwareRecoveryState{FormatVersion: 1, ArchiveSHA256: strings.Repeat("a", 64), ProductName: "UC Voice test", FirmwareVersion: "1.26.0", TargetUSBPIDs: []string{"0x0342"}, Attempt: 1}
	part := conexantRecovery{Plus: true, DescriptorSHA256: strings.Repeat("b", 64)}
	if err := bindConexantRecovery(&state, device, part); err != nil {
		t.Fatal(err)
	}
	if err := saveFirmwareRecoveryState(state); err != nil {
		t.Fatal(err)
	}
	saved, err := loadFirmwareRecoveryState()
	if err != nil {
		t.Fatal(err)
	}
	if err := bindConexantRecovery(&saved, device, part); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*USBDevice, *conexantRecovery){
		func(d *USBDevice, _ *conexantRecovery) { d.SysPath += "-other" },
		func(d *USBDevice, _ *conexantRecovery) { d.ProductID++ },
		func(d *USBDevice, _ *conexantRecovery) { d.Serial = "replacement" },
		func(_ *USBDevice, p *conexantRecovery) { p.Plus = false },
		func(_ *USBDevice, p *conexantRecovery) { p.DescriptorSHA256 = strings.Repeat("c", 64) },
	} {
		d, p := device, part
		change(&d, &p)
		if err := bindConexantRecovery(&saved, d, p); err == nil {
			t.Fatal("recovery followed a different device or layout")
		}
	}
	state.Conexant.Calibration = conexantCalibration{Start: 0xfffe, Size: 3, SHA256: strings.Repeat("d", 64)}
	if err := saveFirmwareRecoveryState(state); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFirmwareRecoveryState(); err == nil {
		t.Fatal("recovery accepted out-of-range calibration")
	}
}

func TestConexantPreflightRejectsWrongImageAndDevice(t *testing.T) {
	manifest := `<buildVector version="1.26.0" productName="UC Voice"><targetUsbPids><usbPid>0x0342</usbPid></targetUsbPids><files><file name="patch.ptc"><content>firmware</content><version>1.26.0</version><target>headset</target></file></files></buildVector>`
	patch := []byte(conexantTestS3(0x14, 0) + conexantTestS3(0x14, 0x50) + "S70500000000FA\n")
	path := writeFirmwareArchiveFixture(t, manifest, map[string][]byte{"patch.ptc": patch})
	if err := ValidateInstallInput([]string{path}); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []uint16{0x0343, 0x4052} {
		device := interactiveDFUTestDevice(t, pid)
		if _, err := interactiveInstallBindingForDevices(path, pid, []USBDevice{device}); err == nil {
			t.Fatal("archive routed to another model")
		}
	}
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInstallInput([]string{path}); err == nil {
		t.Fatal("corrupt firmware passed preflight")
	}
}
