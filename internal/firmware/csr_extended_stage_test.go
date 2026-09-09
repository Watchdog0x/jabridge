package firmware

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestCSRImageCRCGoldenVectors(t *testing.T) {
	for _, test := range []struct {
		data string
		want uint32
	}{
		{"", 0xffffffff}, {"00", 0x0f958f26}, {"0001", 0x248ef9be},
		{"313233343536373839", 0x15f56281}, {"000102030405060708090a0b0c0d0e0f", 0x30305a72},
	} {
		data, err := hex.DecodeString(test.data)
		if err != nil {
			t.Fatal(err)
		}
		if got := csrImageCRC(data); got != test.want {
			t.Fatalf("crc=%08x want=%08x", got, test.want)
		}
	}
	if got := csrImageCRC(bytes.Repeat([]byte{0x55}, 53)); got != 0x965e7551 {
		t.Fatalf("odd image crc=%08x", got)
	}
}

// An independent, bitwise reference: no production packet/CRC helper is used
// to validate bytes accepted by the peer.
func referenceStageCRC(image []byte) uint32 {
	data := append([]byte{255, 255, 255, 255}, image...)
	var result uint32
	for end := len(data); end > 0; end -= 2 {
		for _, value := range data[max(0, end-2):end] {
			for i := 0; i < 8; i++ {
				if result&0x80000000 != 0 {
					result = result<<1 ^ 0xdb710641
				} else {
					result <<= 1
				}
			}
			result ^= uint32(value)
		}
	}
	return result
}

type csrStagePeer struct {
	image, received                           []byte
	queue                                     [][]byte
	writes, next                              int
	reportSize                                int
	chunkBytes                                int
	count                                     uint32
	state                                     byte
	bad, early, missing, stale, legacy, flood bool
	finished                                  bool
	cancel                                    context.CancelFunc
}

