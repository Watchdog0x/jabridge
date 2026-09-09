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

type engageIdentity struct {
	PID, BootPID                                           uint16
	Port, Instance, Serial, Variant, Version               string
	ControllerSerial, ControllerVariant, ControllerVersion string
	ControllerAddress                                      byte
}

var errEngageRejected = errors.New("engage request rejected")

func engageRuntimePID(pid uint16) bool    { return pid >= 0x4051 && pid <= 0x4056 }
func engageHasController(pid uint16) bool { return pid >= 0x4051 && pid <= 0x4054 }
func engageIdentityHash(id engageIdentity) string {
	return extendedRecoveryIdentity(csrExtendedIdentity{PID: id.PID, Port: id.Port, Serial: id.Serial, Variant: id.Variant})
}
func engageControllerHash(id engageIdentity) string {
	return extendedRecoveryIdentity(csrExtendedIdentity{PID: id.PID, Port: id.Port, Serial: id.ControllerSerial, Variant: id.ControllerVariant})
}

func engageControllerMatches(wanted string, id engageIdentity) bool {
	if engageControllerHash(id) == wanted {
		return true
	}
	// An originally serial-less controller stays bound by parent port and
	// variant even if newer firmware later exposes a serial. A previously
	// recorded nonempty serial cannot be silently downgraded to this binding.
	id.ControllerSerial = ""
	return engageControllerHash(id) == wanted
}

type engageRuntime struct {
	io       csrStageIO
	sequence byte
	events   [][]byte
}

func (r *engageRuntime) exchange(ctx context.Context, address, class, flags byte, body []byte) ([]byte, error) {
	if address == 0 || len(body) > 57 {
		return nil, errors.New("invalid Engage management request")
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
				return nil, errors.New("engage firmware event queue overflow")
			}
			r.events = append(r.events, reply)
			continue
		}
		if reply[0] != 0 || reply[1] != address || reply[2] != r.sequence || reply[3]&0xc0 != 0xc0 {
			continue
		}
		if reply[4] == 0xfe {
			return nil, fmt.Errorf("%w: class %02x", errEngageRejected, class)
		}
		if flags == 0x80 && reply[4] == 0xff && len(reply) == 10 && bytes.Equal(reply[5:10], packet[1:6]) {
			return nil, nil
		}
		if flags == 0x40 && len(body) == 1 && len(reply) >= 6 && reply[4] == class && reply[5] == body[0] {
			return reply[6:], nil
		}
	}
}
func (r *engageRuntime) read(ctx context.Context) ([]byte, error) {
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
			return nil, errors.New("malformed Engage management packet")
		}
		return append([]byte(nil), raw[1:n+1]...), nil
	}
}
func (r *engageRuntime) query(ctx context.Context, address, op byte) ([]byte, error) {
	return r.exchange(ctx, address, 2, 0x40, []byte{op})
}
func (r *engageRuntime) text(ctx context.Context, address, op byte) (string, error) {
	data, err := r.query(ctx, address, op)
	if err != nil {
		return "", err
	}
	return decodeExtendedIdentityString(data)
}

