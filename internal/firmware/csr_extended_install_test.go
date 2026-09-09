package firmware

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func TestCSRTargetSelectionHonorsUIAndRejectsAmbiguity(t *testing.T) {
	devices := []USBDevice{{VendorID: JabraVendorID, ProductID: 0x24c7}, {VendorID: JabraVendorID, ProductID: 0x253d}, {VendorID: JabraVendorID, ProductID: 0x253f}}
	validate := func(device USBDevice) (int, error) {
		if device.ProductID == 0x24c7 {
			return 0, errors.New("different firmware")
		}
		return 16, nil
	}
	if _, _, err := selectCSRInstallTarget(devices, 0, validate); err == nil {
		t.Fatal("ambiguous targets accepted")
	}
	device, protocol, err := selectCSRInstallTarget(devices, 0x253f, validate)
	if err != nil || device.ProductID != 0x253f || protocol != 16 {
		t.Fatal(device, protocol, err)
	}
	if _, _, err := selectCSRInstallTarget(devices, 0x24c7, validate); err == nil {
		t.Fatal("selected wrong product accepted")
	}
}

func TestExtendedRecoveryIdentityStableOnlyForSameDevice(t *testing.T) {
	backend, _ := extendedFixture(false)
	original := backend.ids[0]
	after := original
	after.Attachment = "after-reboot"
	after.Version = "1.1.10"
	if extendedRecoveryIdentity(original) != extendedRecoveryIdentity(after) {
		t.Fatal("reboot changed persistent identity")
	}
	after.Serial = "replacement"
	if extendedRecoveryIdentity(original) == extendedRecoveryIdentity(after) {
		t.Fatal("replacement retained recovery identity")
	}
}

func TestExtendedCSRReleaseRequiresExactEvidence(t *testing.T) {
	for _, protocol := range []int{7, 16, 17} {
		evidence := &modelcatalog.ReleaseEvidence{MD5Checksum: "published", CompatiblePIDs: []uint16{0x253d}, FirmwareProtocols: []int{protocol}}
		if got, err := matchedCSRProtocol("published", 0x253d, evidence); err != nil || got != protocol {
			t.Fatal(got, err)
		}
		if _, err := matchedCSRProtocol("different", 0x253d, evidence); err == nil {
			t.Fatal("wrong archive allowed")
		}
		if _, err := matchedCSRProtocol("published", 0x253f, evidence); err == nil {
			t.Fatal("wrong PID allowed")
		}
		evidence.HasUnspecifiedFirmwareProtocol = true
		if _, err := matchedCSRProtocol("published", 0x253d, evidence); err == nil {
			t.Fatal("unspecified protocol allowed")
		}
	}
	for _, protocols := range [][]int{nil, {4}, {7, 16}, {16, 17}, {99}} {
		evidence := &modelcatalog.ReleaseEvidence{MD5Checksum: "published", CompatiblePIDs: []uint16{0x253d}, FirmwareProtocols: protocols}
		if _, err := matchedCSRProtocol("published", 0x253d, evidence); err == nil {
			t.Fatal("unsupported/ambiguous protocol allowed", protocols)
		}
	}
	if _, err := matchedCSRProtocol("published", 0x253d, nil); err == nil {
		t.Fatal("nil evidence allowed")
	}
}

func TestExtendedCSRPreflightAndDuplicateNames(t *testing.T) {
	manifest := `<buildVector version="1.1.10"><maxPreloadCount>10</maxPreloadCount><targetUsbPids><usbPid>0x253D</usbPid></targetUsbPids><files><file name="fw.bin"><content>firmware</content><version>1.1.10</version><target>headset</target><partition>0</partition><language id="0x0409">English</language></file></files></buildVector>`
	path := writeFirmwareArchiveFixture(t, manifest, map[string][]byte{"fw.bin": {1, 2, 3}})
	if err := ValidateInstallInput([]string{path}); err != nil {
		t.Fatal(err)
	}
	if err := validateNativeCSRArchive(path); err == nil {
		t.Fatal("extended archive accepted by legacy transfer")
	}
	for _, entries := range []map[string][]byte{
		{"fw.bin": {1, 2, 3}, "duplicate/fw.bin": {4, 5, 6}},
		{"fw.bin": {1, 2, 3}, "duplicate/info.xml": []byte(manifest)},
	} {
		path := writeFirmwareArchiveFixture(t, manifest, entries)
		if err := ValidateInstallInput([]string{path}); err == nil {
			t.Fatal("ambiguous member accepted")
		}
	}
}

func TestLocalExtendedCSRArchiveAndTransfer(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_EXTENDED_CSR_ARCHIVE")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_EXTENDED_CSR_ARCHIVE for an offline real-file test")
	}
	if err := validateExtendedCSRArchive(path); err != nil {
		t.Fatal(err)
	}
	manifest, contents, err := parseGnVArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := prepareExtendedCSRPlan(manifest, contents, 16, 0x409, true, false)
	if err != nil {
		t.Fatal(err)
	}
	// This optional fixture exercises the supplied single-image English release.
	// It validates actual file bytes but the USB peer and reconnect are synthetic.
	if len(plan.Stages) != 1 || len(plan.Stages[0].Images) != 1 || plan.Version != [3]byte{1, 1, 10} {
		t.Fatal("fixture expects one 1.1.10 image")
	}
	backend, _ := extendedFixture(false)
	backend.peers[0].image = plan.Stages[0].Images[0].Data
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runExtendedCSRUpdate(ctx, backend, plan, func(int) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("transferred %d original image bytes through native code into an isolated peer; no physical flash", len(backend.peers[0].received))
}
