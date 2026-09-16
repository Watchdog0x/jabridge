package firmware

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
)

// The DECT bootloader proxies 16-bit Bluecore SPI word accesses over its HP
// link. These are different messages from the ordinary flash-area requests:
// GNP's length bits stay zero and the HP envelope holds the complete length.
// Read replies carry a two-byte byte count before their little-endian words.
type sitelSPI struct {
	requester *sitelRequester
	address   byte
	maxWords  int
	fast      bool
}

// The original flash engine uses the older fast-access opcodes, while chip
// discovery, BCCMD and reset sequences use slow access. They share one ordered
// HP connection and request sequence; this copy must not be used concurrently.
func (s sitelSPI) fastAccess() *sitelSPI {
	s.fast = true
	return &s
}

func newSitelSPI(r *sitelRequester, target byte, remoteWriteBytes int) (*sitelSPI, error) {
	if r == nil || r.link == nil || r.timeout <= 0 {
		return nil, errors.New("missing Sitel SPI connection")
	}
	var address byte
	switch target {
	case 2, 23, 24:
		address = 1
	case 11, 15:
		address = 2
	case 13, 16:
		address = 12
	default:
		return nil, errors.New("unsupported Sitel Bluetooth component")
	}
	limit := min(1014, r.link.out.MaxMessage-10, r.link.in.MaxMessage-8)
	if address == 12 {
		// Remote headset routing has a smaller limit negotiated from its
		// own bootloader. Never borrow the base's transfer buffer size.
		if remoteWriteBytes < 12 || remoteWriteBytes > 1024 {
			return nil, errors.New("missing remote Bluetooth transfer limit")
		}
		limit = min(limit, remoteWriteBytes-10)
	}
	if limit < 2 {
		return nil, errors.New("sitel link is too small for SPI words")
	}
	return &sitelSPI{requester: r, address: address, maxWords: limit / 2}, nil
}

func (s *sitelSPI) read(ctx context.Context, address uint16, count int, verified bool) ([]uint16, error) {
	if count <= 0 || count > 0x10000-int(address) {
		return nil, errors.New("bluecore read crosses the word-address window")
	}
	words := make([]uint16, count)
	opcode := byte(0x12)
	if verified {
		opcode = 0x14
	}
	if s.fast {
		opcode -= 7
	}
	for offset := 0; offset < count; {
		n := min(count-offset, s.maxWords)
		data, err := s.exchange(ctx, opcode, address+uint16(offset), n, nil)
		if err != nil {
			return nil, err
		}
		for i := range n {
			words[offset+i] = binary.LittleEndian.Uint16(data[2*i:])
		}
		offset += n
	}
	return words, nil
}

func (s *sitelSPI) write(ctx context.Context, address uint16, words []uint16, verified bool) error {
	if len(words) == 0 || len(words) > 0x10000-int(address) {
		return errors.New("bluecore write crosses the word-address window")
	}
	opcode := byte(0x13)
	if verified {
		opcode = 0x15
	}
	if s.fast {
		opcode -= 7
	}
	for offset := 0; offset < len(words); {
		n := min(len(words)-offset, s.maxWords)
		if _, err := s.exchange(ctx, opcode, address+uint16(offset), n, words[offset:offset+n]); err != nil {
			return err
		}
		offset += n
	}
	return nil
}

func (s *sitelSPI) exchange(ctx context.Context, opcode byte, address uint16, count int, words []uint16) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := s.requester
	r.sequence++
	if r.sequence == 0 {
		r.sequence++
	}
	packet := make([]byte, 10+2*len(words))
	packet[0], packet[2], packet[3], packet[4], packet[5] = s.address, r.sequence, 0x40, 15, opcode
	if words != nil {
		packet[3] = 0x80
	}
	binary.LittleEndian.PutUint16(packet[6:8], address)
	binary.LittleEndian.PutUint16(packet[8:10], uint16(count*2))
	for i, word := range words {
		binary.LittleEndian.PutUint16(packet[10+2*i:], word)
	}
	wait, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if err := r.link.send(wait, packet); err != nil {
		return nil, err
	}
	for {
		reply, err := r.link.receive(wait)
		if err != nil {
			return nil, err
		}
		if len(reply) < 6 || reply[0] != 0 || reply[1] != s.address || reply[2] != r.sequence && reply[2] != 0 || reply[3]&0xc0 != 0xc0 {
			continue
		}
		if reply[4] == 0xfe {
			return nil, fmt.Errorf("bluecore SPI request %02x rejected", opcode)
		}
		if words != nil {
			if reply[4] != 0xff {
				continue
			}
			// Native SPI write calls consume the six-byte ACK header. The
			// request sequence, addresses and HP ordering bind that ACK.
			return nil, nil
		}
		legacyOpcode := opcode
		switch opcode {
		case 0x12:
			legacyOpcode = 0x0b
		case 0x14:
			legacyOpcode = 0x0d
		}
		if reply[4] != 15 || reply[5] != opcode && reply[5] != legacyOpcode {
			continue
		}
		if len(reply) != 8+count*2 || int(binary.LittleEndian.Uint16(reply[6:8])) != count*2 {
			return nil, errors.New("bluecore SPI reply has the wrong byte count")
		}
		return append([]byte(nil), reply[8:]...), nil
	}
}
