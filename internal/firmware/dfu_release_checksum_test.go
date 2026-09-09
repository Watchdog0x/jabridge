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
			if !firmwareReleaseMatchesDevice(image.MD5, profile.RuntimePIDs[0], evidence) {
				t.Fatal("valid DFU archive rejected: checksum encoding differs from catalog")
			}
			if _, err := decodeOfficialMD5(image.MD5); err != nil {
				t.Fatal("DFU digest is not in the published form", err)
			}
			evidence.MD5Checksum = hex.EncodeToString(digest[:])
			if firmwareReleaseMatchesDevice(image.MD5, profile.RuntimePIDs[0], evidence) {
				t.Fatal("non-catalog digest form accepted")
			}
			evidence.MD5Checksum = "AAAAAAAAAAAAAAAAAAAAAA=="
			if firmwareReleaseMatchesDevice(image.MD5, profile.RuntimePIDs[0], evidence) {
				t.Fatal("different firmware bytes accepted")
			}
		})
	}
}
