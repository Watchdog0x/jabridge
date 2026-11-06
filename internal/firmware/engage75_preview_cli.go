package firmware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func installEngage75PreviewChecked(snapshot *firmwareSnapshot, accepted bool, validateTarget func() error, preferredPID uint16) error {
	if Engage75Compatibility != "enabled" || !accepted {
		return errors.New("engage 75 preview requires an enabled trial build and the explicit firmware-risk flag")
	}
	if err := checkEngage75PreviewStart(); err != nil {
		return err
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
	var matches []USBDevice
	for _, device := range devices {
		if device.VendorID == JabraVendorID && device.ProductID == 0x1113 && !device.ViaDongle && (preferredPID == 0 || preferredPID == device.ProductID) {
			matches = append(matches, device)
		}
	}
	if len(matches) != 1 {
		return fmt.Errorf("engage 75 preview needs one directly connected 1113 base; found %d", len(matches))
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
	fmt.Fprintln(os.Stderr, "Engage 75 compatibility preview. Leave the headset docked and base connected.\nThis changes firmware modes. A failed attempt may leave the device in update mode. Automatic retry is disabled.")
	lastTarget, lastPercent := byte(0), -1
	err = runEngage75Preview(ctx, nativeSitelDECTBackend{}, matches[0], archive, snapshot.digest, engage75PreviewStore{pending: saveEngage75Pending, recovery: saveFirmwareRecoveryState}, func(target byte, done, total int) {
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