func (r *engageRuntime) serial(ctx context.Context, address byte) (string, error) {
	data, err := r.query(ctx, address, 1)
	if errors.Is(err, errEngageRejected) {
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
func (r *engageRuntime) variant(ctx context.Context, address byte) (string, error) {
	data, err := r.query(ctx, address, 2)
	if err != nil {
		return "", err
	}
	if len(data) < 3 || len(data) > 16 {
		return "", errors.New("invalid Engage variant")
	}
	return hex.EncodeToString(data[1:]), nil
}

func (r *engageRuntime) identify(ctx context.Context, device USBDevice) (engageIdentity, error) {
	id := engageIdentity{PID: device.ProductID, Port: device.SysPath}
	if !engageRuntimePID(id.PID) || device.attachment == nil {
		return id, errors.New("not a bound Engage 50 II runtime device")
	}
	id.Instance = device.attachment.fingerprint
	pids, err := r.query(ctx, 1, 0x11)
	if err != nil || len(pids) != 2 || (binary.LittleEndian.Uint16(pids) != id.PID && binary.LittleEndian.Uint16(pids) != 0x4050) {
		return id, errors.New("engage management PID does not match USB device")
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
		return id, errors.New("engage bootloader PID is unavailable")
	}
	id.BootPID = binary.LittleEndian.Uint16(boot)
	if id.BootPID != 0x4050 {
		return id, errors.New("unrecognized Engage bootloader identity")
	}
	protocols, err := r.query(ctx, 1, 0x14)
	if err != nil || !bytes.Contains(protocols, []byte{4}) {
		return id, errors.New("engage device does not report firmware protocol 4")
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

func (r *engageRuntime) nextEvent(ctx context.Context) ([]byte, error) {
	for {
		if len(r.events) > 0 {
			event := r.events[0]
			r.events = r.events[1:]
			return event, nil
		}
		packet, err := r.read(ctx)
		if err != nil {
			return nil, err
		}
		if packet[3]&0xc0 == 0 {
			return packet, nil
		}
	}
}

func (r *engageRuntime) activateController(ctx context.Context, id engageIdentity, wanted string) error {
	if id.ControllerAddress == 0 {
		return nil
	}
	if compareVersions(id.ControllerVersion, wanted) > 0 {
		return errors.New("controller firmware is newer than this package; controller downgrade is not implemented")
	}
	// Subscribe before issuing the activation command, retaining events that
	// arrive before its ACK. Only this device's controller can satisfy the wait.
	if _, err := r.exchange(ctx, 1, 13, 0, []byte{1, 5, 0}); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = r.exchange(cleanup, 1, 13, 0, []byte{2, 5, 0})
	}()
	if id.ControllerVersion == wanted {
		return nil
	}
	wait, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		event, err := r.nextEvent(wait)
		if err != nil {
			return fmt.Errorf("controller discovery: %w", err)
		}
		// Subscription mask 5 is not the device type. Type 2 identifies the
		// controller in the device-added event emitted for that subscription.
		if len(event) != 8 || event[0] != 0 || event[1] != 1 || event[4] != 13 || event[5] != 1 || event[6] != 2 {
			continue
		}
		if event[7] != id.ControllerAddress {
			return errors.New("a different controller appeared during firmware update")
		}
		break
	}
	serial, err := r.serial(ctx, id.ControllerAddress)
	if err != nil || id.ControllerSerial != "" && serial != id.ControllerSerial {
		return errors.New("controller was replaced before activation")
	}
	if _, err := r.exchange(ctx, 1, 7, 0x80, []byte{2}); err != nil {
		return fmt.Errorf("controller activation: %w", err)
	}
	update, cancelUpdate := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelUpdate()
	started := false
	for {
		event, err := r.nextEvent(update)
		if err != nil {
			return fmt.Errorf("controller update status: %w", err)
		}
		// The controller may disconnect while its own firmware restarts. It
		// must still return with the same serial/variant and requested version.
		if len(event) != 7 || event[0] != 0 || (event[1] != 1 && event[1] != id.ControllerAddress) || event[4] != 7 || event[5] != 1 {
			continue
		}
		switch event[6] {
		case 1, 2, 3, 4:
			started = true
		case 5:
			if started {
				goto verify
			}
		default:
			return fmt.Errorf("controller update failed or returned unknown status %d", event[6])
		}
	}
verify:
	for {
		version, err := r.text(update, id.ControllerAddress, 3)
		if engageDisconnect(err) {
			return err
		}
		if err == nil {
			serial, serialErr := r.serial(update, id.ControllerAddress)
			variant, variantErr := r.variant(update, id.ControllerAddress)
			if engageDisconnect(serialErr) {
				return serialErr
			}
			if engageDisconnect(variantErr) {
				return variantErr
			}
			if serialErr == nil && variantErr == nil {
				if (id.ControllerSerial != "" && serial != id.ControllerSerial) || variant != id.ControllerVariant {
					return errors.New("controller identity changed after activation")
				}
				if version != wanted {
					return errors.New("controller firmware version did not verify after activation")
				}
				return nil
			}
		}
		if err := waitDFU(update, 100*time.Millisecond); err != nil {
			return fmt.Errorf("controller did not return after activation: %w", err)
		}
	}
}
