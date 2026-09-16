// Package headsetvolume controls the headset's USB audio gain separately from
// the sound server. It currently covers one firmware whose request dispatch and
// gain path have been checked by executing the original code in an emulator.
package headsetvolume

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	VendorID  = 0x0b0e
	ProductID = 0x0e36
	Firmware  = "1.11.0"
	minimum   = -48 * 256
	maximum   = 12 * 256
)

func Supported(pid uint16, version string) bool { return pid == ProductID && version == Firmware }

// Value describes the USB volume code, not a measured loudness. The firmware
// rounds requests to its internal gain steps. GET_CUR returns its saved level;
// recent physical button changes need not have been saved yet.
type Value struct {
	Percent int   `json:"percent"`
	Code    int16 `json:"code"`
}

type Control interface {
	Control(context.Context, byte, byte, uint16, uint16, []byte) (int, error)
}

// Evolve2 30 SE 1.11.0 accepts GET_CUR/SET_CUR on control endpoint zero with
// an endpoint recipient and feature-unit 2 in wIndex's high byte. This specific
// compatibility route leaves the Linux audio driver attached. It is not a
// general USB Audio request route: MIN/MAX/RES and Audio 2 are rejected by the
// original dispatcher. Open checks the exact device and Audio 1 descriptor.
// Neither this package nor its caller detaches a driver or resets the device.
func readSaved(ctx context.Context, usb Control) (Value, error) {
	if usb == nil {
		return Value{}, errors.New("missing headset volume transport")
	}
	if err := ctx.Err(); err != nil {
		return Value{}, err
	}
	data := make([]byte, 2)
	n, err := usb.Control(ctx, 0xa2, 0x81, 0x0200, 0x0200, data)
	if err != nil {
		return Value{}, fmt.Errorf("read headset volume: %w", err)
	}
	if n != 2 {
		return Value{}, errors.New("short headset volume reply")
	}
	code := int16(binary.LittleEndian.Uint16(data))
	if int(code) < minimum || int(code) > maximum {
		return Value{}, errors.New("headset returned an unsupported volume range")
	}
	return Value{Code: code, Percent: (int(code) - minimum) * 100 / (maximum - minimum)}, nil
}

func Set(ctx context.Context, usb Control, percent int) (Value, error) {
	if percent < 0 || percent > 100 {
		return Value{}, errors.New("headset volume must be from 0 to 100")
	}
	if _, err := readSaved(ctx, usb); err != nil {
		return Value{}, err
	}
	code := int16(minimum + (maximum-minimum)*percent/100)
	data := make([]byte, 2)
	binary.LittleEndian.PutUint16(data, uint16(code))
	n, err := usb.Control(ctx, 0x22, 1, 0x0200, 0x0200, data)
	if err != nil {
		return Value{}, fmt.Errorf("set headset volume: %w", err)
	}
	if n != 2 {
		return Value{}, errors.New("headset did not accept the complete volume request")
	}
	// Do not claim immediate readback: the original firmware applies gain
	// asynchronously and saves it later. The USB ACK proves request acceptance.
	return Value{Percent: percent, Code: code}, nil
}
