package firmware

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"
)

// The Sitel bootloader uses HP link ACKs as well as FWU request replies. They
// have different sequences and neither may substitute for the other.
type sitelLink struct {
	io                               csrStageIO
	in, out                          sitelHIDLayout
	txFragment, txMessage, rxMessage byte
	rx                               sitelHIDAssembler
	queued                           [][]byte
	ready                            bool
	timeout                          time.Duration
	lastWriteComplete                bool
}

func (l *sitelLink) control(ctx context.Context, kind, value byte) error {
	raw := make([]byte, l.out.ReportBytes)
	raw[0] = l.out.ReportID
	raw[1] = kind<<4 | l.txFragment&15
	raw[2] = value
	if err := l.io.Write(ctx, raw); err != nil {
		return err
	}
	l.txFragment = (l.txFragment + 1) & 15
	return nil
}

func (l *sitelLink) start(ctx context.Context) error {
	if l.io == nil || l.timeout <= 0 {
		return errors.New("missing Sitel link configuration")
	}
	if err := l.in.validate(); err != nil {
		return err
	}
	if err := l.out.validate(); err != nil {
		return err
	}
	l.ready = false
	l.queued = nil
	l.txMessage = 0
	l.rxMessage = 0
	l.txFragment = 0
	l.rx = sitelHIDAssembler{layout: l.in}
	for attempt := 0; attempt < 5; attempt++ {
		if err := l.control(ctx, 1, 0); err != nil {
			return err
		}
		wait, cancel := context.WithTimeout(ctx, l.timeout)
		for {
			raw, err := l.io.Read(wait)
			if err != nil {
				cancel()
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if errors.Is(err, context.DeadlineExceeded) {
					break
				}
				return err
			}
			if len(raw) != l.in.ReportBytes || raw[0] != l.in.ReportID {
				cancel()
				return errors.New("invalid Sitel handshake report")
			}
			kind := raw[1] >> 4
			if kind > 2 {
				continue
			}
			l.rx.nextFragment = (raw[1] + 1) & 15
			l.rx.lastFragment = append([]byte(nil), raw...)
			if kind == 1 {
				if err := l.control(wait, 2, 0); err != nil {
					cancel()
					return err
				}
			}
			if kind == 1 || kind == 2 {
				l.ready = true
				cancel()
				return nil
			}
		}
	}
	return errors.New("sitel bootloader handshake timed out")
}

// pump consumes one frame and returns a link acknowledgement, if any. Input
// messages are acknowledged immediately and kept in a bounded queue for the
// request layer, including replies received before our outgoing link ACK.
func (l *sitelLink) pump(ctx context.Context) (ack byte, isAck bool, err error) {
	raw, err := l.io.Read(ctx)
	if err != nil {
		return 0, false, err
	}
	if len(raw) != l.in.ReportBytes || raw[0] != l.in.ReportID {
		return 0, false, errors.New("invalid Sitel input report")
	}
	if bytes.Equal(raw, l.rx.lastFragment) {
		return 0, false, nil
	}
	kind := raw[1] >> 4
	if kind <= 1 {
		l.ready = false
		return 0, false, errors.New("sitel link reset during update")
	}
	if raw[1]&15 != l.rx.nextFragment {
		return 0, false, errors.New("sitel link fragment sequence mismatch")
	}
	if kind == 2 || kind == 5 {
		l.rx.nextFragment = (raw[1] + 1) & 15
		l.rx.lastFragment = append(l.rx.lastFragment[:0], raw...)
		return raw[2], kind == 5, nil
	}
	message, err := l.rx.Push(raw)
	if err != nil {
		return 0, false, err
	}
	if !message.Complete {
		return 0, false, nil
	}
	if message.Sequence != l.rxMessage && message.Sequence != l.rxMessage-1 {
		return 0, false, errors.New("sitel message sequence mismatch")
	}
	if err := l.control(ctx, 5, message.Sequence); err != nil {
		return 0, false, err
	}
	if message.Sequence == l.rxMessage {
		l.rxMessage++
		if len(message.Payload) > 0 {
			if len(l.queued) >= 32 {
				return 0, false, errors.New("sitel reply queue overflow")
			}
			l.queued = append(l.queued, message.Payload)
		}
	}
	return 0, false, nil
}

