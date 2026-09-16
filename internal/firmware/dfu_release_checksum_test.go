package firmware

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func TestUSBDFUUsesPublishedChecksumEncoding(t *testing.T) {
	for _, profile := range usbDFUProfiles {
		t.Run(profile.Name, func(t *testing.T) {
			data := syntheticDFUZip(t, profile.DFUPID, syntheticDFU(profile.DFUPID, "1.2.3"), "")
			image, err := parseJabraDFUImage(data)
			if err != nil {
				t.Fatal(err)
			}
			digest := md5.Sum(data)
			published := base64.StdEncoding.EncodeToString(digest[:])
			evidence := &modelcatalog.ReleaseEvidence{MD5Checksum: published, CompatiblePIDs: profile.RuntimePIDs, FirmwareProtocols: []int{1}}
			if !usbDFUReleaseMatches(image.MD5, profile.RuntimePIDs[0], evidence) {
				t.Fatal("valid DFU archive rejected: checksum encoding differs from catalog")
			}
			if _, err := decodeOfficialMD5(image.MD5); err != nil {
				t.Fatal("DFU digest is not in the published form", err)
			}
			evidence.MD5Checksum = hex.EncodeToString(digest[:])
			if usbDFUReleaseMatches(image.MD5, profile.RuntimePIDs[0], evidence) {
				t.Fatal("non-catalog digest form accepted")
			}
			evidence.MD5Checksum = "AAAAAAAAAAAAAAAAAAAAAA=="
			if usbDFUReleaseMatches(image.MD5, profile.RuntimePIDs[0], evidence) {
				t.Fatal("different firmware bytes accepted")
			}
		})
	}
}

func TestUSBDFURejectsAmbiguousReleaseProtocol(t *testing.T) {
	for _, protocols := range [][]int{nil, {4}, {1, 4}, {1, 7}, {1, 1}} {
		evidence := &modelcatalog.ReleaseEvidence{MD5Checksum: "published", CompatiblePIDs: []uint16{0x2454}, FirmwareProtocols: protocols}
		if usbDFUReleaseMatches("published", 0x2454, evidence) {
			t.Fatal("ambiguous protocol authorized DFU", protocols)
		}
	}
	evidence := &modelcatalog.ReleaseEvidence{MD5Checksum: "published", CompatiblePIDs: []uint16{0x2454}, FirmwareProtocols: []int{1}}
	if !usbDFUReleaseMatches("published", 0x2454, evidence) {
		t.Fatal("matching protocol 1 rejected")
	}
	evidence.HasUnspecifiedFirmwareProtocol = true
	if usbDFUReleaseMatches("published", 0x2454, evidence) {
		t.Fatal("unspecified protocol authorized DFU")
	}
	if usbDFUReleaseMatches("published", 0x2454, nil) {
		t.Fatal("missing evidence authorized DFU")
	}
}
