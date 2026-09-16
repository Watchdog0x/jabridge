package firmware

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// An independent peer decodes the wire bytes; it never calls the packet builder.
type conexantTestPeer struct {
	plus                             bool
	memory                           [65536]byte
	reply                            []byte
	writes                           []conexantRecord
	failAt, corruptAt, ackMismatchAt int
	corruptCalibration               bool
	drop                             bool
}

func (p *conexantTestPeer) Write(ctx context.Context, packet []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.reply != nil {
		return errors.New("another command is still waiting for its reply")
	}
	var address, count int
	var data []byte
	write := false
	if p.plus {
		if len(packet) < 4 {
			return errors.New("short block request")
		}
		address = int(packet[2])<<8 | int(packet[3])
		count = int(packet[1])
		switch packet[0] {
		case 0x20:
			if len(packet) != 4 {
				return errors.New("bad block read")
			}
		case 0x60:
			write = true
			data = packet[4:]
			if len(data) != count {
				return errors.New("bad block write")
			}
		default:
			return errors.New("wrong block command")
		}
	} else {
		if len(packet) != 5 || packet[2] != 0 || packet[0]&0x20 == 0 {
			return errors.New("bad byte request")
		}
		if byte(uint16(packet[0])+uint16(packet[1])+uint16(packet[2])+uint16(packet[3])+uint16(packet[4])) != 0xfc {
			return errors.New("wrong legacy checksum")
		}
		address = int(packet[0]&0x5f)<<8 | int(packet[1])
		count = 1
		write = packet[0]&0x80 != 0
		data = packet[3:4]
	}
	if count == 0 || address+count > len(p.memory) {
		return errors.New("invalid memory range")
	}
	if write {
		if len(p.writes)+1 == p.failAt {
			return errors.New("injected disconnect")
		}
		p.writes = append(p.writes, conexantRecord{uint32(address), append([]byte(nil), data...)})
		copy(p.memory[address:], data)
		if len(p.writes) == p.corruptAt {
			p.memory[address] ^= 1
		}
		if p.corruptCalibration && len(p.writes) == 1 {
			p.memory[0x44] ^= 1
		}
		if !p.plus {
			p.reply = []byte{0, data[0]}
			if len(p.writes) == p.ackMismatchAt {
				p.reply[1] ^= 1
			}
		}
	} else if p.plus {
		p.reply = append([]byte(nil), p.memory[address:address+count]...)
	} else {
		p.reply = []byte{0, p.memory[address]}
	}
	return nil
}

func (p *conexantTestPeer) Read(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.drop {
		return nil, context.DeadlineExceeded
	}
	if p.reply == nil {
		return nil, errors.New("read without request")
	}
	result := p.reply
	p.reply = nil
	return result, nil
}

func newConexantTestClient(plus bool) (*conexantClient, *conexantTestPeer) {
	p := &conexantTestPeer{plus: plus}
	p.memory[0x38] = 0x42
	p.memory[0x42] = 4
	p.memory[0x43] = 3
	p.memory[0x44] = 0xa5
	p.memory[0x45] = 0x5a
	p.memory[0x46] = 0xc3
	c := &conexantClient{io: p, plus: plus, maxData: 8, wait: func(ctx context.Context, d time.Duration) error {
		if d != 50*time.Millisecond {
			return errors.New("incorrect write delay")
		}
		return ctx.Err()
	}}
	if !plus {
		c.maxData = 1
	}
	return c, p
}

