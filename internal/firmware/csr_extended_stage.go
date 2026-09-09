package firmware

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"
)

// A stage requires an exclusive, already-bound session with cancellation-aware
// I/O. There is deliberately no device-opening or discovery implementation here.
// Session setup, commit, identity-checked reconnect and final version verification
// must be qualified before the CLI can enable protocol 16/17 installation.
type csrStageIO interface {
	Write(context.Context, []byte) error
	Read(context.Context) ([]byte, error)
}

type csrExtendedStage struct {
	Partition  byte
	Image      []byte
	Version    [3]byte
	Address    byte
	ReportSize int
	ChunkBytes int // explicit negotiated limit, not inferred from HID size
	Preload    uint16
	Timeout    time.Duration
}

type csrStageTransfer struct {
	io          csrStageIO
	stage       csrExtendedStage
	seq         byte
	events      [][]byte
	sent, total uint32
	erasing     bool
}

// transferExtendedCSRStage implements the traced per-image transfer, not a
// complete firmware installation. Success means verifyStatus and version-write
// acknowledgement were received; it never claims the device rebooted correctly.
func transferExtendedCSRStage(ctx context.Context, transport csrStageIO, stage csrExtendedStage, progress func(uint32, uint32)) error {
	session := &csrStageTransfer{io: transport}
	return session.transfer(ctx, stage, progress)
}

func (session *csrStageTransfer) transfer(ctx context.Context, stage csrExtendedStage, progress func(uint32, uint32)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if session.io == nil || (stage.Address != 1 && stage.Address != 4 && stage.Address != 8) || (stage.ReportSize != 63 && stage.ReportSize != 64) || stage.ChunkBytes < 1 || stage.ChunkBytes > stage.ReportSize-11 || stage.Preload == 0 || stage.Preload > 10 || stage.Timeout <= 0 || int64(len(stage.Image)) > MaxExpandedArchiveSize {
		return errors.New("invalid extended CSR stage configuration")
	}
	count, err := otaChunkCountForPayload(len(stage.Image), stage.ChunkBytes, true)
	if err != nil {
		return err
	}
	header, err := otaCRCHeader(csrImageCRC(stage.Image), count, stage.Preload, true)
	if err != nil {
		return err
	}
	session.stage = stage
	session.events = nil
	session.sent, session.total, session.erasing = 0, count, false
	if err := session.command(ctx, OtaOpSelectPartition, []byte{stage.Partition}); err != nil {
		return err
	}
	session.erasing = true
	if err := session.command(ctx, OtaOpSendStart, nil); err != nil {
		return err
	}
	if err := session.waitEvent(ctx, OtaEventFlashEraseDone, nil); err != nil {
		return err
	}
	session.erasing = false
	if err := session.command(ctx, OtaOpWriteCrc, header); err != nil {
		return err
	}
	for logical := uint32(0); logical < count; logical++ {
		start := uint64(logical) * uint64(stage.ChunkBytes)
		end := min(start+uint64(stage.ChunkBytes), uint64(len(stage.Image)))
		frame := buildWriteBlockFull(stage.Address, uint16(logical), stage.Image[start:end], stage.ReportSize)
		if err := session.write(ctx, frame); err != nil {
			return fmt.Errorf("CSR block %d: %w", logical, err)
		}
		session.sent++
		if logical%uint32(stage.Preload) == 0 {
			match := func(body []byte) bool {
				got, bits, err := decodeOTAPreload(body)
				return err == nil && (bits == 32 && got == logical || bits == 16 && got == uint32(uint16(logical)))
			}
			if err := session.waitEvent(ctx, OtaEventPreloadProgress, match); err != nil {
				return fmt.Errorf("CSR progress at block %d: %w", logical, err)
			}
		}
		if progress != nil {
			progress(session.sent, count)
		}
	}
	if err := session.waitEvent(ctx, OtaEventVerifyStatus, func(body []byte) bool { return len(body) >= 2 && body[1] == 0 }); err != nil {
		return err
	}
	return session.command(ctx, OtaOpWriteFwVersion, stage.Version[:])
}

func (s *csrStageTransfer) write(ctx context.Context, packet []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	operation, cancel := context.WithTimeout(ctx, s.stage.Timeout)
	defer cancel()
	return s.io.Write(operation, packet)
}

