package firmware

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

type interactiveSharedCSRFixture struct {
	path                                 string
	devices                              []USBDevice
	pids                                 []uint16
	protocols                            []int
	checksum                             string
	revoked, missingRelease, unspecified bool
	requests                             int
}

func newInteractiveSharedCSRFixture(t *testing.T) *interactiveSharedCSRFixture {
	t.Helper()
	app, footer := []byte{1, 2, 3}, []byte{4, 5, 6}
	manifest := fmt.Sprintf(`<buildVector version="1.6.2" productName="Jabra Evolve2 65 Flex"><maxPreloadCount>10</maxPreloadCount><targetUsbPids><usbPid>0x2517</usbPid></targetUsbPids><files><file name="app.gnv"><partition>5</partition><crc>0x%08x</crc><language id="0x0409">English</language></file><file name="footer.gnv"><partition>254</partition><crc>0x%08x</crc><language id="0x0409">English</language></file></files></buildVector>`, referenceStageCRC(app), referenceStageCRC(footer))
	path := writeFirmwareArchiveFixture(t, manifest, map[string][]byte{"app.gnv": app, "footer.gnv": footer})
	archive, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(archive)
	port := t.TempDir()
	for k, v := range map[string]string{"busnum": "1", "devnum": "2"} {
		if err := os.WriteFile(filepath.Join(port, k), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixture := &interactiveSharedCSRFixture{path: path, devices: []USBDevice{{VendorID: JabraVendorID, ProductID: 0x2519, SysPath: port}}, pids: []uint16{0x2517, 0x2519}, protocols: []int{7}, checksum: base64.StdEncoding.EncodeToString(sum[:])}
	setupInteractiveSharedCatalog(t, fixture)
	return fixture
}
func setupInteractiveSharedCatalog(t *testing.T, f *interactiveSharedCSRFixture) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.requests++
		variants := []map[string]any{}
		for _, pid := range f.pids {
			for _, protocol := range f.protocols {
				var value any = protocol
				if f.unspecified {
					value = nil
				}
				variants = append(variants, map[string]any{"vendorId": 2830, "productId": pid, "variantType": "01-82", "fwuProtocolId": value})
			}
		}
		releases := []map[string]any{}
		if !f.missingRelease {
			releases = append(releases, map[string]any{"version": "1.6.2", "md5Checksum": f.checksum, "revoked": f.revoked})
		}
		data := map[string]any{"bundles": []any{}, "unbundledProducts": []any{map[string]any{"productName": "Jabra Evolve2 65 Flex", "variants": variants, "firmwareReleases": releases}}}
		if err := json.NewEncoder(w).Encode(data); err != nil {
			t.Errorf("catalog response: %v", err)
		}
	}))
	old := firmwareModelCatalog
	firmwareModelCatalog = &modelcatalog.Client{HTTPClient: server.Client(), BundlesURL: server.URL}
	t.Cleanup(func() { firmwareModelCatalog = old; server.Close() })
}
func TestInteractiveCSRSharedReleaseRoutesCanonical2517ToSelected2519(t *testing.T) {
	f := newInteractiveSharedCSRFixture(t)
	binding, err := interactiveInstallBindingForDevices(f.path, 0x2519, f.devices)
	if err != nil || binding == "" {
		t.Fatal("official shared native package rejected by menu", err)
	}
	if f.requests == 0 {
		t.Fatal("sibling route skipped official evidence")
	}
	selected, protocol, err := selectCSRInstallTarget(f.devices, 0x2519, func(device USBDevice) (int, error) { return validatedCSRProtocol(f.path, device) })
	if err != nil || protocol != 7 || selected.ProductID != 0x2519 {
		t.Fatal("menu/CLI route disagreed", selected, protocol, err)
	}
	if err := validateCSRInstallArchive(f.path, protocol); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.devices[0].SysPath, "devnum"), []byte("3"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := interactiveInstallBindingForDevices(f.path, 0x2519, f.devices)
	if err != nil || after == binding {
		t.Fatal("reattached device retained old binding", err)
	}
	// Rewrite bytes into another valid package with freshly matching catalog
	// evidence: the archive digest still changes the selected-device binding.
	data, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("changed archive bytes")...)
	if err := os.WriteFile(f.path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(data)
	f.checksum = base64.StdEncoding.EncodeToString(sum[:])
	setupInteractiveSharedCatalog(t, f)
	changed, err := interactiveInstallBindingForDevices(f.path, 0x2519, f.devices)
	if err != nil || changed == after {
		t.Fatal("new archive retained old binding", err)
	}
}
func TestInteractiveCSRSharedReleaseRejectsUnprovenSibling(t *testing.T) {
	for _, fault := range []string{"checksum", "selected", "canonical", "revoked", "release", "ambiguous-protocol", "missing-protocol", "wrong-protocol", "two-devices"} {
		t.Run(fault, func(t *testing.T) {
			f := newInteractiveSharedCSRFixture(t)
			switch fault {
			case "checksum":
				f.checksum = "AAAAAAAAAAAAAAAAAAAAAA=="
			case "selected":
				f.pids = []uint16{0x2517}
			case "canonical":
				f.pids = []uint16{0x2519}
			case "revoked":
				f.revoked = true
			case "release":
				f.missingRelease = true
			case "ambiguous-protocol":
				f.protocols = []int{7, 16}
			case "missing-protocol":
				f.unspecified = true
			case "wrong-protocol":
				f.protocols = []int{4}
			case "two-devices":
				f.devices = append(f.devices, f.devices[0])
			}
			if _, err := interactiveInstallBindingForDevices(f.path, 0x2519, f.devices); err == nil {
				t.Fatal("unproven target accepted")
			}
			if fault != "two-devices" && f.requests == 0 {
				t.Fatal("did not reach official evidence check")
			}
		})
	}
}
func TestLocalEvolve265FlexOfficialArchiveSharedTarget(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_EVOLVE265FLEX_ARCHIVE")
	if path == "" {
		t.Skip("set original Flex archive for read-only routing verification")
	}
	if err := validateNativeCSRArchive(path); err != nil {
		t.Fatal(err)
	}
	metadata, err := parseFirmwareManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Version != "1.6.2" || len(metadata.TargetUSBPIDs) != 1 || metadata.TargetUSBPIDs[0] != "0x2517" {
		t.Fatal("unexpected original archive manifest")
	}
	// This opt-in reads Jabra's public catalog; it does not enumerate or open HID.
	evidence, err := firmwareModelCatalog.FirmwareRelease(context.Background(), 0x2519, "1.6.2")
	if err != nil {
		t.Fatal(err)
	}
	checksum, err := firmwareFileMD5(path)
	if err != nil {
		t.Fatal(err)
	}
	if !nativeCSRReleaseMatches(checksum, 0x2519, evidence) || !containsPID(evidence.CompatiblePIDs, 0x2517) {
		t.Fatal("official release does not prove both target variants")
	}
	port := t.TempDir()
	for k, v := range map[string]string{"busnum": "1", "devnum": "2"} {
		if err := os.WriteFile(filepath.Join(port, k), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	binding, err := interactiveInstallBindingForDevices(path, 0x2519, []USBDevice{{VendorID: JabraVendorID, ProductID: 0x2519, SysPath: port}})
	if err != nil || binding == "" {
		t.Fatal("original shared package rejected", err)
	}
	t.Logf("original package validated and routed 2517 -> 2519; protocol7, MD5 %s; no hardware calls", checksum)
}

func TestInteractiveCSRPreparedSelectionRejectsReplugBeforeWriter(t *testing.T) {
	for _, kind := range []string{"new-instance-same-address", "replug"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
			f := newInteractiveSharedCSRFixture(t)
			port := f.devices[0].SysPath
			writeDevice := func(address string) {
				t.Helper()
				for name, value := range map[string]string{"idVendor": "0b0e", "idProduct": "2519", "busnum": "1", "devnum": address} {
					if err := os.WriteFile(filepath.Join(port, name), []byte(value), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			writeDevice("2")
			discoveries := 0
			discover := func() ([]USBDevice, error) { discoveries++; return f.devices, nil }
			attachment, err := captureInstallAttachmentFrom(f.devices, 0x2519)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := prepareInteractiveInstall(f.path, 0x2519, attachment, discover)
			if err != nil || plan == nil || plan.binding == "" || plan.attachment != attachment {
				t.Fatal("valid prepared selection not constructed", err)
			}
			if err := plan.validate(); err != nil {
				t.Fatal("initial prepared plan invalid", err)
			}
			if kind == "replug" {
				writeDevice("3")
			} else {
				if err := os.Rename(port, port+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(port, 0o700); err != nil {
					t.Fatal(err)
				}
				writeDevice("2")
				sameDigest, err := interactiveBindingDigest(f.devices[0], plan.archiveSHA)
				if err != nil || sameDigest != plan.binding {
					t.Fatal("replacement must keep digest to isolate attachment guard", err)
				}
			}
			if err := plan.validate(); err == nil || err.Error() != "selected USB device changed; select it again before updating" {
				t.Fatal("prepared attachment was not enforced", err)
			}
			beforeDiscovery := discoveries
			callbackCalled, writerBoundaryReached := false, false
			func() {
				defer func() {
					failure, ok := recover().(commandFailure)
					if !ok || !strings.Contains(failure.message, "selected USB device changed; select it again before updating") || !callbackCalled || writerBoundaryReached {
						t.Fatalf("install-time attachment refusal missing: %#v callback=%t writer=%t", failure, callbackCalled, writerBoundaryReached)
					}
				}()
				cmdInstallForSelection([]string{plan.path, HardwareWriteFlag}, func() error {
					callbackCalled = true
					if err := plan.validate(); err != nil {
						return err
					}
					writerBoundaryReached = true
					return errors.New("test sentinel: stop before real writer discovery")
				}, plan.archiveSHA, plan.pid, nil)
			}()
			if discoveries <= beforeDiscovery {
				t.Fatal("install-time validator did not discover current attachment")
			}
		})
	}
}
