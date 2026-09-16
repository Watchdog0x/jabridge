package firmware

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

// Independent bit-at-a-time CRC and receiver. It checks the wire bytes, file
// length, zero padding and reconstructed file rather than echoing host state.
func cameraPeerCRC(data []byte) uint16 {
	crc := uint16(0xffff)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

type cameraBulkPeer struct {
	mu             sync.Mutex
	responses      chan []byte
	data           []byte
	wanted         uint32
	started, ended bool
	commands       []byte
	fail           byte
	corrupt        bool
	drop           bool
}

func newCameraBulkPeer() *cameraBulkPeer { return &cameraBulkPeer{responses: make(chan []byte, 16)} }
func (p *cameraBulkPeer) reply(id byte, data []byte) {
	packet := make([]byte, 6+len(data))
	packet[0], packet[1] = 0xaa, id
	copy(packet[6:], data)
	p.responses <- packet
}
func (p *cameraBulkPeer) Read(ctx context.Context) ([]byte, error) {
	select {
	case raw := <-p.responses:
		return raw, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (p *cameraBulkPeer) Write(ctx context.Context, packet []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(packet) != 512 || packet[0] != 0xaa {
		return errors.New("wrong packet envelope")
	}
	n := int(binary.LittleEndian.Uint16(packet[2:4]))
	if n > 506 || !bytes.Equal(packet[6+n:], make([]byte, 506-n)) {
		return errors.New("bad length or padding")
	}
	crcData := append(append([]byte{}, packet[1:4]...), packet[6:6+n]...)
	if cameraPeerCRC(crcData) != binary.LittleEndian.Uint16(packet[4:6]) {
		return errors.New("wire CRC mismatch")
	}
	p.commands = append(p.commands, packet[1])
	switch packet[1] {
	case 4:
		digest := md5.Sum(p.data)
		if p.corrupt && p.ended {
			digest[0] ^= 1
		}
		if !p.drop {
			p.reply(22, digest[:])
		}
	case 1:
		if n != 4 || p.started {
			return errors.New("bad start")
		}
		p.started = true
		p.wanted = binary.LittleEndian.Uint32(packet[6:])
		p.data = nil
		p.reply(23, nil)
	case 2:
		if !p.started || p.ended || n == 0 {
			return errors.New("data out of order")
		}
		p.data = append(p.data, packet[6:6+n]...)
		if p.fail != 0 {
			p.reply(p.fail, nil)
			return errors.New("injected data failure")
		}
	case 3:
		if !p.started || p.ended || uint32(len(p.data)) != p.wanted {
			return errors.New("end before complete file")
		}
		p.ended = true
		p.reply(24, nil)
	default:
		return errors.New("unknown command")
	}
	return nil
}

func TestCameraBulkTransfer(t *testing.T) {
	for _, size := range []int{1, 505, 506, 507, 32769} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			data := make([]byte, size)
			for i := range data {
				data[i] = byte(i*31 + 9)
			}
			peer := newCameraBulkPeer()
			digest := md5.Sum(data)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var progress int64
			if err := transferBulkCamera(ctx, peer, bytes.NewReader(data), int64(size), digest, func(done, total int64) {
				if total != int64(size) || done < progress || done > total {
					t.Error("invalid transfer progress")
				}
				progress = done
			}); err != nil {
				t.Fatal(err)
			}
			if !peer.ended || !bytes.Equal(peer.data, data) || progress != int64(size) {
				t.Fatal("staging did not produce exact file")
			}
			if got := peer.commands; got[0] != 4 || got[1] != 1 || got[len(got)-2] != 3 || got[len(got)-1] != 4 {
				t.Fatalf("wrong command order %v", got)
			}
		})
	}
}
func TestCameraBulkSkipAndFailures(t *testing.T) {
	data := bytes.Repeat([]byte{0xa5}, 1024)
	digest := md5.Sum(data)
	for _, mode := range []string{"already-staged", "wrong-md5", "write-error", "short-file", "long-file", "stale-busy", "missing-reply"} {
		t.Run(mode, func(t *testing.T) {
			peer := newCameraBulkPeer()
			var source io.Reader = bytes.NewReader(data)
			switch mode {
			case "already-staged":
				peer.data = append([]byte(nil), data...)
			case "wrong-md5":
				peer.corrupt = true
			case "write-error":
				peer.fail = 19
			case "short-file":
				source = bytes.NewReader(data[:1000])
			case "long-file":
				source = bytes.NewReader(append(append([]byte(nil), data...), 0))
			case "stale-busy":
				peer.reply(25, nil)
			case "missing-reply":
				peer.drop = true
			}
			ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
			defer cancel()
			err := transferBulkCamera(ctx, peer, source, int64(len(data)), digest, nil)
			if mode == "already-staged" {
				if err != nil || !bytes.Equal(peer.commands, []byte{4}) {
					t.Fatalf("skip: %v %v", err, peer.commands)
				}
				return
			}
			if err == nil {
				t.Fatal("failure accepted")
			}
			if mode != "wrong-md5" && peer.ended {
				t.Fatal("incomplete file was ended")
			}
		})
	}
}

