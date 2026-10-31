package firmware

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type nativeLink390CSR7Backend struct {
	device  USBDevice
	version string
}

func (b *nativeLink390CSR7Backend) Open(ctx context.Context) (csrExtendedConnection, error) {
	return openLink390CSR7Connection(ctx, b.device)
}

// Only read-only IDENT queries are repeated while waiting for active firmware.
// Commit need not cause USB re-enumeration; a fresh same-attachment version read
// is allowed. A replaced attachment must retain the bound port/model/identity.
var errCSR7RuntimeIdentityChanged = errors.New("runtime identity changed during Link 390 update")

func (b *nativeLink390CSR7Backend) Reconnect(ctx context.Context, before csrExtendedIdentity) (csrExtendedConnection, error) {
	return waitLink390Runtime(ctx, func(ctx context.Context) (csrExtendedConnection, error) { return b.probeRuntime(ctx, before) }, waitDFU)
}

// Each failed observation is followed by a full second with no management I/O.
// This is our read-only verification policy; the vendor sends nothing here.
func waitLink390Runtime(ctx context.Context, probe func(context.Context) (csrExtendedConnection, error), pause func(context.Context, time.Duration) error) (csrExtendedConnection, error) {
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return csrExtendedConnection{}, errors.Join(err, lastErr)
		}
		connection, err := probe(ctx)
		if ctx.Err() != nil {
			if connection.Close != nil {
				_ = connection.Close()
			}
			return csrExtendedConnection{}, errors.Join(ctx.Err(), err)
		}
		if err == nil {
			return connection, nil
		}
		if errors.Is(err, errCSR7RuntimeIdentityChanged) {
			return csrExtendedConnection{}, err
		}
		lastErr = err
		if err := pause(ctx, time.Second); err != nil {
			return csrExtendedConnection{}, errors.Join(err, lastErr)
		}
	}
}

func (b *nativeLink390CSR7Backend) probeRuntime(ctx context.Context, before csrExtendedIdentity) (csrExtendedConnection, error) {
	candidate := b.device
	present := validateUSBDevice(candidate) == nil
	if !present {
		devices, err := enumerateBoundUSB()
		if err != nil {
			return csrExtendedConnection{}, err
		}
		for _, device := range devices {
			if device.SysPath == b.device.SysPath && device.ProductID == b.device.ProductID && device.VendorID == JabraVendorID {
				if b.device.Serial != "" && device.Serial != b.device.Serial {
					return csrExtendedConnection{}, fmt.Errorf("%w: USB serial changed", errCSR7RuntimeIdentityChanged)
				}
				candidate, present = device, true
				break
			}
		}
	}
	if !present {
		return csrExtendedConnection{}, errors.New("selected Link 390 is not present")
	}
	connection, err := openLink390CSR7Connection(ctx, candidate)
	if err != nil {
		return csrExtendedConnection{}, err
	}
	after := connection.Identity
	if after.PID != before.PID || after.Port != before.Port || after.Serial != before.Serial || after.Variant != before.Variant {
		_ = connection.Close()
		return csrExtendedConnection{}, errCSR7RuntimeIdentityChanged
	}
	b.device = candidate
	if after.Version == b.version {
		return connection, nil
	}
	_ = connection.Close()
	return csrExtendedConnection{}, fmt.Errorf("reported firmware is still %s; waiting for %s", after.Version, b.version)
}

func openLink390CSR7Connection(ctx context.Context, device USBDevice) (csrExtendedConnection, error) {
	if !isLink390PID(device.ProductID) {
		return csrExtendedConnection{}, errors.New("not a Link 390 firmware target")
	}
	transport, err := openBoundCSR(device)
	if err != nil {
		return csrExtendedConnection{}, err
	}
	connection, err := identifyCSRConnection(ctx, csrContextHID{transport}, device, transport.reportSize, transport.Close, true)
	if err == nil {
		err = validateCSR7Connection(connection)
	}
	if err != nil {
		_ = transport.Close()
		return csrExtendedConnection{}, err
	}
	return connection, nil
}
