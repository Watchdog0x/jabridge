package firmware

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// Sitel's bootloader HID framing is not ordinary report-5 GNP framing. A start
// fragment carries a 16-bit message length and a separate message sequence;
// every fragment carries a rolling 4-bit sequence. The caller supplies the
// negotiated report layout and sequence state. This codec does not enter DFU,
// choose a target, erase flash or implement the link's handshake/retry policy.
type sitelHIDLayout struct {
	ReportID    byte
	ReportBytes int
	MaxMessage  int
}

func (l sitelHIDLayout) validate() error {
	// The upstream entry header occupies 21 bytes within a 16-bit allocation.
	if l.ReportBytes < 8 || l.ReportBytes > 65 || l.MaxMessage < 1 || l.MaxMessage > 65535-21 {
		return errors.New("invalid Sitel HID framing limits")
	}
	return nil
}

func encodeSitelHID(l sitelHIDLayout, payload []byte, fragmentSequence, messageSequence byte) ([][]byte, byte, error) {
	if err := l.validate(); err != nil {
		return nil, fragmentSequence, err
	}
	if len(payload) > l.MaxMessage {
		return nil, fragmentSequence, errors.New("sitel message exceeds negotiated limit")
	}
	var frames [][]byte
	remaining := payload
	for len(frames) == 0 || len(remaining) > 0 {
		raw := make([]byte, l.ReportBytes)
		raw[0] = l.ReportID
		header := 2
		if len(frames) == 0 {
			raw[1] = 0x30 | fragmentSequence&15
			binary.LittleEndian.PutUint16(raw[2:4], uint16(len(payload)))
			raw[4] = messageSequence
			header = 5
		} else {
			raw[1] = 0x40 | fragmentSequence&15
		}
		count := copy(raw[header:], remaining)
		remaining = remaining[count:]
		frames = append(frames, raw)
		fragmentSequence++
	}
	return frames, fragmentSequence & 15, nil
}

type sitelHIDMessage struct {
	Payload   []byte
	Sequence  byte
	Complete  bool
	Duplicate bool
}

type sitelHIDAssembler struct {
	layout          sitelHIDLayout
	nextFragment    byte
	lastFragment    []byte
	pending         []byte
	total           int
	messageSequence byte
	active          bool
}

func (a *sitelHIDAssembler) resetMessage() { a.pending = nil; a.total = 0; a.active = false }

// Push consumes numbered HID reports after link-level sequence negotiation.
// Bad size, reordered or changed duplicate fragments fail closed and discard
// partial data. Ordinary GNP parsing must run only after Complete is true.
func (a *sitelHIDAssembler) Push(raw []byte) (result sitelHIDMessage, err error) {
	defer func() {
		if err != nil {
			a.resetMessage()
		}
	}()
	if err = a.layout.validate(); err != nil {
		return result, err
	}
	if len(raw) != a.layout.ReportBytes || raw[0] != a.layout.ReportID {
		return result, errors.New("wrong Sitel HID report")
	}
	sequence := raw[1] & 15
	if len(a.lastFragment) > 0 && sequence == (a.nextFragment-1)&15 && bytes.Equal(raw, a.lastFragment) {
		return sitelHIDMessage{Duplicate: true}, nil
	}
	if sequence != a.nextFragment&15 {
		return result, errors.New("sitel HID fragment sequence mismatch")
	}
	var data []byte
	switch raw[1] >> 4 {
	case 3:
		if a.active {
			return result, errors.New("new Sitel message interrupted an incomplete message")
		}
		total := int(binary.LittleEndian.Uint16(raw[2:4]))
		if total > a.layout.MaxMessage {
			return result, errors.New("sitel incoming message exceeds negotiated limit")
		}
		a.total = total
		a.messageSequence = raw[4]
		a.active = true
		a.pending = make([]byte, 0, total)
		data = raw[5:]
	case 4:
		if !a.active {
			return result, errors.New("sitel continuation without start")
		}
		data = raw[2:]
	default:
		return result, errors.New("not a Sitel data fragment; link control must be handled separately")
	}
	count := min(len(data), a.total-len(a.pending))
	a.pending = append(a.pending, data[:count]...)
	a.nextFragment = (sequence + 1) & 15
	a.lastFragment = append(a.lastFragment[:0], raw...)
	if len(a.pending) == a.total {
		result = sitelHIDMessage{Payload: append([]byte(nil), a.pending...), Sequence: a.messageSequence, Complete: true}
		a.resetMessage()
	}
	return result, nil
}