func TestCameraBulkRepliesRejectMalformed(t *testing.T) {
	for _, raw := range [][]byte{nil, {0xaa, 23}, {0, 23, 0, 0, 0, 0}, {0xaa, 15, 0, 0, 0, 0}, {0xaa, 22, 16, 0, 0, 0}, make([]byte, 513)} {
		if _, err := decodeBulkMessage(raw); err == nil {
			t.Fatalf("accepted %x", raw)
		}
	}
	for id := byte(16); id <= 25; id++ {
		if id == 22 {
			continue
		}
		if _, err := decodeBulkMessage([]byte{0xaa, id, 0, 0, 0, 0}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCameraCommandsMatchOriginalNativeCRC(t *testing.T) {
	// Synthetic inputs run through the unchanged JabraCLI 1.6.48.0 native
	// helper at 0x2a9a06 in an isolated CPU emulator, with no USB access.
	full := make([]byte, 506)
	for i := range full {
		full[i] = byte(i)
	}
	start := make([]byte, 4)
	binary.LittleEndian.PutUint32(start, 1867687068)
	for _, vector := range []struct {
		command byte
		data    []byte
		crc     uint16
	}{
		{1, start, 0x526b}, {2, []byte("123456789"), 0xc917}, {2, full, 0xe18c}, {3, nil, 0x95cc}, {4, nil, 0x105c},
	} {
		packet := make([]byte, 512)
		if err := encodeBulkCommand(packet, vector.command, vector.data); err != nil {
			t.Fatal(err)
		}
		if binary.LittleEndian.Uint16(packet[4:6]) != vector.crc {
			t.Fatalf("command %d disagrees with original-code checksum", vector.command)
		}
	}
}

func TestCameraBulkDescriptors(t *testing.T) {
	good := []byte{9, 2, 32, 0, 1, 7, 0, 0x80, 50, 9, 4, 4, 0, 2, 0xff, 0xcc, 1, 0, 7, 5, 0x86, 2, 0, 2, 0, 7, 5, 0x05, 2, 0, 2, 0}
	layout, err := parseCameraBulkInterface(good, 7)
	if err != nil || layout != (bulkUSBLayout{4, 0x86, 5}) {
		t.Fatalf("%+v %v", layout, err)
	}
	for _, mode := range []string{"inactive", "wrong-class", "wrong-subclass", "wrong-protocol", "alt", "short", "duplicate-in", "missing-out", "bad-size", "endpoint-zero"} {
		t.Run(mode, func(t *testing.T) {
			d := append([]byte(nil), good...)
			active := byte(7)
			switch mode {
			case "inactive":
				active = 1
			case "wrong-class":
				d[14] = 14
			case "wrong-subclass":
				d[15] = 0
			case "wrong-protocol":
				d[16] = 0
			case "alt":
				d[12] = 1
			case "short":
				d = d[:len(d)-1]
			case "duplicate-in":
				d[27] = 0x87
			case "missing-out":
				d = d[:25]
			case "bad-size":
				d[22] = 7
			case "endpoint-zero":
				d[20] = 0x80
			}
			if _, err := parseCameraBulkInterface(d, active); err == nil {
				t.Fatal("invalid USB interface accepted")
			}
		})
	}
	for _, size := range []uint16{8, 16, 32, 64, 512, 1024} {
		d := append([]byte(nil), good...)
		binary.LittleEndian.PutUint16(d[22:24], size)
		if _, err := parseCameraBulkInterface(d, 7); err != nil {
			t.Fatal(err)
		}
	}
}
