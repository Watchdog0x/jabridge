package firmware

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestSitelPSROrderReadbackAndCheckpoint(t *testing.T) {
	for _, fault := range []string{"", "checkpoint", "readback", "size", "cancel", "other-chip"} {
		t.Run(fault, func(t *testing.T) {
			peer := newSitelMailboxPeer()
			b, err := discoverSitelBCCMD(context.Background(), peer, 24)
			if err != nil {
				t.Fatal(err)
			}
			b.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
			records := []sitelPSRRecord{{Chip: "rick", Key: 0x123, Delete: true}, {Chip: "rick", Key: 0x123, Words: []uint16{3, 4}}, {Chip: "all", Key: 0x123, Words: []uint16{5}}}
			if fault == "other-chip" {
				records[2].Chip = "gordon"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if fault == "cancel" {
				cancel()
			}
			if fault == "readback" {
				peer.fault = "bad-setting-readback"
			}
			if fault == "size" {
				peer.fault = "bad-setting-size"
			}
			checkpoints := 0
			err = applySitelPSR(ctx, b, "rick", records, func(index int) error {
				if fault == "checkpoint" {
					return errors.New("disk full")
				}
				if index != checkpoints {
					t.Fatal("reordered settings")
				}
				checkpoints++
				return nil
			}, nil)
			if fault == "" {
				if err != nil || !slices.Equal(peer.keys[0x123], []uint16{5}) || checkpoints != 3 {
					t.Fatal("ordered PSR failed", err)
				}
			} else if err == nil {
				t.Fatal("fault accepted")
			}
			if (fault == "checkpoint" || fault == "cancel" || fault == "other-chip") && peer.writes != 0 {
				t.Fatal("invalid plan wrote settings")
			}
		})
	}
}
