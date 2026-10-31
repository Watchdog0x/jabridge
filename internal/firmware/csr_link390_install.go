package firmware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func flashBoundLink390CSR7(snapshot *firmwareSnapshot, device USBDevice, prepared firmwareTransferPreparation, previous firmwareRecoveryState) error {
	if err := requireHardwareWrites(); err != nil {
		return err
	}
	if !link390FirmwareEnabled {
		return errors.New("untested Link 390 firmware path is not enabled")
	}
	if !isLink390PID(device.ProductID) {
		return errors.New("selected firmware target is not a Link 390")
	}
	image, err := loadLink390CSR7Archive(snapshot.path)
	if err != nil {
		return err
	}
	phase, expected := "", ""
	if prepared.Recovery {
		if previous.Protocol != 7 || validLink390CSR7Recovery(previous) != nil {
			return errors.New("the Link 390 recovery identity or phase is missing; refusing to replay writes")
		}
		phase, expected = previous.Phase, previous.TargetIdentitySHA256
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	wanted := fmt.Sprintf("%d.%d.%d", image.Version[0], image.Version[1], image.Version[2])
	backend := &nativeLink390CSR7Backend{device: device, version: wanted}
	fmt.Fprintln(os.Stderr, "WARNING: untested Link 390 firmware path. Physical installation and recovery are not yet qualified.")
	lastPercent := -1
	err = runLink390CSR7Update(ctx, backend, image, phase, expected, func(identity csrExtendedIdentity, phase string) error {
		prepared.State.Protocol = 7
		prepared.State.RuntimePID = identity.PID
		prepared.State.TargetIdentitySHA256 = extendedRecoveryIdentity(identity)
		prepared.State.Language = image.Language
		prepared.State.Phase = phase
		return saveFirmwareRecoveryState(prepared.State)
	}, func(sent, total uint32) {
		percent := int(uint64(sent) * 100 / uint64(total))
		if percent != lastPercent {
			fmt.Fprintf(os.Stderr, "\rFirmware: %3d%%", percent)
			lastPercent = percent
			if sent == total {
				fmt.Fprintln(os.Stderr)
			}
		}
	})
	if err == nil {
		fmt.Fprintf(os.Stderr, "Firmware %s read back from the same Link 390.\n", wanted)
	}
	return err
}

func checkLink390FirmwareOptIn(protocol int, manifest *BuildVector, pid uint16) error {
	if protocol != 7 || !isLink390BinaryManifest(manifest) {
		return nil
	}
	if !isLink390PID(pid) {
		return errors.New("the Link 390 monolithic firmware does not match this device")
	}
	if !link390FirmwareEnabled {
		return fmt.Errorf("this Link 390 firmware format is experimental and off by default; a test build requires %s=%s", link390FirmwareEnv, link390FirmwareAck)
	}
	return nil
}

func isLink390CSR7Recovery(state firmwareRecoveryState) bool {
	return state.Protocol == 7 && strings.HasPrefix(state.Phase, "csr7-")
}
