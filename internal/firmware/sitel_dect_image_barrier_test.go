package firmware

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"time"
)

// Exercise preparation through the real HP requester and simulated device,
// rather than just checking erase counters after an earlier mode change.
func TestEngage75VerifiesAllImageIDsBeforeModeOrArea(t *testing.T) {
	for _, failure := range []string{"mmi-mismatch", "mmi-unavailable", "base-mismatch"} {
		t.Run(failure, func(t *testing.T) {
			w := newEngage75World(syntheticEngage75Archive(), 0x35)
			w.head.applicationMode = true
			if failure == "mmi-mismatch" {
				w.mmi.wrongImageID = true
			}
			if failure == "base-mismatch" {
				w.base.wrongImageID = true
			}
			previous := commandLineRiskAccepted.Swap(true)
			defer commandLineRiskAccepted.Store(previous)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			connection, err := w.boot(ctx, w.base.device())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = connection.Close() }()
			wire := connection.Root.link.io.(*sitelBootPeer)
			original := wire.onPacket
			var packets [][]byte
			wire.onPacket = func(packet []byte) ([]byte, error) {
				packets = append(packets, append([]byte(nil), packet...))
				if failure == "mmi-unavailable" && packet[0] == 3 && packet[4] == 15 && packet[5] == 0 {
					return nil, errors.New("fixture display information unavailable")
				}
				return original(packet)
			}
			if _, err := prepareSitelDECTTransfer(ctx, w, connection, w.archive); err == nil {
				t.Fatal("unverified image identity accepted")
			}
			for _, packet := range packets {
				if packet[4] != 15 || packet[5] != 0 || len(packet) != 6 {
					t.Fatalf("mode/area/effect before all image IDs matched: %x", packet)
				}
			}
			if !w.head.applicationMode || w.head.erases+w.mmi.erases+w.base.erases != 0 || w.settingsWrites != 0 || w.saved != nil {
				t.Fatal("identity failure changed a component or recovery")
			}
		})
	}
}

func TestEngage75RechecksImageAndModeAfterIdentitySweep(t *testing.T) {
	for _, change := range []string{"mode", "image"} {
		t.Run(change, func(t *testing.T) {
			w := newEngage75World(syntheticEngage75Archive(), 0x35)
			w.head.applicationMode = true
			previous := commandLineRiskAccepted.Swap(true)
			defer commandLineRiskAccepted.Store(previous)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			connection, err := w.boot(ctx, w.base.device())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = connection.Close() }()
			wire := connection.Root.link.io.(*sitelBootPeer)
			original := wire.onPacket
			modeRequests, areaRequests := 0, 0
			wire.onPacket = func(packet []byte) ([]byte, error) {
				switch packet[5] {
				case 5:
					modeRequests++
				case 1:
					areaRequests++
				}
				reply, err := original(packet)
				if err == nil && packet[0] == 3 && packet[5] == 0 {
					if change == "mode" {
						w.head.applicationMode = false
					} else {
						w.head.wrongImageID = true
					}
				}
				return reply, err
			}
			_, err = prepareSitelDECTTransfer(ctx, w, connection, w.archive)
			if modeRequests != 0 {
				t.Fatal("used an earlier mode observation for a later transition", modeRequests)
			}
			switch change {
			case "mode":
				if err != nil {
					t.Fatal("fresh update-mode observation should not be switched again", err)
				}
			case "image":
				if err == nil || areaRequests != 0 {
					t.Fatal("changed image reached target preparation", err, areaRequests)
				}
			}
		})
	}
}

func TestEngage75ImageSweepIsOp0OnlyAndPreservesApplications(t *testing.T) {
	for _, rawSector := range []uint32{4096, 65536} {
		w := newEngage75World(syntheticEngage75Archive(), 0x35)
		w.head.applicationMode, w.mmi.applicationMode = true, true
		originalVersions := []string{w.base.version, w.head.version, w.mmi.version}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		connection, err := w.boot(ctx, w.base.device())
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		wire := connection.Root.link.io.(*sitelBootPeer)
		original := wire.onPacket
		var addresses []byte
		wire.onPacket = func(packet []byte) ([]byte, error) {
			if len(packet) != 6 || packet[4] != 15 || packet[5] != 0 {
				t.Errorf("identity sweep sent non-info operation: %x", packet)
			}
			addresses = append(addresses, packet[0])
			reply, err := original(packet)
			if err == nil && packet[0] == 10 {
				// Explicit synthetic raw forms; the report contains decoded
				// geometry and does not establish the actual raw version.
				binary.LittleEndian.PutUint32(reply[14:], rawSector)
				binary.LittleEndian.PutUint32(reply[18:], 1001)
				clear(reply[23:25])
				if rawSector == 65536 {
					reply[24] = 1
				}
			}
			return reply, err
		}
		observations, err := collectEngage75ImageInfo(ctx, connection)
		_ = connection.Close()
		cancel()
		if err != nil || len(observations) != 3 || !reflect.DeepEqual(addresses, []byte{1, 10, 3}) {
			t.Fatal("wrong identity sweep", addresses, observations, err)
		}
		for _, observed := range observations {
			if observed.Info.ID != observed.ExpectedID || observed.peer == nil {
				t.Fatal("missing measured image evidence", observed)
			}
		}
		head := observations[1].Info
		if head.Mode != 1 || head.SectorSize != 65536 || head.WriteSize != 1001 {
			t.Fatal("observed geometry/mode not preserved", head)
		}
		if !w.head.applicationMode || !w.mmi.applicationMode || !reflect.DeepEqual(originalVersions, []string{w.base.version, w.head.version, w.mmi.version}) || len(w.boots) != 0 || w.saved != nil {
			t.Fatal("identity sweep changed original applications or recovery")
		}
	}
}

func TestEngage75ImageSweepRejectsChangedBindingAndCancellation(t *testing.T) {
	w := newEngage75World(syntheticEngage75Archive(), 0x35)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := w.boot(ctx, w.base.device())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	wire := connection.Root.link.io.(*sitelBootPeer)
	original := wire.onPacket
	requests := 0
	changed := false
	wire.onPacket = func(packet []byte) ([]byte, error) {
		requests++
		result, err := original(packet)
		changed = true
		return result, err
	}
	connection.Validate = func() error {
		if changed {
			return errors.New("fixture replacement")
		}
		return nil
	}
	observations, err := collectEngage75ImageInfo(ctx, connection)
	if err == nil || observations != nil || requests != 1 {
		t.Fatal("stale evidence or later requests escaped", observations, err, requests)
	}
	changed = false
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if observations, err = collectEngage75ImageInfo(cancelled, connection); !errors.Is(err, context.Canceled) || observations != nil || requests != 1 {
		t.Fatal("cancelled sweep sent requests", observations, err, requests)
	}
}