func (l *sitelLink) send(ctx context.Context, message []byte) error {
	l.lastWriteComplete = false
	if !l.ready {
		return errors.New("sitel link is not ready")
	}
	for attempt := 0; attempt < 5; attempt++ {
		frames, next, err := encodeSitelHID(l.out, message, l.txFragment, l.txMessage)
		if err != nil {
			return err
		}
		for _, frame := range frames {
			if err := l.io.Write(ctx, frame); err != nil {
				l.ready = false
				return err
			}
		}
		l.txFragment = next
		l.lastWriteComplete = true
		wait, cancel := context.WithTimeout(ctx, l.timeout)
		for {
			ack, ok, err := l.pump(wait)
			if err != nil {
				cancel()
				if ctx.Err() != nil {
					l.ready = false
					return ctx.Err()
				}
				if errors.Is(err, context.DeadlineExceeded) {
					break
				}
				l.ready = false
				return err
			}
			if ok && ack == l.txMessage {
				l.txMessage++
				cancel()
				return nil
			}
		}
	}
	l.ready = false
	return errors.New("sitel link acknowledgement timed out after four retries")
}

func (l *sitelLink) receive(ctx context.Context) ([]byte, error) {
	for {
		if len(l.queued) > 0 {
			message := l.queued[0]
			l.queued = l.queued[1:]
			return message, nil
		}
		if !l.ready {
			return nil, errors.New("sitel link closed")
		}
		if _, _, err := l.pump(ctx); err != nil {
			l.ready = false
			return nil, err
		}
	}
}

type sitelRequester struct {
	link     *sitelLink
	sequence byte
	address  byte
	timeout  time.Duration
}

func (s *sitelRequester) request(ctx context.Context, opcode byte, payload []byte) ([]byte, error) {
	if s.address == 0 || s.timeout <= 0 || len(payload) > 1024 {
		return nil, errors.New("invalid Sitel request")
	}
	s.sequence++
	if s.sequence == 0 {
		s.sequence++
	}
	// Variable block requests have a fixed minimum GNP length; the HP envelope
	// carries their complete size, which can exceed GNP's six-bit length field.
	length := 6 + len(payload)
	headerLength := length
	if opcode == 3 {
		headerLength = 11
	}
	if headerLength > 63 {
		return nil, errors.New("invalid Sitel request length")
	}
	packet := append([]byte{s.address, 0, s.sequence, 0x40 | byte(headerLength), 15, opcode}, payload...)
	wait, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.link.send(wait, packet); err != nil {
		return nil, err
	}
	for {
		reply, err := s.link.receive(wait)
		if err != nil {
			return nil, err
		}
		// Some Sitel bootloaders return ID zero. Only one FWU request is in
		// flight, and HP enforces ordered, deduplicated messages underneath it.
		if len(reply) < 6 || reply[0] != 0 || reply[1] != s.address || (reply[2] != s.sequence && reply[2] != 0) || reply[3]&0xc0 != 0xc0 {
			continue
		}
		if reply[4] == 0xfe {
			return nil, fmt.Errorf("sitel request %02x rejected", opcode)
		}
		if reply[4] != 15 || reply[5] != opcode {
			continue
		}
		if n := int(reply[3] & 63); n < 6 || n > len(reply) || opcode != 1 && n != len(reply) {
			return nil, errors.New("invalid Sitel reply length")
		}
		// Status one advertises write-buffer availability. It is not the
		// completion of this write; wait for status zero without resending.
		if opcode == 3 && len(reply) == 7 && reply[6] == 1 {
			continue
		}
		return append([]byte(nil), reply[6:]...), nil
	}
}
