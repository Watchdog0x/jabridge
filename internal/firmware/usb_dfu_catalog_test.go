package firmware

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func interactiveDFUTestDevice(t *testing.T, pid uint16) USBDevice {
	t.Helper()
	port := t.TempDir()
	for name, value := range map[string]string{"busnum": "1", "devnum": "2"} {
		if err := os.WriteFile(filepath.Join(port, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return USBDevice{VendorID: JabraVendorID, ProductID: pid, SysPath: port}
}

func TestInteractiveFirmwareAcceptsRawDFUWithoutZIPManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "firmware.dfu")
	if err := os.WriteFile(path, syntheticDFU(0x0421, "1.2.3"), 0600); err != nil {
		t.Fatal(err)
	}
	device := interactiveDFUTestDevice(t, 0x0422)
	if _, err := interactiveInstallBindingForDevices(path, device.ProductID, []USBDevice{device}); err != nil {
		t.Fatal("the TUI rejected a valid raw DFU image", err)
	}
	device.ProductID = 0x0412
	if _, err := interactiveInstallBindingForDevices(path, device.ProductID, []USBDevice{device}); err == nil {
		t.Fatal("raw DFU image accepted for a different model")
	}
}

func TestUSBDFUCatalogBootloaderCounterparts(t *testing.T) {
	// Official catalog runtime IDs, independently paired with the suffix of
	// each runtime variant's published firmware. Names alone are insufficient.
	pairs := map[uint16][]uint16{
		0x0411: {0x0410, 0x0412}, 0x0421: {0x0420, 0x0422},
		0x090f: {0x0910}, 0x091b: {0x090a, 0x091c}, 0x0948: {0x0949},
		0x094e: {0x030b, 0x030c, 0x0311}, 0x0954: {0x030d, 0x030e, 0x0310},
		0x096a: {0x2450, 0x2451}, 0x096c: {0x2452, 0x2453},
		0x0971: {0x2454, 0x2456}, 0x0976: {0x245d, 0x245e},
		0x097a: {0x2465, 0x2466, 0x2467}, 0x097e: {0x246c, 0x246d, 0x246e},
		0x0982: {0x2475, 0x2476, 0x2477}, 0x0994: {0x24ae},
		0x0995: {0x24b0, 0x24b2}, 0x0997: {0x24b1, 0x24cb}, 0x0998: {0x24e8},
		0x2402: {0x2400, 0x2401}, 0xa347: {0xa345, 0xa346},
	}
	for boot, pids := range pairs {
		for _, pid := range pids {
			profile, ok := usbDFUProfileForPID(pid)
			if !ok || profile.DFUPID != boot || !profile.runtime(pid) || !NativeFirmwareProtocolSupported(pid, 1) {
				t.Errorf("runtime %04x needs bootloader %04x; got %+v", pid, boot, profile)
			}
		}
	}
	for _, pid := range []uint16{0x030f, 0x094f, 0x24c7, 0x0e41, 0x4052} {
		if _, ok := usbDFUProfileForPID(pid); ok {
			t.Errorf("unverified DFU model accepted: %04x", pid)
		}
	}
}

func TestUSBDFUEntryUsesValidatedManagementReportLayout(t *testing.T) {
	for _, id := range []byte{2, 5, 7} {
		for _, size := range []int{33, 63, 64, 65} {
			layout, err := SelectControlLayout(managementReports(id, size))
			if err != nil {
				t.Fatal(err)
			}
			packet, err := usbDFUModePacket(layout, 8)
			if err != nil || len(packet) != size || !bytes.Equal(packet[:6], []byte{id, 8, 0, 1, 0x85, 7}) {
				t.Fatal("DFU mode request ignored the live management descriptor", packet, err)
			}
		}
	}
	wrong := managementReports(2, 33)
	wrong[0].Fields[0].UsagePage = 0xff20
	if _, err := SelectControlLayout(wrong); err == nil {
		t.Fatal("non-management interface accepted")
	}
}

func TestLocalUSBDFUCatalogArchivesAndTransfers(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_DFU_CATALOG_AUDIT")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_DFU_CATALOG_AUDIT for original-archive checks")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Status, Path, Version, MD5 string
		Devices                    []struct{ VID, PID string }
	}
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) == 0 {
		t.Fatal("missing catalog evidence", err)
	}
	transferred := map[string]bool{}
	for _, row := range rows {
		if row.Status != "archive inspected" || len(row.Devices) != 1 {
			t.Fatal("incomplete catalog evidence", row.Status)
		}
		t.Run(row.Devices[0].PID, func(t *testing.T) {
			pid, err := strconv.ParseUint(row.Devices[0].PID, 16, 16)
			if err != nil || row.Devices[0].VID != "0b0e" {
				t.Fatal("invalid evidence identity", err)
			}
			image, err := loadJabraDFUImage(row.Path)
			if err != nil || image.MD5 != row.MD5 || image.Manifest.Version != row.Version || !image.Profile.runtime(uint16(pid)) {
				t.Fatal("original archive does not match its runtime profile", err)
			}
			if os.Getenv("JABRIDGE_TEST_LIVE_RELEASES") == "1" {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := verifyUSBDFURelease(ctx, image, USBDevice{VendorID: JabraVendorID, ProductID: uint16(pid)})
				cancel()
				if err != nil {
					t.Fatal("official release protocol or checksum", err)
				}
			}
			if err := ValidateInstallInput([]string{row.Path}); err != nil {
				t.Fatal("CLI preflight", err)
			}
			device := interactiveDFUTestDevice(t, uint16(pid))
			if _, err := interactiveInstallBindingForDevices(row.Path, uint16(pid), []USBDevice{device}); err != nil {
				t.Fatal("TUI preflight", err)
			}
			if !transferred[row.MD5] {
				peer := &dfuTestTransport{}
				intf := testDFUInterface()
				intf.TransferSize = 1024 // Independent simulated USB descriptor.
				if err := transferDFU(context.Background(), peer, intf, image.Payload, nil); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(bytes.Join(peer.writes, nil), image.Payload) || len(peer.writes[len(peer.writes)-1]) != 0 {
					t.Fatal("original image transfer changed bytes or omitted finalization")
				}
				transferred[row.MD5] = true
				t.Logf("bootloader %04x, original version %s, %d simulated writes", image.Profile.DFUPID, row.Version, len(peer.writes))
			}
		})
	}
	t.Logf("%d runtime variants checked; %d original archives transferred; no hardware", len(rows), len(transferred))
}