func TestConexantTransferPreservesCalibrationAndWriteOrder(t *testing.T) {
	for _, plus := range []bool{false, true} {
		t.Run(fmt.Sprint(plus), func(t *testing.T) {
			c, p := newConexantTestClient(plus)
			ctx := context.Background()
			cal, err := c.captureCalibration(ctx)
			if err != nil {
				t.Fatal(err)
			}
			original := append([]byte(nil), p.memory[cal.Start:cal.Start+uint32(cal.Size)]...)
			records := []conexantRecord{{0x14, []byte{0}}, {0x40, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}}, {0x14, []byte{0x50}}}
			checkpoints := 0
			err = transferConexant(ctx, c, records, cal, func() error {
				checkpoints++
				if len(p.writes) != 0 {
					t.Fatal("checkpoint was saved after writing")
				}
				return nil
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if checkpoints != 1 || !bytes.Equal(original, p.memory[cal.Start:cal.Start+uint32(cal.Size)]) {
				t.Fatal("calibration or checkpoint changed")
			}
			if p.writes[0].Address != 0x14 || p.writes[0].Data[0] != 0 || p.writes[len(p.writes)-1].Address != 0x14 || p.writes[len(p.writes)-1].Data[0] != 0x50 {
				t.Fatal("patch activation order changed")
			}
			for a := 0x40; a < 0x4c; a++ {
				if uint32(a) >= cal.Start && uint32(a) < cal.Start+uint32(cal.Size) {
					continue
				}
				if p.memory[a] != byte(a-0x40+1) {
					t.Fatal("data beside calibration was dropped or shifted")
				}
			}
		})
	}
}

func TestConexantTransferStopsBeforeActivationOnFailure(t *testing.T) {
	for _, plus := range []bool{false, true} {
		for _, failure := range []string{"checkpoint", "disconnect", "readback", "calibration", "calibration-during-write", "cancel", "timeout", "ack"} {
			if plus && failure == "ack" {
				continue
			}
			t.Run(fmt.Sprintf("%t/%s", plus, failure), func(t *testing.T) {
				c, p := newConexantTestClient(plus)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				cal, err := c.captureCalibration(ctx)
				if err != nil {
					t.Fatal(err)
				}
				checkpoint := func() error { return nil }
				switch failure {
				case "checkpoint":
					checkpoint = func() error { return errors.New("disk full") }
				case "disconnect":
					p.failAt = 2
				case "readback":
					p.corruptAt = 2
				case "calibration":
					p.memory[0x44] ^= 1
				case "calibration-during-write":
					p.corruptCalibration = true
				case "cancel":
					cancel()
				case "timeout":
					p.drop = true
				case "ack":
					p.ackMismatchAt = 2
				}
				err = transferConexant(ctx, c, []conexantRecord{{0x14, []byte{0}}, {0x80, []byte{9}}, {0x14, []byte{0x50}}}, cal, checkpoint, nil)
				if err == nil {
					t.Fatal("failure was ignored")
				}
				for _, write := range p.writes {
					if write.Address == 0x14 && write.Data[0] == 0x50 {
						t.Fatal("activated an incomplete patch")
					}
				}
			})
		}
	}
}

func TestConexantRetryKeepsOriginalCalibrationRange(t *testing.T) {
	c, p := newConexantTestClient(true)
	ctx := context.Background()
	cal, err := c.captureCalibration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// An interrupted patch has changed the pointer. Recovery must not follow it.
	p.memory[0x38] = 0x90
	err = transferConexant(ctx, c, []conexantRecord{{0x40, []byte{1, 2, 3, 4, 5, 6, 7, 8}}}, cal, func() error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.memory[0x44] != 0xa5 || p.memory[0x46] != 7 {
		t.Fatal("retry lost original calibration exclusion")
	}
}

func TestConexantLegacyProjectionDoesNotChangePacketAddress(t *testing.T) {
	cal := conexantCalibration{Start: 0x42, Size: 5}
	plan, err := conexantTransferPlan([]conexantRecord{{0x1041, []byte{1, 2, 3, 4, 5, 6, 7}}, {0x2041, []byte{8, 9}}}, false, 1, cal)
	want := []conexantRecord{{0x1041, []byte{1}}, {0x1047, []byte{7}}, {0x2041, []byte{8}}}
	if err != nil || !reflect.DeepEqual(plan, want) {
		t.Fatal("legacy address projection changed", plan, err)
	}
}

func TestConexantCalibrationReadCannotBecomeAWrite(t *testing.T) {
	c, p := newConexantTestClient(false)
	p.memory[0x39] = 0x80
	if _, err := c.captureCalibration(context.Background()); err == nil {
		t.Fatal("accepted pointer overlapping the write flag")
	}
	if len(p.writes) != 0 {
		t.Fatal("calibration lookup wrote to hardware")
	}
	c, p = newConexantTestClient(false)
	if err := transferConexant(context.Background(), c, []conexantRecord{{0x14, []byte{0}}, {0x8000, []byte{1}}}, conexantCalibration{}, func() error { return nil }, nil); err == nil || len(p.writes) != 0 {
		t.Fatal("an unreadable patch address was discovered after writing", err)
	}
}
