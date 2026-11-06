package firmware

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// This opt-in experiment is limited to the reported runtime USB variant.
// GetInfo image IDs are not runtime PIDs or installation authorization.
func DECTImageInfoSupported(pid uint16) bool { return pid == 0x1113 }

type dectInfoReadOnlyIO struct {
	next     csrStageIO
	validate func() error
	changed  bool
}

func (g *dectInfoReadOnlyIO) Write(ctx context.Context, packet []byte) error {
	if g.changed {
		return managementNotSent(ErrDECTInspectionChanged)
	}
	if len(packet) != 64 || packet[0] != 5 || packet[2] != 0 || packet[3] == 0 ||
		packet[4] != 0x46 || packet[5] != 15 || packet[6] != 0 ||
		(packet[1] != 1 && packet[1] != 10 && packet[1] != 3) {
		return managementNotSent(errors.New("outside the runtime GetInfo read allowlist"))
	}
	for _, value := range packet[7:] {
		if value != 0 {
			return managementNotSent(errors.New("runtime GetInfo cannot carry arguments"))
		}
	}
	if err := ctx.Err(); err != nil {
		return managementNotSent(err)
	}
	if err := g.validate(); err != nil {
		g.changed = true
		return managementNotSent(ErrDECTInspectionChanged)
	}
	return g.next.Write(ctx, packet)
}

func (g *dectInfoReadOnlyIO) Read(ctx context.Context) ([]byte, error) {
	if g.changed {
		return nil, ErrDECTInspectionChanged
	}
	data, err := g.next.Read(ctx)
	if g.validate() != nil {
		g.changed = true
		return nil, ErrDECTInspectionChanged
	}
	return data, err
}

// InspectDECTImageInfo sends only existing device-info requests through the
// normal management connection. It never opens HP or changes device mode.
func InspectDECTImageInfo(ctx context.Context, binding *ControlBinding) ([]DECTComponentCheck, error) {
	if binding == nil || !DECTImageInfoSupported(binding.device.ProductID) ||
		binding.device.VendorID != JabraVendorID || binding.device.ViaDongle {
		return nil, errors.New("runtime GetInfo requires a bound Engage 75 USB 1113 base")
	}
	if binding.Validate() != nil {
		return nil, ErrDECTInspectionChanged
	}
	transport, err := binding.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = transport.Close() }()
	runtime := sitelRuntimeFromManagement(transport)
	return inspectDECTImageInfo(ctx, binding.device, runtime.io, binding.Validate, 3*time.Second, 25*time.Second)
}

func inspectDECTImageInfo(ctx context.Context, device USBDevice, io csrStageIO, validate func() error, queryTimeout, totalTimeout time.Duration) ([]DECTComponentCheck, error) {
	if device.VendorID != JabraVendorID || device.ViaDongle || !DECTImageInfoSupported(device.ProductID) || device.attachment == nil {
		return nil, errors.New("unsupported runtime GetInfo target")
	}
	if validate() != nil {
		return nil, ErrDECTInspectionChanged
	}
	bounded, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()
	guard := &dectInfoReadOnlyIO{next: io, validate: validate}
	runtime := &sitelRuntime{io: guard}
	var checks []DECTComponentCheck
	for _, target := range []struct {
		role    string
		address byte
		image   uint16
	}{{"base", 1, 0x1111}, {"headset", 10, 0x1116}, {"display", 3, 0x1117}} {
		check := DECTComponentCheck{Role: target.role, Address: target.address, Query: "runtime GetInfo (0x0f/0x00)", Step: "opt-in image observation", State: "INFO"}
		if bounded.Err() != nil {
			check.State, check.Detail = "NOT TESTED", "check cancelled or time budget ended; no request sent"
			checks = append(checks, check)
			continue
		}
		start := time.Now()
		data, err := runtime.exchangeTimeout(bounded, target.address, 15, 0x40, []byte{0}, queryTimeout)
		check.ElapsedMS = time.Since(start).Milliseconds()
		if guard.changed || errors.Is(err, ErrDECTInspectionChanged) || validate() != nil {
			return nil, ErrDECTInspectionChanged
		}
		switch {
		case errors.Is(err, errSitelRejected):
			check.Detail = "request rejected; runtime GetInfo support is unknown"
		case errors.Is(err, context.DeadlineExceeded):
			check.Detail = "timeout; runtime GetInfo support is unknown"
		case errors.Is(err, context.Canceled):
			check.Detail = "cancelled; runtime GetInfo support is unknown"
		case err != nil:
			check.Detail = "transport or reply error; runtime GetInfo support is unknown"
		default:
			info, decodeErr := decodeSitelInfo(data)
			if decodeErr != nil {
				check.State, check.Detail = "BLOCKED", fmt.Sprintf("invalid device-info (%d bytes or unsupported geometry/mode); no image ID established", len(data))
			} else {
				check.Detail = fmt.Sprintf("measured runtime GetInfo image ID %04x:%04x; mode %d; sector %d bytes; write block %d bytes; expected image %04x:%04x; observation only, not update approval", info.ID>>16, info.ID&0xffff, info.Mode, info.SectorSize, info.WriteSize, JabraVendorID, target.image)
				if info.ID != uint32(JabraVendorID)<<16|uint32(target.image) {
					check.State = "BLOCKED"
					check.Detail += "; does not match the expected image"
				}
			}
		}
		checks = append(checks, check)
	}
	if guard.changed || validate() != nil {
		return nil, ErrDECTInspectionChanged
	}
	return checks, nil
}
