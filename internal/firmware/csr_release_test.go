package firmware

import (
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
	"testing"
)

func TestCSRReleaseCannotBorrowAnotherProtocol(t *testing.T) {
	evidence := &modelcatalog.ReleaseEvidence{MD5Checksum: "published", CompatiblePIDs: []uint16{0x24c7}, FirmwareProtocols: []int{7}}
	if !nativeCSRReleaseMatches("published", 0x24c7, evidence) {
		t.Fatal("protocol 7 rejected")
	}
	for _, protocols := range [][]int{nil, {1}, {4}, {16}, {17}, {7, 16}, {7, 17}} {
		evidence.FirmwareProtocols = protocols
		if nativeCSRReleaseMatches("published", 0x24c7, evidence) {
			t.Fatal("wrong protocol accepted", protocols)
		}
	}
	evidence.FirmwareProtocols = []int{7}
	if nativeCSRReleaseMatches("other", 0x24c7, evidence) || nativeCSRReleaseMatches("published", 0x24c8, evidence) || nativeCSRReleaseMatches("published", 0x24c7, nil) {
		t.Fatal("wrong file/device accepted")
	}
	evidence.HasUnspecifiedFirmwareProtocol = true
	if nativeCSRReleaseMatches("published", 0x24c7, evidence) {
		t.Fatal("ambiguous protocol accepted")
	}
}
