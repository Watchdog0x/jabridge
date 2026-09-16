package firmware

import (
	"context"
	"errors"
	"fmt"
)

// Bind every SPI operation to the same open base connection as the HEX peers.
// This wrapper also applies the firmware-write authorization to raw SPI writes.
type boundSitelSPI struct {
	words    sitelSPIWords
	validate func() error
}

func (s boundSitelSPI) read(ctx context.Context, address uint16, count int, verified bool) ([]uint16, error) {
	if s.validate != nil {
		if err := s.validate(); err != nil {
			return nil, err
		}
	}
	return s.words.read(ctx, address, count, verified)
}

func (s boundSitelSPI) write(ctx context.Context, address uint16, words []uint16, verified bool) error {
	if err := requireHardwareWrites(); err != nil {
		return err
	}
	if s.validate != nil {
		if err := s.validate(); err != nil {
			return err
		}
	}
	return s.words.write(ctx, address, words, verified)
}

func transferDECTPrepared(ctx context.Context, image sitelDECTPrepared, progress func(byte, int, int)) error {
	return transferSitelImage(ctx, image.Peer, image.Image, image.Info, func(done, total int) {
		if progress != nil {
			progress(image.Image.Target, done, total)
		}
	})
}

func transferEngage75Components(ctx context.Context, backend sitelDECTBackend, connection *sitelDECTConnection, archive *sitelDECTArchive, prepared []sitelDECTPrepared, state *firmwareRecoveryState, save func() error, progress func(byte, int, int)) error {
	spi, err := newSitelSPI(connection.Root, 23, 0)
	if err != nil {
		return err
	}
	slow := boundSitelSPI{words: spi, validate: connection.Validate}
	fast := boundSitelSPI{words: spi.fastAccess(), validate: connection.Validate}
	// Identify the real processor before the first component erase. Recovery
	// does not depend on the old radio application or its mailbox surviving.
	chip, revision, err := identifySitelInternalChip(ctx, slow)
	if err != nil {
		return err
	}
	if state.SitelDECT.RadioRevision != 0 && state.SitelDECT.RadioRevision != revision {
		return errors.New("engage 75 radio changed during recovery")
	}
	state.SitelDECT.RadioRevision = revision
	state.SitelDECT.SettingsVerified = false
	if err := save(); err != nil {
		return err
	}
	byTarget := make(map[byte]sitelDECTPrepared, len(prepared))
	for _, image := range prepared {
		byTarget[image.Image.Target] = image
	}
	for _, target := range engage75TargetOrder {
		stageProgress := func(done, total int) {
			if progress != nil {
				progress(target, done, total)
			}
		}
		switch target {
		case 23:
			flash, err := prepareSitelInternalFlash(ctx, slow, fast, backend.sleep)
			if err != nil {
				return err
			}
			if flash.chip != chip || flash.revision != revision {
				return errors.New("engage 75 radio identity changed before flashing")
			}
			if err := flash.transfer(ctx, archive.Engage75.Radio[chip], func(uint16) error { return save() }, stageProgress); err != nil {
				return err
			}
			if err := flash.bootApplication(ctx); err != nil {
				return err
			}
		case 24:
			mailbox, err := discoverSitelBCCMD(ctx, slow, 24)
			if err != nil {
				return err
			}
			if mailbox.chip != chip || mailbox.revision != revision {
				return errors.New("engage 75 settings mailbox belongs to a different chip")
			}
			mailbox.sleep = backend.sleep
			if err := applySitelPSR(ctx, mailbox, chip, archive.Engage75.Settings[chip], func(int) error { return save() }, stageProgress); err != nil {
				return err
			}
			state.SitelDECT.SettingsVerified = true
			if err := save(); err != nil {
				state.SitelDECT.SettingsVerified = false
				return err
			}
		default:
			image, ok := byTarget[target]
			if !ok {
				return fmt.Errorf("missing prepared Engage 75 target %d", target)
			}
			if err := transferDECTPrepared(ctx, image, progress); err != nil {
				return err
			}
		}
	}
	return nil
}
