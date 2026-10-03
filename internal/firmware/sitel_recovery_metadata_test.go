package firmware

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

type recoveryMetadataTransport func(*http.Request) (*http.Response, error)

func (f recoveryMetadataTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func setupSitelRecoveryMetadata(t *testing.T) (firmwareRecoveryState, *[]string) {
	t.Helper()
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	archive, err := os.ReadFile(syntheticEvolve2Archive(t, 0x0e44))
	if err != nil {
		t.Fatal(err)
	}
	state := firmwareRecoveryState{FormatVersion: 1, ArchiveSHA256: fmt.Sprintf("%x", sha256.Sum256(archive)), ProductName: "Jabra_Evolve2_40", FirmwareVersion: "2.11.1", TargetUSBPIDs: []string{"0x0E44"}, Attempt: 1, Protocol: 4, RuntimePID: 0x0e41, BootPID: 0x0e44, USBPort: "1-2", Phase: "entering-bootloader", TargetIdentitySHA256: strings.Repeat("a", 64)}
	if err := saveFirmwareRecoveryState(state); err != nil {
		t.Fatal(err)
	}
	checksum := md5.Sum(archive)
	encodedChecksum := base64.StdEncoding.EncodeToString(checksum[:])
	var requests []string
	transport := recoveryMetadataTransport(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.Path)
		var data []byte
		switch r.URL.Path {
		case "/v4/Firmware/e41", "/v4/Firmware/e44":
			// The firmware API knows both IDs; the model catalog only knows
			// runtime e41. A newer release exists but cannot replace recovery.
			data = []byte(`<Firmware><DeviceName>Jabra Evolve2 40</DeviceName><Releases><Release><Version>9.0.0</Version><DownloadUrl>/new.zip</DownloadUrl><FileName>new.zip</FileName></Release><Release><Version>2.11.1</Version><DownloadUrl>/original.zip</DownloadUrl><FileName>original.zip</FileName></Release></Releases></Firmware>`)
		case "/bundles.json":
			data = []byte(fmt.Sprintf(`{"bundles":[],"unbundledProducts":[{"productName":"Jabra Evolve2 40","variants":[{"vendorId":2830,"productId":3649,"fwuProtocolId":4}],"firmwareReleases":[{"version":"2.11.1","md5Checksum":"%s","revoked":false},{"version":"9.0.0","md5Checksum":"%s","revoked":false}]}]}`, encodedChecksum, encodedChecksum))
		case "/original.zip", "/new.zip":
			data = archive
		default:
			return nil, fmt.Errorf("unexpected network request %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Request: r}, nil
	})
	oldTransport, oldCatalog := http.DefaultTransport, firmwareModelCatalog
	http.DefaultTransport = transport
	firmwareModelCatalog = &modelcatalog.Client{HTTPClient: &http.Client{Transport: transport}, BundlesURL: "https://fixture.invalid/bundles.json"}
	t.Cleanup(func() { http.DefaultTransport = oldTransport; firmwareModelCatalog = oldCatalog })
	return state, &requests
}

func TestSitelRecoveryDownloadUsesOriginalRuntimeRelease(t *testing.T) {
	state, requests := setupSitelRecoveryMetadata(t)
	latest, err := LatestForPID(0x0e44)
	if err != nil || latest.Version != "2.11.1" || latest.ProductID != 0x0e44 {
		t.Fatalf("recovery offered wrong release: %+v, %v", latest, err)
	}
	directory := t.TempDir()
	file, err := DownloadLatestQuiet(0x0e44, directory)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := firmwareArchiveSHA256(file.Path)
	if err != nil || digest != state.ArchiveSHA256 || file.Version != "2.11.1" {
		t.Fatal(file, digest, err)
	}
	for _, path := range *requests {
		if path == "/v4/Firmware/e44" || path == "/new.zip" {
			t.Fatal("recovery used boot PID or newer archive", path)
		}
	}
	// The same selected boot device can reach interactive preparation.
	d := interactiveDFUTestDevice(t, 0x0e44)
	if _, err := interactiveInstallBindingForDevices(file.Path, 0x0e44, []USBDevice{d}); err != nil {
		t.Fatal(err)
	}
	result, err := DiagnoseFirmware(context.Background(), 0x0e44, directory)
	if err != nil || !result.ChecksumMatches || !result.NativeLayout {
		t.Fatalf("recovery diagnostics: %+v, %v", result, err)
	}
}

func TestSitelRecoveryMetadataDoesNotGuessMissingIdentity(t *testing.T) {
	_, requests := setupSitelRecoveryMetadata(t)
	if err := clearFirmwareRecoveryState(); err != nil {
		t.Fatal(err)
	}
	if _, err := DownloadLatestQuiet(0x0e44, t.TempDir()); err == nil || !strings.Contains(err.Error(), "recovery record is missing") {
		t.Fatal(err)
	}
	if len(*requests) != 0 {
		t.Fatal("missing recovery identity reached network")
	}
}

func TestSitelRecoveryRejectsChangedArchive(t *testing.T) {
	state, _ := setupSitelRecoveryMetadata(t)
	state.ArchiveSHA256 = strings.Repeat("f", 64)
	if err := saveFirmwareRecoveryState(state); err != nil {
		t.Fatal(err)
	}
	if _, err := DownloadLatestQuiet(0x0e44, t.TempDir()); err == nil || !strings.Contains(err.Error(), "original archive") {
		t.Fatal(err)
	}
}

func TestSitelRecoveryRejectsAnotherModelsRecord(t *testing.T) {
	state, requests := setupSitelRecoveryMetadata(t)
	state.RuntimePID, state.BootPID = 0x4052, 0x4050
	state.TargetUSBPIDs = []string{"0x4050"}
	if err := saveFirmwareRecoveryState(state); err != nil {
		t.Fatal(err)
	}
	if _, err := DownloadLatestQuiet(0x0e44, t.TempDir()); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatal(err)
	}
	if len(*requests) != 0 {
		t.Fatal("unrelated recovery record reached network")
	}
}

func TestSitelRuntimeStillOffersLatestRelease(t *testing.T) {
	_, _ = setupSitelRecoveryMetadata(t)
	latest, err := LatestForPID(0x0e41)
	if err != nil || latest.Version != "9.0.0" {
		t.Fatal(latest, err)
	}
}
