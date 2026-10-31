package firmware

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

// This peer keeps the independent CRC/image validation of the extended peer,
// but requires a tunes-version/LCID write and the last partial batch ACK.
type languageStagePeer struct {
	csrStagePeer
	language      uint16
	withholdFinal bool
	versionWrites int
}

func (peer *languageStagePeer) Write(ctx context.Context, raw []byte) error {
	if len(raw) > 6 && raw[5] == 0x0f && raw[6] == OtaOpWriteFwVersion {
		peer.versionWrites++
		wanted := []byte{0, 2, 1, byte(peer.language), byte(peer.language >> 8)}
		if peer.withholdFinal || peer.state != 3 || uint32(peer.next) != peer.count || int(raw[4]&0x3f) != 11 || !bytes.Equal(raw[7:12], wanted) {
			return errors.New("wrong voice-pack version, LCID, or ordering")
		}
		peer.finished = true
		peer.queue = append(peer.queue, append([]byte{5, 0, 8, raw[3], 0xca, 0xff}, raw[1:6]...))
		return nil
	}
	err := peer.csrStagePeer.Write(ctx, raw)
	if err != nil {
		return err
	}
	if len(raw) > 6 && raw[6] == OtaOpWriteBlock && uint32(peer.next) == peer.count && !peer.withholdFinal && (peer.next-1)%10 != 0 {
		index := make([]byte, 4)
		binary.LittleEndian.PutUint32(index, uint32(peer.next-1))
		// Verification may already be queued. The transfer must still wait for
		// this final progress event before it can write a version.
		peer.queue = append(peer.queue, peer.event(OtaEventPreloadProgress, index...))
	}
	return nil
}

func TestCSR17LanguageRequiresFinalBatchACKAndPerFileVersion(t *testing.T) {
	for _, withheld := range []bool{false, true} {
		image := bytes.Repeat([]byte{0x51}, 105)
		peer := &languageStagePeer{csrStagePeer: csrStagePeer{image: image, reportSize: 64, chunkBytes: 52}, language: 0x407, withholdFinal: withheld}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := transferExtendedCSRStage(ctx, peer, csrExtendedStage{Image: image, Version: [3]byte{0, 2, 1}, Address: 8, ReportSize: 64, ChunkBytes: 52, Preload: 10, Timeout: time.Second, SplitLanguage: true, Language: 0x407}, nil)
		cancel()
		if withheld {
			if err == nil || peer.versionWrites != 0 {
				t.Fatal("missing last ACK allowed version or success", err, peer.versionWrites)
			}
		} else if err != nil || peer.versionWrites != 1 || !peer.finished {
			t.Fatal("correct language stage failed", err, peer.versionWrites)
		}
	}
}
