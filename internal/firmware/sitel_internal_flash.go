package firmware

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"
)

type sitelInternalFlash struct {
	slow, fast sitelSPIWords
	xap        *sitelXAP
	chip       string
	revision   uint16
	sleep      func(context.Context, time.Duration) error
	operation  chan struct{}
	active     bool
	verified   bool
}

// Raw register identification also works after an interrupted flash, when the
// old application cannot boot and its BCCMD symbol table is unavailable.
func prepareSitelInternalFlash(ctx context.Context, slow, fast sitelSPIWords, sleep func(context.Context, time.Duration) error) (*sitelInternalFlash, error) {
	if slow == nil || fast == nil || sleep == nil {
		return nil, errors.New("missing internal flash transport")
	}
	code, err := buildSitelInternalFlashProgram()
	if err != nil {
		return nil, err
	}
	chip, revision, err := identifySitelInternalChip(ctx, slow)
	if err != nil {
		return nil, err
	}
	f := &sitelInternalFlash{slow: slow, fast: fast, chip: chip, revision: revision, sleep: sleep, operation: make(chan struct{}, 1)}
	f.xap = &sitelXAP{spi: slow, chip: chip}
	// A previous process may have disappeared while the RAM program was still
	// finishing a pulse. Never halt/reset an actively programming controller.
	if err := f.waitControllerIdle(ctx); err != nil {
		return nil, err
	}
	if err := f.xap.resetAndStop(ctx); err != nil {
		return nil, err
	}
	if err := fast.write(ctx, 0xf8fa, []uint16{uint16(xapInternalCodeBase>>11) | 0x2000}, true); err != nil {
		return nil, err
	}
	if err := fast.write(ctx, xapInternalReady, []uint16{0}, true); err != nil {
		return nil, err
	}
	if err := fast.write(ctx, 0x2000, code, true); err != nil {
		return nil, err
	}
	readback, err := fast.read(ctx, 0x2000, len(code), true)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(readback, code) {
		return nil, errors.New("standalone flash program RAM readback failed")
	}
	pc := []uint16{uint16(xapInternalCodeBase >> 16), uint16(xapInternalCodeBase & 0xffff)}
	if err := fast.write(ctx, 0xffe9, pc, true); err != nil {
		return nil, err
	}
	if err := f.xap.resume(ctx); err != nil {
		return nil, err
	}
	ready, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		data, err := fast.read(ready, xapInternalReady, 1, false)
		if err != nil {
			return nil, err
		}
		if len(data) == 1 && data[0] == xapInternalMagic {
			f.active = true
			return f, nil
		}
		if err := sleep(ready, time.Millisecond); err != nil {
			return nil, fmt.Errorf("standalone flash program did not start: %w", err)
		}
	}
}

// A transfer owns both transports and the shared RAM mailbox until it returns.
// Waiting callers remain cancellable; boot additionally requires verification.
func (f *sitelInternalFlash) acquire(ctx context.Context) error {
	select {
	case f.operation <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		f.release()
		return err
	}
	if !f.active {
		f.release()
		return errors.New("internal flash session is no longer active")
	}
	return nil
}

func (f *sitelInternalFlash) release() { <-f.operation }

func (f *sitelInternalFlash) waitControllerIdle(ctx context.Context) error {
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		data, err := f.slow.read(wait, 0xfb86, 1, true)
		if err != nil {
			return err
		}
		if len(data) != 1 {
			return errors.New("short internal flash controller state")
		}
		if data[0] == 0 {
			return nil
		}
		if err := f.sleep(wait, time.Millisecond); err != nil {
			return fmt.Errorf("bluetooth flash controller is still busy: %w", err)
		}
	}
}

func (f *sitelInternalFlash) waitCommand(ctx context.Context) error {
	for {
		data, err := f.fast.read(ctx, xapInternalCommand, 4, false)
		if err != nil {
			return err
		}
		if len(data) != 4 || data[3] != xapInternalMagic {
			return errors.New("standalone flash program identity changed")
		}
		if data[0] == 0 {
			if data[2] != 0 {
				return fmt.Errorf("standalone flash request failed with status %d", data[2])
			}
			return nil
		}
		if err := f.sleep(ctx, time.Millisecond); err != nil {
			return err
		}
	}
}

