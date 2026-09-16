package firmware

import (
	"context"
	"errors"
	"time"
)

// XAP is the processor in the older Bluetooth chips. These operations stop
// and reset it through the DECT base's SPI bridge. They do not erase flash or
// supply a flash loader. The caller must already own a verified update session.
type sitelXAP struct {
	spi           sitelSPIWords
	chip          string
	legacyCommand func(context.Context, byte) error
}

func newSitelXAP(mailbox *sitelBCCMD, legacyCommand func(context.Context, byte) error) (*sitelXAP, error) {
	if mailbox == nil || mailbox.spi == nil || !validSitelBluecoreChip(mailbox.chip) || mailbox.chip == "elvis" && legacyCommand == nil {
		return nil, errors.New("missing identified Bluetooth processor")
	}
	return &sitelXAP{spi: mailbox.spi, chip: mailbox.chip, legacyCommand: legacyCommand}, nil
}

func (x *sitelXAP) readRegister(ctx context.Context, address uint16) (uint16, error) {
	words, err := x.spi.read(ctx, address, 1, true)
	if err != nil {
		return 0, err
	}
	if len(words) != 1 {
		return 0, errors.New("short Bluetooth register reply")
	}
	return words[0], nil
}

func (x *sitelXAP) writeRegister(ctx context.Context, address, value uint16, verified bool) error {
	return x.spi.write(ctx, address, []uint16{value}, verified)
}

func (x *sitelXAP) stop(ctx context.Context) error {
	if x.chip == "elvis" {
		return x.legacyCommand(ctx, 6)
	}
	mode, err := x.readRegister(ctx, 0xf81d)
	if err != nil {
		return err
	}
	if mode != 2 {
		mode = 3
	}
	for range 5 {
		if err := x.writeRegister(ctx, 0xf81d, mode, true); err != nil {
			return err
		}
		state, err := x.readRegister(ctx, 0xf831)
		if err != nil {
			return err
		}
		if state&1 != 0 {
			return nil
		}
		if err := waitDFU(ctx, time.Millisecond); err != nil {
			return err
		}
	}
	return errors.New("bluetooth processor did not stop")
}

func (x *sitelXAP) enableResetPower(ctx context.Context) (uint16, error) {
	if x.chip == "rick" {
		// Reference PMU write-protection sequence.
		if err := x.writeRegister(ctx, 0xf942, 0x5afe, true); err != nil {
			return 0, err
		}
	}
	power, err := x.readRegister(ctx, 0xf39f)
	if err != nil {
		return 0, err
	}
	if power&1 == 0 {
		power |= 1
		if err := x.writeRegister(ctx, 0xf39f, power, true); err != nil {
			return 0, err
		}
	}
	enables, err := x.readRegister(ctx, 0xf3bd)
	if err != nil {
		return 0, err
	}
	if enables&0x143 != 0x143 {
		if err := x.writeRegister(ctx, 0xf3bd, enables|0x143, true); err != nil {
			return 0, err
		}
	}
	return power, nil
}

func (x *sitelXAP) reset(ctx context.Context) error {
	if err := x.stop(ctx); err != nil {
		return err
	}
	if x.chip != "elvis" {
		power, err := x.enableResetPower(ctx)
		if err != nil {
			return err
		}
		power = (power | 0x400) &^ 0x800
		for _, value := range []uint16{power, power &^ 0x400, (power &^ 0x400) | 0x800} {
			if err := x.writeRegister(ctx, 0xf39f, value, true); err != nil {
				return err
			}
		}
	}
	if err := x.writeRegister(ctx, 0xf82f, 1, false); err != nil {
		return err
	}
	for range 20 {
		state, err := x.readRegister(ctx, 0xf82f)
		if err != nil {
			return err
		}
		if state == 0 || state == 0xdeaf {
			if x.chip != "elvis" {
				_, err = x.enableResetPower(ctx)
			}
			return err
		}
		if err := waitDFU(ctx, time.Millisecond); err != nil {
			return err
		}
	}
	return errors.New("bluetooth processor reset did not finish")
}

func (x *sitelXAP) resetAndStop(ctx context.Context) error {
	if x.chip == "elvis" {
		return x.legacyCommand(ctx, 8)
	}
	if err := x.reset(ctx); err != nil {
		return err
	}
	return x.stop(ctx)
}

func (x *sitelXAP) readPC(ctx context.Context) error {
	words, err := x.spi.read(ctx, 0xffe9, 2, false)
	if err != nil {
		return err
	}
	if len(words) != 2 {
		return errors.New("short Bluetooth program-counter reply")
	}
	return nil
}

func (x *sitelXAP) resume(ctx context.Context) error {
	if x.chip == "elvis" {
		return x.legacyCommand(ctx, 7)
	}
	if err := x.readPC(ctx); err != nil {
		return err
	}
	// Clear all four debug breakpoints before resuming the processor.
	for index := uint16(0); index < 4; index++ {
		if err := x.writeRegister(ctx, 0xffed, index, false); err != nil {
			return err
		}
		if err := x.spi.write(ctx, 0xffeb, []uint16{0xff, 0xffff}, true); err != nil {
			return err
		}
	}
	if err := x.readPC(ctx); err != nil {
		return err
	}
	for _, mode := range []uint16{2, 3, 2} {
		if err := x.writeRegister(ctx, 0xf81d, mode, true); err != nil {
			return err
		}
	}
	if err := x.readPC(ctx); err != nil {
		return err
	}
	return x.writeRegister(ctx, 0xf81d, 1, true)
}
