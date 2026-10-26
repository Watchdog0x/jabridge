package btsearch

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PairIO belongs to one captured USB attachment. Valid must reject replacement
// devices. Confirm reads the requested address's current connected pairing entry.
// Read is passive and must honor cancellation; it never sends commands.
type PairIO interface {
	Valid() error
	Command(context.Context, byte, []byte) error
	Read(context.Context) ([]byte, error)
	Confirm(context.Context, Device) (bool, error)
}

func PairCommand(device Device) ([]byte, error) {
	if device.Address == [6]byte{} || device.BluetoothType > 1 {
		return nil, errors.New("search result has no supported Bluetooth address and type")
	}
	return append([]byte{0x30, 4 | device.BluetoothType}, device.Address[:]...), nil
}

// PairDualMode follows the SDK's stop-both, typed pair, then confirmation flow.
// The reader must be opened before this call so fast connection events queue.
// Events accelerate confirmation; a once-per-second readback also handles a
// lost event without repeating either write. An ACK alone is never success.
func PairDualMode(ctx context.Context, transport PairIO, device Device) error {
	command, err := PairCommand(device)
	if err != nil {
		return err
	}
	if err := pairValid(ctx, transport); err != nil {
		return err
	}
	stop := DualModeStopCommand()
	if err := transport.Command(ctx, stop[0], stop[1:]); err != nil {
		return fmt.Errorf("stop Bluetooth discovery before pairing: %w", err)
	}
	if err := pairValid(ctx, transport); err != nil {
		return err
	}
	if err := transport.Command(ctx, command[0], command[1:]); err != nil {
		return fmt.Errorf("pair selected headset: %w", err)
	}
	attached, named := false, false
	nextCheck := time.Now().Add(time.Second)
	for {
		if err := pairValid(ctx, transport); err != nil {
			return fmt.Errorf("pairing was requested but could not be confirmed; read the device list again: %w", err)
		}
		readContext, cancel := context.WithDeadline(ctx, nextCheck)
		packet, readErr := transport.Read(readContext)
		readTimedOut := errors.Is(readContext.Err(), context.DeadlineExceeded)
		cancel()
		if ctx.Err() != nil {
			continue
		}
		if readErr != nil && !readTimedOut {
			return fmt.Errorf("pairing was requested but events could not be read; read the device list again: %w", readErr)
		}
		a, n := pairEvent(packet)
		attached, named = attached || a, named || n
		if (!attached || !named) && time.Now().Before(nextCheck) {
			continue
		}
		if err := pairValid(ctx, transport); err != nil {
			return err
		}
		confirmation, cancel := context.WithTimeout(ctx, time.Second)
		connected, err := transport.Confirm(confirmation, device)
		confirmationExpired := confirmation.Err() != nil
		cancel()
		if err := pairValid(ctx, transport); err != nil {
			return err
		}
		if err != nil && !confirmationExpired {
			return fmt.Errorf("confirm headset connection: %w", err)
		}
		if connected && !confirmationExpired {
			return nil
		}
		// Each event pair can accelerate only one read. Missing or delayed
		// names do not cause a write retry or an immediate false failure.
		attached, named = false, false
		nextCheck = time.Now().Add(time.Second)
	}
}

func pairValid(ctx context.Context, transport PairIO) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if transport == nil {
		return errors.New("pairing transport is unavailable")
	}
	return transport.Valid()
}

func pairEvent(packet []byte) (attached, named bool) {
	if len(packet) < 7 || len(packet) > 64 || packet[0] != 5 || packet[1] != 0 || packet[2] != 1 || packet[4]&0xc0 != 0 || int(packet[4])+1 != len(packet) {
		return false, false
	}
	return packet[5] == 0x0d && packet[6] == 1, packet[5] == 2 && packet[6] == 4
}
