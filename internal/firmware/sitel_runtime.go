package firmware

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

type sitelIdentity struct {
	PID, BootPID                                           uint16
	Port, Instance, Serial, Variant, Version               string
	ControllerSerial, ControllerVariant, ControllerVersion string
	ControllerAddress                                      byte
}

var errSitelRejected = errors.New("sitel request rejected")

func sitelIdentityHash(id sitelIdentity) string {
	return extendedRecoveryIdentity(csrExtendedIdentity{PID: id.PID, Port: id.Port, Serial: id.Serial, Variant: id.Variant})
}

type sitelRuntime struct {
	io       csrStageIO
	sequence byte
	events   [][]byte
}

func (r *sitelRuntime) exchange(ctx context.Context, address, class, flags byte, body []byte) ([]byte, error) {
	if address == 0 || len(body) > 57 {
		return nil, errors.New("invalid Sitel management request")
	}
	r.sequence++
	if r.sequence == 0 {
		r.sequence++
	}
	packet := make([]byte, 64)
	copy(packet, []byte{5, address, 0, r.sequence, flags | byte(5+len(body)), class})
	copy(packet[6:], body)
	wait, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := r.io.Write(wait, packet); err != nil {
		return nil, err
	}
	if flags == 0 {
		return nil, nil
	}
	for {
		reply, err := r.read(wait)
		if err != nil {
			return nil, err
		}
		if reply[3]&0xc0 == 0 {
			if len(r.events) >= 64 {
				return nil, errors.New("sitel firmware event queue overflow")
			}
			r.events = append(r.events, reply)
			continue
		}
		if reply[0] != 0 || reply[1] != address || reply[2] != r.sequence || reply[3]&0xc0 != 0xc0 {
			continue
		}
		if reply[4] == 0xfe {
			return nil, fmt.Errorf("%w: class %02x", errSitelRejected, class)
		}
		if flags == 0x80 && reply[4] == 0xff && len(reply) == 10 && bytes.Equal(reply[5:10], packet[1:6]) {
			return nil, nil
		}
		if flags == 0x40 && len(body) == 1 && len(reply) >= 6 && reply[4] == class && reply[5] == body[0] {
			return reply[6:], nil
		}
	}
}
func (r *sitelRuntime) read(ctx context.Context) ([]byte, error) {
	for {
		raw, err := r.io.Read(ctx)
		if err != nil {
			return nil, err
		}
		if len(raw) < 6 || raw[0] != 5 {
			continue
		}
		n := int(raw[4] & 63)
		if n < 5 || n+1 > len(raw) {
			return nil, errors.New("malformed Sitel management packet")
		}
		return append([]byte(nil), raw[1:n+1]...), nil
	}
}
func (r *sitelRuntime) query(ctx context.Context, address, op byte) ([]byte, error) {
	return r.exchange(ctx, address, 2, 0x40, []byte{op})
}
func (r *sitelRuntime) text(ctx context.Context, address, op byte) (string, error) {
	data, err := r.query(ctx, address, op)
	if err != nil {
		return "", err
	}
	return decodeExtendedIdentityString(data)
}

func (r *sitelRuntime) serial(ctx context.Context, address byte) (string, error) {
	data, err := r.query(ctx, address, 1)
	if errors.Is(err, errSitelRejected) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if len(data) == 1 && data[0] == 0 {
		return "", nil
	}
	return decodeExtendedIdentityString(data)
}
func (r *sitelRuntime) variant(ctx context.Context, address byte) (string, error) {
	data, err := r.query(ctx, address, 2)
	if err != nil {
		return "", err
	}
	if len(data) < 3 || len(data) > 16 {
		return "", errors.New("invalid Sitel variant")
	}
	return hex.EncodeToString(data[1:]), nil
}

func (r *sitelRuntime) identify(ctx context.Context, device USBDevice) (sitelIdentity, error) {
	id := sitelIdentity{PID: device.ProductID, Port: device.SysPath}
	profile, ok := sitelProfileForPID(id.PID)
	if !ok || !profile.runtime(id.PID) || device.VendorID != JabraVendorID || device.ViaDongle || device.attachment == nil {
		return id, errors.New("not a bound supported Sitel runtime device")
	}
	id.Instance = device.attachment.fingerprint
	pids, err := r.query(ctx, 1, 0x11)
	if err != nil || len(pids) != 2 || (binary.LittleEndian.Uint16(pids) != id.PID && binary.LittleEndian.Uint16(pids) != profile.BootPID) {
		return id, errors.New("sitel management PID does not match USB device")
	}
	id.Serial = device.Serial
	if id.Serial == "" {
		id.Serial, err = r.serial(ctx, 1)
		if err != nil {
			return id, err
		}
	}
	if id.Serial == "" {
		return id, errors.New("headset has no readable serial identity")
	}
	id.Variant, err = r.variant(ctx, 1)
	if err != nil {
		return id, err
	}
	id.Version, err = r.text(ctx, 1, 3)
	if err != nil {
		return id, err
	}
	if _, err = parseVersionTriplet(id.Version); err != nil {
		return id, err
	}
	boot, err := r.query(ctx, 1, 0x13)
	if err != nil || len(boot) != 2 {
		return id, errors.New("sitel bootloader PID is unavailable")
	}
	id.BootPID = binary.LittleEndian.Uint16(boot)
	if id.BootPID != profile.BootPID {
		return id, errors.New("bootloader identity does not match the selected model")
	}
	protocols, err := r.query(ctx, 1, 0x14)
	if err != nil || !bytes.Contains(protocols, []byte{4}) {
		return id, errors.New("sitel device does not report firmware protocol 4")
	}
	if engageHasController(id.PID) {
		id.ControllerAddress = 3
		id.ControllerSerial, err = r.serial(ctx, 3)
		if err != nil {
			return id, fmt.Errorf("controller identity: %w", err)
		}
		id.ControllerVariant, err = r.variant(ctx, 3)
		if err != nil {
			return id, err
		}
		id.ControllerVersion, err = r.text(ctx, 3, 3)
		if err != nil {
			return id, err
		}
		if _, err = parseVersionTriplet(id.ControllerVersion); err != nil {
			return id, err
		}
	}
	return id, nil
}