// read returns a bounded, unpadded inner packet from this exact route.
func (s *csrStageTransfer) read(ctx context.Context) ([]byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := s.io.Read(ctx)
		if err != nil {
			return nil, err
		}
		if len(raw) < 6 || len(raw) > s.stage.ReportSize || raw[0] != GnpReportID {
			continue
		}
		length := int(raw[4] & 0x3f)
		if length < 5 || length+1 > len(raw) {
			return nil, errors.New("malformed CSR reply")
		}
		if raw[1] != 0 || raw[2] != s.stage.Address {
			continue
		}
		return raw[1 : length+1], nil
	}
}

func (s *csrStageTransfer) queue(packet []byte) error {
	if len(packet) < 6 || packet[3]&0xc0 != 0 || packet[4] != GnpClassCsrOta {
		return nil
	}
	body := packet[5:]
	switch body[0] {
	case OtaEventFlashEraseDone:
		if !s.erasing {
			return errors.New("unexpected CSR erase event")
		}
	case OtaEventPreloadProgress:
		if _, _, err := decodeOTAPreload(body); err != nil {
			return err
		}
	case OtaEventVerifyStatus:
		if s.sent != s.total || len(body) < 2 {
			return errors.New("premature or malformed CSR verify event")
		}
		if body[1] != 0 {
			return fmt.Errorf("CSR image verification failed: status %d", body[1])
		}
	default:
		return nil
	}
	if len(s.events) >= 64 {
		return errors.New("CSR event queue overflow")
	}
	s.events = append(s.events, append([]byte(nil), body...))
	return nil
}

func (s *csrStageTransfer) command(ctx context.Context, opcode byte, payload []byte) error {
	return s.commandClass(ctx, GnpClassCsrOta, opcode, payload)
}

func (s *csrStageTransfer) nextSeq() byte {
	s.seq++
	if s.seq == 0 {
		s.seq++
	}
	return s.seq
}

func (s *csrStageTransfer) commandClass(ctx context.Context, class, opcode byte, payload []byte) error {
	seq := s.nextSeq()
	packet := buildCommandFull(s.stage.Address, seq, class, opcode, payload, s.stage.ReportSize)
	if err := s.write(ctx, packet); err != nil {
		return fmt.Errorf("CSR command %02x: %w", opcode, err)
	}
	wait, cancel := context.WithTimeout(ctx, s.stage.Timeout)
	defer cancel()
	for {
		reply, err := s.read(wait)
		if err != nil {
			return fmt.Errorf("CSR command %02x acknowledgement: %w", opcode, err)
		}
		if reply[3]&0xc0 == 0xc0 && reply[2] == seq {
			// A sequence number wraps. Also require the echoed destination,
			// source, sequence, length/flags and class of this exact request.
			if reply[4] == 0xff && len(reply) == 10 && bytes.Equal(reply[5:10], packet[1:6]) {
				return nil
			}
			if reply[4] == 0xfe {
				return fmt.Errorf("CSR command %02x rejected", opcode)
			}
		}
		if err := s.queue(reply); err != nil {
			return err
		}
	}
}

func (s *csrStageTransfer) query(ctx context.Context, class, opcode byte) ([]byte, error) {
	seq := s.nextSeq()
	packet := buildInitQuerySized(s.stage.Address, seq, class, opcode, s.stage.ReportSize)
	if err := s.write(ctx, packet); err != nil {
		return nil, err
	}
	wait, cancel := context.WithTimeout(ctx, s.stage.Timeout)
	defer cancel()
	for {
		reply, err := s.read(wait)
		if err != nil {
			return nil, err
		}
		if reply[3]&0xc0 == 0xc0 && reply[2] == seq {
			if reply[4] == 0xfe {
				return nil, fmt.Errorf("query %02x/%02x rejected", class, opcode)
			}
			if len(reply) >= 6 && reply[4] == class && reply[5] == opcode {
				return append([]byte(nil), reply[6:]...), nil
			}
		}
		if err := s.queue(reply); err != nil {
			return nil, err
		}
	}
}

func (s *csrStageTransfer) waitEvent(ctx context.Context, opcode byte, matches func([]byte) bool) error {
	wait, cancel := context.WithTimeout(ctx, s.stage.Timeout)
	defer cancel()
	for {
		for i := 0; i < len(s.events); {
			body := s.events[i]
			if body[0] != opcode {
				i++
				continue
			}
			s.events = append(s.events[:i], s.events[i+1:]...)
			if matches == nil || matches(body) {
				return nil
			}
		}
		reply, err := s.read(wait)
		if err != nil {
			return fmt.Errorf("CSR event %02x: %w", opcode, err)
		}
		if err := s.queue(reply); err != nil {
			return err
		}
	}
}
