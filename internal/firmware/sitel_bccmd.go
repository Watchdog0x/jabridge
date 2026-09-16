package firmware

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type sitelSPIWords interface {
	read(context.Context, uint16, int, bool) ([]uint16, error)
	write(context.Context, uint16, []uint16, bool) error
}

// The mailbox addresses come from the running chip's symbol table. They are
// never copied from a different headset, firmware image or test fixture.
type sitelBCCMD struct {
	spi      sitelSPIWords
	icb      uint16
	kick     uint16
	chip     string // vendor image-section selector, not a measured silicon ID
	revision uint16 // actual FE81 register value
	poisoned bool
	sleep    func(context.Context, time.Duration) error
}

func discoverSitelBCCMD(ctx context.Context, spi sitelSPIWords, target byte) (*sitelBCCMD, error) {
	if spi == nil {
		return nil, errors.New("missing Bluetooth SPI transport")
	}
	start := uint16(0x80)
	switch target {
	case 2:
		start = 0x100
	case 11, 13, 15, 16, 23, 24:
	default:
		return nil, errors.New("unsupported Bluetooth mailbox target")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for attempt := 0; attempt < 200; attempt++ {
		header, err := spi.read(ctx, start, 2, true)
		if err != nil {
			return nil, err
		}
		if len(header) != 2 {
			return nil, errors.New("short Bluetooth symbol-table header")
		}
		if header[0] != 0xd397 {
			if err := waitDFU(ctx, 10*time.Millisecond); err != nil {
				return nil, err
			}
			continue
		}
		b := &sitelBCCMD{spi: spi, sleep: waitDFU}
		if err := b.findMailbox(ctx, header[1]); err != nil {
			return nil, err
		}
		revision, err := spi.read(ctx, 0xfe81, 1, true)
		if err != nil {
			return nil, err
		}
		if len(revision) != 1 {
			return nil, errors.New("short Bluetooth revision reply")
		}
		b.revision = revision[0]
		// Match the reference updater's section selection. Older target 2
		// uses Elvis; later components distinguish Rick by FE81's low byte.
		switch {
		case target == 2:
			b.chip = "elvis"
		case byte(b.revision) == 0x35:
			b.chip = "rick"
		default:
			b.chip = "gordon"
		}
		return b, nil
	}
	return nil, errors.New("bluetooth symbol table did not become ready")
}

func (b *sitelBCCMD) findMailbox(ctx context.Context, table uint16) error {
	if table == 0 || int(table)+3*20+10 > 0x10000 {
		return errors.New("bluetooth symbol-table address is outside its word window")
	}
	for block := 0; block < 4; block++ {
		// The reference scans ten-word blocks at twenty-word strides.
		words, err := b.spi.read(ctx, table+uint16(block*20), 10, true)
		if err != nil {
			return err
		}
		if len(words) != 10 {
			return errors.New("short Bluetooth symbol table")
		}
		for i := 0; i < len(words); i += 2 {
			switch words[i] {
			case 0:
				return errors.New("bluetooth mailbox is missing from its symbol table")
			case 9:
				if b.icb != 0 && b.icb != words[i+1] {
					return errors.New("conflicting Bluetooth mailbox pointers")
				}
				b.icb = words[i+1]
			case 11:
				if b.kick != 0 && b.kick != words[i+1] {
					return errors.New("conflicting Bluetooth interrupt pointers")
				}
				b.kick = words[i+1]
			}
			if b.icb != 0 && b.kick != 0 {
				if b.icb > 0xfffd || b.kick == 0xffff {
					return errors.New("invalid Bluetooth mailbox pointers")
				}
				return nil
			}
		}
	}
	return errors.New("bluetooth mailbox was not found")
}

func (b *sitelBCCMD) state(ctx context.Context) (uint16, error) {
	words, err := b.spi.read(ctx, b.icb, 1, true)
	if err != nil {
		return 0, err
	}
	if len(words) != 1 {
		return 0, errors.New("short Bluetooth mailbox state")
	}
	return words[0], nil
}

func (b *sitelBCCMD) command(ctx context.Context, command uint16) (uint16, error) {
	if err := b.spi.write(ctx, b.icb, []uint16{command}, false); err != nil {
		return 0, err
	}
	if err := b.spi.write(ctx, b.kick, []uint16{0}, true); err != nil {
		return 0, err
	}
	for range 11 {
		state, err := b.state(ctx)
		if err != nil || state != command {
			return state, err
		}
		if err := waitDFU(ctx, 10*time.Millisecond); err != nil {
			return 0, err
		}
	}
	return 0, errors.New("bluetooth mailbox did not acknowledge its command")
}

func (b *sitelBCCMD) request(ctx context.Context, kind, variable uint16, payload []uint16) (_ []uint16, err error) {
	if b == nil || b.spi == nil || b.icb == 0 || b.kick == 0 || b.poisoned || kind != 0 && kind != 2 || len(payload) > 103 {
		return nil, errors.New("invalid or interrupted Bluetooth mailbox request")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	state, err := b.state(ctx)
	if err != nil {
		return nil, err
	}
	if state != 0 {
		return nil, errors.New("bluetooth mailbox is already busy; reconnect before retrying")
	}
	// An uncertain partially sent transaction cannot be reused as a fresh
	// request. The installer must reopen, rediscover and check device state.
	b.poisoned = true
	count := uint16(5 + len(payload))
	if err := b.spi.write(ctx, b.icb+1, []uint16{count}, false); err != nil {
		return nil, err
	}
	state, err = b.command(ctx, 1) // allocate
	if err != nil || state != 2 {
		return nil, fmt.Errorf("bluetooth mailbox allocation failed (state %d): %w", state, nonNilBCCMDError(err))
	}
	pointer, err := b.spi.read(ctx, b.icb+2, 1, false)
	if err != nil {
		return nil, err
	}
	if len(pointer) != 1 || pointer[0] == 0 || int(pointer[0])+int(count) > 0x10000 {
		return nil, errors.New("invalid Bluetooth command buffer")
	}
	packet := append([]uint16{kind, count, 0, variable, 0}, payload...)
	if err := b.spi.write(ctx, pointer[0], packet, false); err != nil {
		return nil, err
	}
	state, err = b.command(ctx, 4) // execute
	for err == nil && state == 5 {
		if err = waitDFU(ctx, 10*time.Millisecond); err == nil {
			state, err = b.state(ctx)
		}
	}
	if err != nil || state != 6 {
		return nil, fmt.Errorf("bluetooth mailbox execution failed (state %d): %w", state, nonNilBCCMDError(err))
	}
	reply, err := b.spi.read(ctx, pointer[0], int(count), false)
	if err != nil {
		return nil, err
	}
	state, err = b.command(ctx, 7) // release response buffer
	if err != nil || state != 0 {
		return nil, fmt.Errorf("bluetooth mailbox cleanup failed (state %d): %w", state, nonNilBCCMDError(err))
	}
	if err := b.sleep(ctx, 200*time.Millisecond); err != nil {
		return nil, err
	}
	b.poisoned = false
	if len(reply) != int(count) || reply[0] != kind+1 || reply[1] != count || reply[2] != 0 || reply[3] != variable {
		return nil, errors.New("bluetooth command response does not match its request")
	}
	if reply[4] != 0 {
		return nil, fmt.Errorf("bluetooth command %04x returned status %04x", variable, reply[4])
	}
	return reply[5:], nil
}

func nonNilBCCMDError(err error) error {
	if err != nil {
		return err
	}
	return errors.New("unexpected mailbox state")
}

func (b *sitelBCCMD) psKeySize(ctx context.Context, key uint16) (uint16, error) {
	data, err := b.request(ctx, 0, 0x3006, []uint16{key, 0, 0, 0})
	if err != nil {
		return 0, err
	}
	if data[0] != key || data[1] > 100 {
		return 0, errors.New("invalid Bluetooth settings key size")
	}
	return data[1], nil
}

func (b *sitelBCCMD) readPSKey(ctx context.Context, key, count uint16) ([]uint16, error) {
	if count == 0 || count > 100 {
		return nil, errors.New("invalid Bluetooth settings read size")
	}
	payload := make([]uint16, 3+int(count))
	payload[0], payload[1] = key, count
	data, err := b.request(ctx, 0, 0x7003, payload)
	if err != nil {
		return nil, err
	}
	if data[0] != key || data[1] != count || data[2] != 0 {
		return nil, errors.New("bluetooth settings response has the wrong key or size")
	}
	return data[3:], nil
}

func (b *sitelBCCMD) writePSKey(ctx context.Context, key uint16, words []uint16) error {
	if len(words) == 0 || len(words) > 100 {
		return errors.New("invalid Bluetooth settings write size")
	}
	_, err := b.request(ctx, 2, 0x7003, append([]uint16{key, uint16(len(words)), 0}, words...))
	return err
}

func (b *sitelBCCMD) deletePSKey(ctx context.Context, key uint16) error {
	data, err := b.request(ctx, 2, 0x500c, []uint16{key, 0, 0, 0})
	if err != nil {
		return err
	}
	if data[0] != key {
		return errors.New("bluetooth settings delete response has the wrong key")
	}
	return nil
}
