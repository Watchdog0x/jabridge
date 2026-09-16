package firmware

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

// This independent word-memory peer models mailbox allocation and execution.
// SPI framing is exercised separately against the original host builders.
type sitelMailboxPeer struct {
	memory  [65536]uint16
	keys    map[uint16][]uint16
	writes  int
	fault   string
	packets [][]uint16
}

func newSitelMailboxPeer() *sitelMailboxPeer {
	p := &sitelMailboxPeer{keys: map[uint16][]uint16{0x123: {0xabcd, 0x4567}}}
	p.memory[0x80], p.memory[0x81] = 0xd397, 0x200
	p.memory[0x100], p.memory[0x101] = 0xd397, 0x200
	copy(p.memory[0x200:], []uint16{9, 0x300, 11, 0x320})
	p.memory[0xfe81] = 0x35
	return p
}

func (p *sitelMailboxPeer) read(ctx context.Context, address uint16, count int, _ bool) ([]uint16, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if count <= 0 || count > 65536-int(address) {
		return nil, errors.New("invalid peer memory range")
	}
	return append([]uint16(nil), p.memory[int(address):int(address)+count]...), nil
}

func (p *sitelMailboxPeer) write(ctx context.Context, address uint16, words []uint16, verified bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.writes++
	if p.fault == "lost-write" && p.writes == 4 {
		return errors.New("injected lost write")
	}
	copy(p.memory[int(address):], words)
	if address != 0x320 {
		return nil
	}
	if !verified || !reflect.DeepEqual(words, []uint16{0}) {
		return errors.New("invalid mailbox kick")
	}
	switch p.memory[0x300] {
	case 1:
		p.memory[0x300], p.memory[0x302] = 2, 0x400
		if p.fault == "bad-buffer" {
			p.memory[0x302] = 0xfffc
		}
	case 4:
		count := p.memory[0x301]
		packet := p.memory[0x400 : 0x400+count]
		p.packets = append(p.packets, append([]uint16(nil), packet...))
		if packet[1] != count || packet[2] != 0 || packet[4] != 0 || packet[0] != 0 && packet[0] != 2 {
			return errors.New("invalid BCCMD header")
		}
		key := packet[5]
		switch packet[3] {
		case 0x3006:
			if count != 9 || packet[0] != 0 {
				return errors.New("invalid key-size request")
			}
			packet[6] = uint16(len(p.keys[key]))
			if p.fault == "bad-setting-size" {
				packet[6]++
			}
		case 0x7003:
			if count != packet[6]+8 || packet[7] != 0 {
				return errors.New("invalid PSKEY request")
			}
			if packet[0] == 2 {
				p.keys[key] = append([]uint16(nil), packet[8:]...)
			} else {
				copy(packet[8:], p.keys[key])
				if p.fault == "bad-setting-readback" {
					packet[8] ^= 1
				}
			}
		case 0x500c:
			if count != 9 || packet[0] != 2 || packet[6] != 0 {
				return errors.New("invalid key-delete request")
			}
			delete(p.keys, key)
		default:
			return errors.New("unexpected BCCMD variable")
		}
		packet[0]++
		switch p.fault {
		case "status":
			packet[4] = 3
		case "wrong-variable":
			packet[3]++
		case "wrong-kind":
			packet[0] = 5
		case "wrong-length":
			packet[1]++
		case "wrong-sequence":
			packet[2]++
		}
		p.memory[0x300] = 6
	case 7:
		p.memory[0x300] = 0
	default:
		return errors.New("unexpected mailbox command")
	}
	return nil
}

func TestSitelBCCMDDiscoveryAndSettings(t *testing.T) {
	peer := newSitelMailboxPeer()
	mailbox, err := discoverSitelBCCMD(context.Background(), peer, 23)
	if err != nil || mailbox.chip != "rick" || mailbox.revision != 0x35 || mailbox.icb != 0x300 || mailbox.kick != 0x320 || peer.writes != 0 {
		t.Fatal("discovery guessed pointers or wrote to device", mailbox, err)
	}
	size, err := mailbox.psKeySize(context.Background(), 0x123)
	if err != nil || size != 2 {
		t.Fatal("key-size request failed", size, err)
	}
	words, err := mailbox.readPSKey(context.Background(), 0x123, size)
	if err != nil || !reflect.DeepEqual(words, []uint16{0xabcd, 0x4567}) {
		t.Fatal("settings read changed words", words, err)
	}
	if err := mailbox.writePSKey(context.Background(), 0x123, []uint16{0x7788}); err != nil || !reflect.DeepEqual(peer.keys[0x123], []uint16{0x7788}) {
		t.Fatal("settings write failed", err)
	}
	if err := mailbox.deletePSKey(context.Background(), 0x123); err != nil {
		t.Fatal(err)
	}
	if size, err := mailbox.psKeySize(context.Background(), 0x123); err != nil || size != 0 || peer.memory[0x300] != 0 {
		t.Fatal("settings delete or mailbox cleanup failed", size, err)
	}
}

