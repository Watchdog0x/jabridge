package firmware

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func engageControllerHash(id sitelIdentity) string {
	return extendedRecoveryIdentity(csrExtendedIdentity{PID: id.PID, Port: id.Port, Serial: id.ControllerSerial, Variant: id.ControllerVariant})
}

func engageControllerMatches(wanted string, id sitelIdentity) bool {
	if engageControllerHash(id) == wanted {
		return true
	}
	// An originally serial-less controller stays bound by parent port and
	// variant even if newer firmware later exposes a serial. A previously
	// recorded nonempty serial cannot be silently downgraded to this binding.
	id.ControllerSerial = ""
	return engageControllerHash(id) == wanted
}

func (r *sitelRuntime) nextEvent(ctx context.Context) ([]byte, error) {
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

func (r *sitelRuntime) activateController(ctx context.Context, id sitelIdentity, wanted string) error {
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
		if sitelDisconnect(err) {
			return err
		}
		if err == nil {
			serial, serialErr := r.serial(update, id.ControllerAddress)
			variant, variantErr := r.variant(update, id.ControllerAddress)
			if sitelDisconnect(serialErr) {
				return serialErr
			}
			if sitelDisconnect(variantErr) {
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

// A controller restart may re-enumerate the combined USB device. Re-open only
// the original port/model and verify both identities and versions. Never replay
// the activation command merely because its final reply was lost.
func verifyEngageControllerReattach(ctx context.Context, backend sitelInstallBackend, previous USBDevice, state *firmwareRecoveryState, wanted string) error {
	wait, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	device, err := waitSitelDevice(wait, backend, previous, state.RuntimePID)
	if err != nil {
		return err
	}
	for {
		_, id, closeConnection, err := backend.runtime(wait, device)
		if err == nil {
			_ = closeConnection()
			if sitelIdentityHash(id) != state.TargetIdentitySHA256 || !engageControllerMatches(state.ControllerIdentitySHA256, id) {
				return errors.New("controller reconnect returned a different device")
			}
			if id.Version != wanted {
				return errors.New("headset version changed during controller activation")
			}
			if id.ControllerVersion == wanted {
				return nil
			}
		}
		if wait.Err() != nil {
			return fmt.Errorf("controller did not return with firmware %s: %w", wanted, wait.Err())
		}
		if err != nil {
			retry, stop := context.WithTimeout(wait, time.Second)
			next, changed := waitSitelDevice(retry, backend, device, state.RuntimePID)
			stop()
			if changed == nil {
				device = next
			}
		}
		if err := waitDFU(wait, 200*time.Millisecond); err != nil {
			return err
		}
	}
}

func engageHasController(pid uint16) bool {
	return pid >= 0x4001 && pid <= 0x4004 || pid >= 0x4051 && pid <= 0x4054 || pid >= 0x4061 && pid <= 0x4064
}