// commandLocked and readSectorLocked run only while their caller owns operation.
func (f *sitelInternalFlash) commandLocked(ctx context.Context, command, sector, count uint16) (resultErr error) {
	if sector >= xapInternalSectors || command < 1 || command > 3 || command == 2 && count != 0 || command != 2 && (count == 0 || count > xapInternalWords) || command == 3 && count%4 != 0 {
		return errors.New("invalid internal flash operation")
	}
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := f.waitCommand(wait); err != nil {
		return err
	}
	if err := f.fast.write(wait, xapInternalSector, []uint16{sector}, true); err != nil {
		return err
	}
	if err := f.fast.write(wait, xapInternalCount, []uint16{count, 0}, true); err != nil {
		return err
	}
	// Intent is already durable at the installer layer. A lost command reply
	// must not provoke a duplicate erase or halt the CPU in a timed pulse.
	defer func() {
		if resultErr != nil {
			settle, finish := context.WithTimeout(context.Background(), 5*time.Second)
			defer finish()
			resultErr = errors.Join(resultErr, f.waitCommand(settle))
		}
	}()
	if err := f.fast.write(wait, xapInternalCommand, []uint16{command}, true); err != nil {
		return err
	}
	return f.waitCommand(wait)
}

func (f *sitelInternalFlash) readSectorLocked(ctx context.Context, sector uint16) ([]uint16, error) {
	if err := f.commandLocked(ctx, 1, sector, xapInternalWords); err != nil {
		return nil, err
	}
	words, err := f.fast.read(ctx, xapInternalBuffer, xapInternalWords, false)
	if err != nil {
		return nil, err
	}
	if len(words) != xapInternalWords {
		return nil, errors.New("short internal flash sector readback")
	}
	return words, nil
}

func (f *sitelInternalFlash) transfer(ctx context.Context, image *sitelBluecoreImage, beforeErase func(uint16) error, progress func(int, int)) error {
	if err := f.acquire(ctx); err != nil {
		return err
	}
	defer f.release()
	f.verified = false
	if image == nil || image.Chip != f.chip || len(image.Sectors) == 0 || beforeErase == nil {
		return errors.New("bluetooth image does not match the identified chip")
	}
	var sectors []int
	for index, words := range image.Sectors {
		if index >= xapInternalSectors || len(words) != xapInternalWords {
			return errors.New("bluetooth image reaches outside the firmware flash area")
		}
		sectors = append(sectors, int(index))
	}
	sort.Ints(sectors)
	for step, value := range sectors {
		sector := uint16(value)
		wanted := image.Sectors[sector]
		actual, err := f.readSectorLocked(ctx, sector)
		if err != nil {
			return err
		}
		if !slices.Equal(actual, wanted) {
			if err := beforeErase(sector); err != nil {
				return err
			}
			if err := f.commandLocked(ctx, 2, sector, 0); err != nil {
				return err
			}
			if err := f.fast.write(ctx, xapInternalBuffer, wanted, true); err != nil {
				return err
			}
			if err := f.commandLocked(ctx, 3, sector, xapInternalWords); err != nil {
				return err
			}
			actual, err = f.readSectorLocked(ctx, sector)
			if err != nil {
				return fmt.Errorf("bluetooth sector %d readback failed: %w", sector, err)
			}
			if !slices.Equal(actual, wanted) {
				return fmt.Errorf("bluetooth sector %d readback does not match", sector)
			}
		}
		if progress != nil {
			progress(step+1, len(sectors))
		}
	}
	f.verified = true
	return nil
}

func (f *sitelInternalFlash) bootApplication(ctx context.Context) error {
	if err := f.acquire(ctx); err != nil {
		return err
	}
	defer f.release()
	if !f.verified {
		return errors.New("bluetooth firmware has not completed verification")
	}
	if err := f.waitCommand(ctx); err != nil {
		return err
	}
	if err := f.waitControllerIdle(ctx); err != nil {
		return err
	}
	// Once reset may have been sent, the old RAM-worker object cannot safely
	// be reused, even if the reset acknowledgement is lost.
	f.active = false
	if err := f.xap.reset(ctx); err != nil {
		return err
	}
	if err := f.xap.resume(ctx); err != nil {
		return err
	}
	return f.sleep(ctx, 2*time.Second)
}

func identifySitelInternalChip(ctx context.Context, slow sitelSPIWords) (string, uint16, error) {
	ff9a, err := slow.read(ctx, 0xff9a, 1, true)
	if err != nil {
		return "", 0, err
	}
	fe81, err := slow.read(ctx, 0xfe81, 1, true)
	if err != nil {
		return "", 0, err
	}
	if len(ff9a) != 1 || ff9a[0] != 0 || len(fe81) != 1 {
		return "", 0, errors.New("unsupported Engage 75 Bluetooth processor identity")
	}
	var chip string
	switch byte(fe81[0]) {
	case 0x28:
		chip = "gordon" // CSR8670 in the reference chip table
	case 0x35:
		chip = "rick" // CSR8675 in the reference chip table
	default:
		return "", 0, fmt.Errorf("unsupported Engage 75 Bluetooth revision %04x", fe81[0])
	}
	return chip, fe81[0], nil
}