func TestSitelBCCMDRejectsInvalidDiscovery(t *testing.T) {
	for _, mode := range []string{"table-wrap", "missing", "bad-pointer", "duplicate", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			peer := newSitelMailboxPeer()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "table-wrap":
				peer.memory[0x81] = 0xfffa
			case "missing":
				peer.memory[0x200] = 0
			case "bad-pointer":
				peer.memory[0x201] = 0xffff
			case "duplicate":
				copy(peer.memory[0x200:], []uint16{9, 0x300, 9, 0x301, 11, 0x320})
			case "cancelled":
				cancel()
			}
			if _, err := discoverSitelBCCMD(ctx, peer, 23); err == nil || peer.writes != 0 {
				t.Fatal("invalid discovery guessed a working device")
			}
		})
	}
}

func TestSitelBCCMDRejectsBrokenRepliesAndLostWrites(t *testing.T) {
	for _, fault := range []string{"status", "wrong-variable", "wrong-kind", "wrong-length", "wrong-sequence", "bad-buffer", "lost-write"} {
		t.Run(fault, func(t *testing.T) {
			peer := newSitelMailboxPeer()
			b, err := discoverSitelBCCMD(context.Background(), peer, 23)
			if err != nil {
				t.Fatal(err)
			}
			peer.fault = fault
			if _, err := b.psKeySize(context.Background(), 0x123); err == nil {
				t.Fatal("accepted invalid mailbox reply")
			}
			if fault == "bad-buffer" || fault == "lost-write" {
				writes := peer.writes
				if !b.poisoned {
					t.Fatal("interrupted mailbox is reusable")
				}
				if _, err := b.psKeySize(context.Background(), 0x123); err == nil || peer.writes != writes {
					t.Fatal("interrupted transaction was replayed")
				}
			}
		})
	}
}

func TestSitelBCCMDDifferentChipAndSecondTableBlock(t *testing.T) {
	for _, target := range []byte{2, 11, 13} {
		p := newSitelMailboxPeer()
		for i := 0; i < 10; i += 2 {
			p.memory[0x200+i], p.memory[0x201+i] = uint16(30+i), 0x500
		}
		copy(p.memory[0x214:], []uint16{9, 0x300, 11, 0x320})
		p.memory[0xfe81] = 0x34
		b, err := discoverSitelBCCMD(context.Background(), p, target)
		want := "gordon"
		if target == 2 {
			want = "elvis"
		}
		if err != nil || b.chip != want || b.icb != 0x300 {
			t.Fatal("incorrect target or symbol-table stride", target, b, err)
		}
		p.memory[0x300] = 5
		if _, err := b.psKeySize(context.Background(), 0x123); err == nil || p.writes != 0 {
			t.Fatal("existing chip command was overwritten")
		}
	}
}

func TestLocalSitelBCCMDOriginalPackets(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_BLUECORE_BCCMD_ORACLE")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_BLUECORE_BCCMD_ORACLE for original mailbox comparisons")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Vectors []struct {
			Target   byte
			Revision uint16
			Packets  [][]uint16
		}
	}
	if err := json.Unmarshal(data, &oracle); err != nil || len(oracle.Vectors) != 4 {
		t.Fatal("incomplete original mailbox audit", err)
	}
	for _, vector := range oracle.Vectors {
		peer := newSitelMailboxPeer()
		peer.memory[0xfe81] = vector.Revision
		b, err := discoverSitelBCCMD(context.Background(), peer, vector.Target)
		if err != nil {
			t.Fatal(err)
		}
		size, err := b.psKeySize(context.Background(), 0x123)
		if err != nil || size != 2 {
			t.Fatal(size, err)
		}
		if _, err := b.readPSKey(context.Background(), 0x123, size); err != nil {
			t.Fatal(err)
		}
		if err := b.writePSKey(context.Background(), 0x123, []uint16{0x7788}); err != nil {
			t.Fatal(err)
		}
		if err := b.deletePSKey(context.Background(), 0x123); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(peer.packets, vector.Packets) {
			t.Fatalf("original BCCMD mismatch for target %d", vector.Target)
		}
	}
}
