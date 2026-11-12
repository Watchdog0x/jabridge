package firmware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func installEngage75PreviewChecked(snapshot *firmwareSnapshot, accepted bool, validateTarget func() error, preferredPID uint16, newAttempt bool) error {
	if Engage75Compatibility != "enabled" || !accepted {
		return errors.New("engage 75 preview requires an enabled trial build and the explicit firmware-risk flag")
	}
	previous, err := loadEngage75PreviewStart(newAttempt)
	if err != nil {
		return err
	}
	if previous != nil && previous.ArchiveSHA256 != snapshot.digest {
		return errors.New("the new attempt needs the exact original firmware archive")
	}
	archive, err := loadSitelDECTArchive(snapshot.path)
	if err != nil {
		return err
	}
	if archive.Engage75 == nil || archive.Manifest.ProductName != "Jabra_Engage_75" || archive.Manifest.Version != "5.20.1" {
		return errors.New("engage 75 preview supports only its original 5.20.1 package")
	}
	devices, err := enumerateBoundUSB()
	if err != nil {
		return err
	}
	device, err := selectEngage75PreviewDevice(devices, previous, newAttempt, preferredPID)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	metadata, finish := context.WithTimeout(ctx, MetadataTimeout)
	err = verifySitelDECTRelease(metadata, snapshot.path, archive, 0x1113)
	finish()
	if err != nil {
		return err
	}
	if validateTarget != nil {
		if err := validateTarget(); err != nil {
			return err
		}
	}
	commandLineRiskAccepted.Store(true)
	defer commandLineRiskAccepted.Store(false)
	fmt.Fprintln(os.Stderr, "Engage 75 firmware test. Keep the headset docked and base connected.\nThe base restarts and reconnects during the update. Saved attempts are checked before a new attempt.")
	lastTarget, lastPercent := byte(0), -1
	err = runEngage75Preview(ctx, nativeSitelDECTBackend{}, device, archive, snapshot.digest, engage75PreviewStore{pending: func(p engage75Pending) error {
		if err := saveEngage75Pending(p); err != nil {
			return err
		}
		if p.Phase == "bound-boot-attachment" && p.BootSerial == "absent" {
			fmt.Fprintln(os.Stderr, "Base reconnected in update mode without a USB serial. Checking its firmware image IDs.")
		}
		return nil
	}, recovery: saveFirmwareRecoveryState, previous: previous, supersede: supersedeEngage75Pending}, func(target byte, done, total int) {
		percent := done * 100 / total
		if target != lastTarget || percent != lastPercent {
			fmt.Fprintf(os.Stderr, "\r%s: %3d%%", dectComponentLabel(target), percent)
			lastTarget, lastPercent = target, percent
			if done == total {
				fmt.Fprintln(os.Stderr)
			}
		}
	})
	if err != nil {
		return err
	}
	// Both records say final-verified before either file is removed. A crash
	// during cleanup leaves an explicit record, never permission to replay entry.
	if err := clearFirmwareRecoveryState(); err != nil {
		return err
	}
	if err := clearEngage75Pending(); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Engage 75 firmware installed. All component versions and selected identities verified.")
	return nil
}

func selectEngage75PreviewDevice(devices []USBDevice, previous *engage75Pending, newAttempt bool, preferredPID uint16) (USBDevice, error) {
	var bases []USBDevice
	for _, device := range devices {
		if device.VendorID == JabraVendorID && !device.ViaDongle && (engage75Profile.runtime(device.ProductID) || device.ProductID == engage75Profile.BootPID) {
			bases = append(bases, device)
		}
	}
	if len(bases) != 1 {
		return USBDevice{}, fmt.Errorf("connect only one Engage 75 base for this test; found %d", len(bases))
	}
	device := bases[0]
	if device.ProductID == engage75Profile.BootPID {
		if newAttempt && previous != nil && filepath.Base(device.SysPath) == previous.Selected.USBPort {
			return USBDevice{}, errors.New("the base is still in update mode; switch its power off for a few minutes, reconnect it and wait for its normal screen before a new attempt")
		}
		return USBDevice{}, errors.New("the base is not in normal mode; keep the existing result for checking")
	}
	if device.ProductID != 0x1113 || preferredPID != 0 && preferredPID != device.ProductID {
		return USBDevice{}, errors.New("this test needs the original directly connected 1113 base")
	}
	return device, nil
}