func (p *csrStagePeer) event(op byte, data ...byte) []byte {
	return append([]byte{5, 0, 8, 0, byte(6 + len(data)), 0x0f, op}, data...)
}
func (p *csrStagePeer) Write(ctx context.Context, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.writes++
	reportSize := p.reportSize
	if reportSize == 0 {
		reportSize = 63
	}
	chunkSize := reportSize - 11
	if p.chunkBytes != 0 {
		chunkSize = p.chunkBytes
	}
	if len(raw) != reportSize || raw[0] != 5 || raw[1] != 8 || raw[2] != 0 || raw[5] != 0x0f {
		return errors.New("wrong frame or route")
	}
	length := int(raw[4] & 0x3f)
	if length < 6 || length+1 > len(raw) {
		return errors.New("wrong packet length")
	}
	op := raw[6]
	ack := append([]byte{5, 0, 8, raw[3], 0xca, 0xff}, raw[1:6]...)
	if op != 0x1a && raw[4]&0xc0 != 0x80 {
		return errors.New("wrong command flags")
	}
	switch op {
	case 0x2d:
		if p.state != 0 || length != 7 || raw[7] != 0 {
			return errors.New("wrong select")
		}
		p.state = 1
		if p.stale {
			wrongSeq := append([]byte(nil), ack...)
			wrongSeq[3]++
			wrongRoute := append([]byte(nil), ack...)
			wrongRoute[2] = 4
			p.queue = append(p.queue, wrongSeq, wrongRoute)
		}
	case 0x17:
		if p.state != 1 {
			return errors.New("wrong start order")
		}
		p.state = 2
		// Erase arrives before ACK to exercise real event/reply interleaving.
		p.queue = append(p.queue, p.event(0x18))
		if p.flood {
			for i := 0; i < 65; i++ {
				p.queue = append(p.queue, p.event(0x1b, 0, 0, 0, 0))
			}
		}
	case 0x19:
		if p.state != 2 || length != 18 || raw[11] != 0 || raw[12] != 0 || binary.LittleEndian.Uint16(raw[13:15]) != 10 {
			return errors.New("wrong extended count header")
		}
		crc := uint32(binary.LittleEndian.Uint16(raw[7:9]))<<16 | uint32(binary.LittleEndian.Uint16(raw[9:11]))
		if crc != referenceStageCRC(p.image) {
			return errors.New("wrong image CRC")
		}
		p.count = binary.LittleEndian.Uint32(raw[15:19])
		if int(p.count) != (len(p.image)+chunkSize-1)/chunkSize {
			return errors.New("truncated chunk count")
		}
		p.state = 3
		if p.early {
			p.queue = append(p.queue, p.event(0x1c, 0))
		}
	case 0x1a:
		if p.state != 3 || raw[4]&0xc0 != 0 || uint32(p.next) >= p.count {
			return errors.New("unexpected block")
		}
		n := int(binary.LittleEndian.Uint16(raw[9:11]))
		start := p.next * chunkSize
		end := min(start+chunkSize, len(p.image))
		if binary.LittleEndian.Uint16(raw[7:9]) != uint16(p.next) || n != end-start || !bytes.Equal(raw[11:11+n], p.image[start:end]) {
			return errors.New("wrong image bytes or offset")
		}
		p.received = append(p.received, raw[11:11+n]...)
		if p.next%10 == 0 && !p.missing {
			data := make([]byte, 4)
			binary.LittleEndian.PutUint32(data, uint32(p.next))
			if p.stale {
				p.queue = append(p.queue, p.event(0x1b, 0xfe, 0xff, 0xff, 0xff))
			}
			if p.legacy {
				data = data[:2]
			}
			p.queue = append(p.queue, p.event(0x1b, data...))
		}
		p.next++
		if uint32(p.next) == p.count {
			status := byte(0)
			if p.bad {
				status = 1
			}
			p.queue = append(p.queue, p.event(0x1c, status))
		}
		if p.cancel != nil {
			p.cancel()
		}
		return nil
	case 0x1e:
		if uint32(p.next) != p.count || !bytes.Equal(raw[7:10], []byte{1, 1, 10}) || p.bad {
			return errors.New("unexpected version write")
		}
		p.finished = true
	default:
		return fmt.Errorf("unexpected opcode %02x", op)
	}
	p.queue = append(p.queue, ack)
	return nil
}
func (p *csrStagePeer) Read(ctx context.Context) ([]byte, error) {
	if len(p.queue) > 0 {
		result := p.queue[0]
		p.queue = p.queue[1:]
		return result, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCSRExtendedStageTransfersLargeImage(t *testing.T) {
	for _, layout := range []struct{ report, chunk int }{{63, 52}, {64, 52}, {64, 53}} {
		reportSize, chunkBytes := layout.report, layout.chunk
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("report=%d/chunk=%d/legacy-progress=%t", reportSize, chunkBytes, legacy), func(t *testing.T) {
				image := make([]byte, 3700072)
				for i := range image {
					image[i] = byte(i*73 + i/257)
				}
				peer := &csrStagePeer{image: image, stale: true, legacy: legacy, reportSize: reportSize, chunkBytes: chunkBytes}
				want := uint32(71156)
				if chunkBytes == 53 {
					want = 69813
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				var last uint32
				err := transferExtendedCSRStage(ctx, peer, csrExtendedStage{Image: image, Version: [3]byte{1, 1, 10}, Address: 8, ReportSize: reportSize, ChunkBytes: chunkBytes, Preload: 10, Timeout: time.Second}, func(sent, total uint32) {
					if sent != last+1 || total != want {
						t.Fatal("wrong progress", sent, total)
					}
					last = sent
				})
				if err != nil || !peer.finished || last != want || !bytes.Equal(peer.received, image) {
					t.Fatal("large stage transfer", err, last)
				}
			})
		}
	}
}

func TestCSRExtendedStageFailsClosed(t *testing.T) {
	for _, name := range []string{"verify-failed", "early-verify", "missing-progress", "cancel", "flood", "invalid-address", "invalid-preload", "invalid-chunk", "empty", "already-cancelled"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			image := bytes.Repeat([]byte{0x55}, 53)
			peer := &csrStagePeer{image: image}
			stage := csrExtendedStage{Image: image, Version: [3]byte{1, 1, 10}, Address: 8, ReportSize: 63, ChunkBytes: 52, Preload: 10, Timeout: 20 * time.Millisecond}
			switch name {
			case "verify-failed":
				peer.bad = true
			case "early-verify":
				peer.early = true
			case "missing-progress":
				peer.missing = true
			case "cancel":
				peer.cancel = cancel
			case "flood":
				peer.flood = true
			case "invalid-address":
				stage.Address = 0
			case "invalid-preload":
				stage.Preload = 0
			case "invalid-chunk":
				stage.ChunkBytes = 0
			case "empty":
				stage.Image = nil
			case "already-cancelled":
				cancel()
			}
			err := transferExtendedCSRStage(ctx, peer, stage, nil)
			if err == nil || peer.finished {
				t.Fatal("failed operation reported completion", err)
			}
			if (name == "invalid-address" || name == "invalid-preload" || name == "invalid-chunk" || name == "empty" || name == "already-cancelled") && peer.writes != 0 {
				t.Fatal("invalid transfer sent data")
			}
		})
	}
}
