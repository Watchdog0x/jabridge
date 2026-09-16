package firmware

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

type sitelSPITestPeer struct {
	*sitelBootPeer
	packets [][]byte
	fault   string
}

func newSitelSPITestPeer(t *testing.T, target byte) (*sitelSPI, *sitelSPITestPeer) {
	t.Helper()
	p := &sitelSPITestPeer{sitelBootPeer: &sitelBootPeer{world: makeEngageWorld(false)}}
	p.onPacket = p.handleSPI
	layout := sitelHIDLayout{ReportID: 10, ReportBytes: 64, MaxMessage: 1024}
	link := &sitelLink{io: p, in: layout, out: layout, timeout: time.Millisecond}
	if err := link.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, err := newSitelSPI(&sitelRequester{link: link, timeout: 20 * time.Millisecond}, target, 80)
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

func (p *sitelSPITestPeer) handleSPI(packet []byte) ([]byte, error) {
	p.packets = append(p.packets, append([]byte(nil), packet...))
	if p.fault == "write-error" {
		return nil, errors.New("injected transport error")
	}
	if len(packet) < 10 || packet[1] != 0 || packet[4] != 15 || packet[3] != 0x40 && packet[3] != 0x80 {
		return nil, errors.New("invalid raw SPI message")
	}
	address := int(packet[6]) | int(packet[7])<<8
	count := int(packet[8]) | int(packet[9])<<8
	if count == 0 || count%2 != 0 || count > 1014 || address+count/2 > 65536 || packet[0] == 12 && count > 70 {
		return nil, errors.New("invalid SPI window or byte count")
	}
	reply := []byte{0, packet[0], packet[2], 0xc0, 0xff, 0}
	if packet[3] == 0x40 {
		if len(packet) != 10 || packet[5] != 0x12 && packet[5] != 0x14 && packet[5] != 0x0b && packet[5] != 0x0d {
			return nil, errors.New("invalid SPI read")
		}
		reply[4], reply[5] = 15, packet[5]
		reply = append(reply, byte(count), byte(count>>8))
		for i := range count / 2 {
			word := address + i
			reply = append(reply, byte(word), byte(word>>8))
		}
	} else if len(packet) != 10+count || packet[5] != 0x13 && packet[5] != 0x15 && packet[5] != 0x0c && packet[5] != 0x0e {
		return nil, errors.New("invalid SPI write")
	}
	switch p.fault {
	case "nak":
		return []byte{0, packet[0], packet[2], 0xc0, 0xfe, 0}, nil
	case "short":
		reply = reply[:len(reply)-1]
	case "bad-count":
		reply[6] ^= 2
	case "wrong-source":
		reply[1]++
	case "zero-id":
		reply[2] = 0
	case "legacy-opcode":
		reply[5] -= 7
	}
	return reply, nil
}

func TestSitelSPIWordsRoutesAndAcknowledgements(t *testing.T) {
	for target, address := range map[byte]byte{2: 1, 11: 2, 13: 12, 23: 1, 15: 2, 16: 12, 24: 1} {
		for _, verified := range []bool{false, true} {
			s, peer := newSitelSPITestPeer(t, target)
			peer.dropAck = 2
			words, err := s.read(context.Background(), 0x2000, 508, verified)
			if err != nil || len(words) != 508 {
				t.Fatal("SPI read failed", err)
			}
			for i, word := range words {
				if word != 0x2000+uint16(i) {
					t.Fatal("word address, byte order or reply offset changed", i, word)
				}
			}
			if err := s.write(context.Background(), 0x2000, words, verified); err != nil {
				t.Fatal(err)
			}
			var written []byte
			for _, packet := range peer.packets {
				if packet[0] != address || packet[3]&63 != 0 {
					t.Fatal("wrong component or GNP length bits", packet[:10])
				}
				if packet[3] == 0x80 {
					written = append(written, packet[10:]...)
				}
			}
			var want bytes.Buffer
			if err := binary.Write(&want, binary.LittleEndian, words); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(written, want.Bytes()) {
				t.Fatal("lost ACK repeated or corrupted a word write")
			}
		}
	}
}

func TestSitelSPIFaultsAndLimits(t *testing.T) {
	for _, fault := range []string{"nak", "short", "bad-count", "wrong-source", "write-error"} {
		t.Run(fault, func(t *testing.T) {
			s, peer := newSitelSPITestPeer(t, 23)
			peer.fault = fault
			if _, err := s.read(context.Background(), 0x100, 2, true); err == nil {
				t.Fatal("accepted broken SPI reply")
			}
		})
	}
	for _, fault := range []string{"zero-id", "legacy-opcode"} {
		s, peer := newSitelSPITestPeer(t, 23)
		peer.fault = fault
		if words, err := s.read(context.Background(), 0x100, 1, true); err != nil || !reflect.DeepEqual(words, []uint16{0x100}) {
			t.Fatal("valid legacy reply rejected", fault, err)
		}
	}
	s, peer := newSitelSPITestPeer(t, 23)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.read(ctx, 0, 1, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.read(context.Background(), 0xffff, 2, false); err == nil {
		t.Fatal("read wrapped the address")
	}
	if err := s.write(context.Background(), 0xffff, []uint16{1, 2}, false); err == nil {
		t.Fatal("write wrapped the address")
	}
	if len(peer.packets) != 0 {
		t.Fatal("invalid or cancelled operation sent a device command")
	}
	for _, target := range []byte{0, 1, 3, 14, 22, 255} {
		if _, err := newSitelSPI(s.requester, target, 80); err == nil {
			t.Fatal("guessed unsupported Bluetooth target", target)
		}
	}
	if _, err := newSitelSPI(s.requester, 13, 0); err == nil {
		t.Fatal("borrowed the base buffer size for a remote headset")
	}
}

func TestLocalSitelSPIOriginalPackets(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_BLUECORE_SPI_ORACLE")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_BLUECORE_SPI_ORACLE for original host packet comparisons")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Vectors []struct {
			Target, Opcode byte
			Count          int
			Packets        []string
		}
	}
	if err := json.Unmarshal(data, &oracle); err != nil || len(oracle.Vectors) != 168 {
		t.Fatal("incomplete SPI oracle", err)
	}
	for _, vector := range oracle.Vectors {
		s, peer := newSitelSPITestPeer(t, vector.Target)
		if vector.Opcode < 0x10 {
			s = s.fastAccess()
		}
		if vector.Opcode == 0x12 || vector.Opcode == 0x14 || vector.Opcode == 0x0b || vector.Opcode == 0x0d {
			_, err = s.read(context.Background(), 0x2000, vector.Count, vector.Opcode == 0x14 || vector.Opcode == 0x0d)
		} else {
			words := make([]uint16, vector.Count)
			for i := range words {
				words[i] = 0x2000 + uint16(i)
			}
			err = s.write(context.Background(), 0x2000, words, vector.Opcode == 0x15 || vector.Opcode == 0x0e)
		}
		if err != nil {
			t.Fatal(err)
		}
		var packets []string
		for _, packet := range peer.packets {
			packets = append(packets, hex.EncodeToString(packet))
		}
		if !reflect.DeepEqual(packets, vector.Packets) {
			t.Fatalf("original SPI mismatch: target %d opcode %02x count %d", vector.Target, vector.Opcode, vector.Count)
		}
	}
}
