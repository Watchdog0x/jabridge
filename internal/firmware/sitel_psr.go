package firmware

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

type sitelSettings interface {
	psKeySize(context.Context, uint16) (uint16, error)
	readPSKey(context.Context, uint16, uint16) ([]uint16, error)
	writePSKey(context.Context, uint16, []uint16) error
	deletePSKey(context.Context, uint16) error
}

// Replay the selected PSR in file order after every interrupted settings stage.
// Assignments and deletions are repeatable. A radio version alone never proves
// that its separate settings stage finished. The caller keeps that checkpoint.
func applySitelPSR(ctx context.Context, device sitelSettings, chip string, records []sitelPSRRecord, beforeWrite func(int) error, progress func(int, int)) error {
	if device == nil || !validSitelBluecoreChip(chip) || len(records) == 0 || len(records) > 1000 || beforeWrite == nil {
		return errors.New("incomplete Bluetooth settings plan")
	}
	if err := validateSitelPSR(chip, records); err != nil {
		return err
	}
	for index, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := beforeWrite(index); err != nil {
			return err
		}
		if record.Delete {
			// The native delete command acknowledges removal from the writable
			// store. Reading the key afterwards can expose a ROM default, so
			// neither an empty value nor an invented "missing" status is required.
			if err := device.deletePSKey(ctx, record.Key); err != nil {
				return fmt.Errorf("delete Bluetooth setting %04x: %w", record.Key, err)
			}
		} else {
			if err := device.writePSKey(ctx, record.Key, record.Words); err != nil {
				return fmt.Errorf("write Bluetooth setting %04x: %w", record.Key, err)
			}
			count, err := device.psKeySize(ctx, record.Key)
			if err != nil {
				return err
			}
			if int(count) != len(record.Words) {
				return fmt.Errorf("bluetooth setting %04x has the wrong size after writing", record.Key)
			}
			actual, err := device.readPSKey(ctx, record.Key, count)
			if err != nil {
				return err
			}
			if !slices.Equal(actual, record.Words) {
				return fmt.Errorf("bluetooth setting %04x did not verify", record.Key)
			}
		}
		if progress != nil {
			progress(index+1, len(records))
		}
	}
	return nil
}

func validateSitelPSR(chip string, records []sitelPSRRecord) error {
	if len(records) == 0 || len(records) > 1000 || !validSitelBluecoreChip(chip) {
		return errors.New("invalid Bluetooth settings plan")
	}
	// Reject the entire plan before applying even its first operation.
	for _, record := range records {
		if record.Chip != chip && record.Chip != "all" || record.Delete && len(record.Words) != 0 || !record.Delete && (len(record.Words) == 0 || len(record.Words) > 100) {
			return errors.New("bluetooth settings do not match the selected chip")
		}
	}
	return nil
}
