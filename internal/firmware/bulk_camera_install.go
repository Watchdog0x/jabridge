package firmware

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

var errCameraUpdateFailed = errors.New("camera reported that its firmware update failed")

type cameraRecovery struct {
	RebootFrom string `json:"rebootFrom,omitempty"`
}

func validCameraRecovery(state firmwareRecoveryState) error {
	profile, ok := bulkCameraProfileForPID(state.RuntimePID)
	pids, err := parseTargetPIDs(state.TargetUSBPIDs)
	if !ok || state.Protocol != 18 || state.Camera == nil || err != nil || len(pids) != 1 || pids[0] != profile.ArchivePID || state.BootPID != profile.ArchivePID || state.USBPort == "" || state.ControllerIdentitySHA256 != "" {
		return errors.New("invalid camera recovery target")
	}
	for _, value := range []string{state.TargetIdentitySHA256, state.USBSerialSHA256} {
		if value == "" && value == state.USBSerialSHA256 {
			continue
		}
		data, err := hex.DecodeString(value)
		if err != nil || len(data) != sha256.Size {
			return errors.New("invalid camera recovery identity")
		}
	}
	if state.TargetIdentitySHA256 == "" {
		return errors.New("camera recovery serial identity is missing")
	}
	if state.Phase == "reboot-system" || state.Phase == "reboot-subsystem" {
		data, err := hex.DecodeString(state.Camera.RebootFrom)
		if err != nil || len(data) != sha256.Size {
			return errors.New("camera reboot attachment is missing")
		}
	} else if state.Camera.RebootFrom != "" {
		return errors.New("unexpected camera reboot attachment")
	}
	switch state.Phase {
	case "staging", "activating", "system", "reboot-system", "merging", "subsystem", "reboot-subsystem", "verifying", "failed":
	default:
		return errors.New("invalid camera recovery phase")
	}
	return nil
}

func bindCameraRecovery(state *firmwareRecoveryState, device USBDevice, id cameraIdentity, archive *bulkCameraArchive) error {
	if state == nil || archive == nil || device.VendorID != JabraVendorID || device.ViaDongle || !containsPID(archive.Profile.RuntimePIDs, device.ProductID) || device.attachment == nil || id.PID != device.ProductID || id.Port != device.SysPath || id.Serial == "" {
		return errors.New("camera update target is not bound")
	}
	serial := ""
	if device.Serial != "" {
		serial = fmt.Sprintf("%x", sha256.Sum256([]byte(device.Serial)))
	}
	port := filepath.Base(device.SysPath)
	if state.Protocol != 0 {
		if err := validCameraRecovery(*state); err != nil {
			return err
		}
		if state.RuntimePID != device.ProductID || state.USBPort != port || state.USBSerialSHA256 != serial || state.TargetIdentitySHA256 != id.hash() {
			return errors.New("camera recovery device or USB port changed")
		}
		return nil
	}
	state.Protocol, state.RuntimePID, state.BootPID = 18, device.ProductID, archive.Profile.ArchivePID
	state.USBPort, state.USBSerialSHA256, state.TargetIdentitySHA256 = port, serial, id.hash()
	state.Phase = "staging"
	state.Camera = &cameraRecovery{}
	return validCameraRecovery(*state)
}

// Keep the first reboot, library merge and optional subsystem update separate.
// A new main version after reboot alone is not evidence that the update ended.
// Persist activation intent BEFORE the command: recovery never sends it twice
// unless the device explicitly reported failure and the user retries.
func runCameraInstall(ctx context.Context, backend cameraInstallBackend, device USBDevice, path string, archive *bulkCameraArchive, state *firmwareRecoveryState, save func() error, progress func(string, int64, int64)) error {
	if backend == nil || archive == nil || state == nil || save == nil {
		return errors.New("incomplete camera installation plan")
	}
	control, err := backend.Open(ctx, device)
	if err != nil {
		return err
	}
	defer func() {
		if control != nil {
			_ = control.Close()
		}
	}()
	if err := bindCameraRecovery(state, device, control.Identity(), archive); err != nil {
		return err
	}
	checkpoint := func(phase string) error {
		state.Phase = phase
		state.Camera.RebootFrom = ""
		if phase == "reboot-system" || phase == "reboot-subsystem" {
			state.Camera.RebootFrom = device.attachment.fingerprint
		}
		return save()
	}
	report := func(stage string, done, total int64) {
		if progress != nil {
			progress(stage, done, total)
		}
	}
	if state.Phase == "staging" || state.Phase == "failed" {
		if compareVersions(control.Identity().Version, archive.Manifest.Version) > 0 {
			return errors.New("camera firmware is newer than this file; refusing a downgrade")
		}
		if err := checkpoint("staging"); err != nil {
			return err
		}
		if err := backend.Stage(ctx, device, path, archive, func(done, total int64) { report("Sending firmware", done, total) }); err != nil {
			return err
		}
		if err := control.Subscribe(ctx); err != nil {
			return err
		}
		// Discard pre-activation status from the previous operation. Retain
		// events received while waiting for the activation acknowledgement.
		drain, cancel := context.WithTimeout(ctx, time.Second)
		for {
			_, err := control.Next(drain)
			if err != nil {
				cancel()
				if !errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				break
			}
		}
		if err := checkpoint("activating"); err != nil {
			return err
		}
		if err := control.Start(ctx); err != nil {
			if errors.Is(err, errCameraCommandNotStarted) || errors.Is(err, errSitelRejected) {
				if saveErr := checkpoint("staging"); saveErr != nil {
					return errors.Join(err, saveErr)
				}
				return fmt.Errorf("camera activation was not started; retry this file: %w", err)
			}
			return fmt.Errorf("camera activation reply was lost; retry this file to monitor the existing update: %w", err)
		}
		if err := checkpoint("system"); err != nil {
			return err
		}
	} else if err := control.Subscribe(ctx); err != nil {
		return err
	}
	for {
		switch state.Phase {
		case "activating", "system", "merging", "subsystem":
			phase := state.Phase
			timeout := 6 * time.Minute
			if phase == "system" || phase == "activating" {
				timeout = 10 * time.Minute
			}
			report(phase, 0, 0)
			wait, cancel := context.WithTimeout(ctx, timeout)
			next, err := waitCameraStage(wait, control, phase, func(value int) { report(phase, int64(value), 100) })
			cancel()
			if errors.Is(err, errCameraUpdateFailed) {
				if saveErr := checkpoint("failed"); saveErr != nil {
					return errors.Join(err, saveErr)
				}
				return err
			}
			if err != nil {
				return fmt.Errorf("camera %s is not confirmed complete; keep it connected and retry this file: %w", phase, err)
			}
			if err := checkpoint(next); err != nil {
				return err
			}
		case "reboot-system", "reboot-subsystem":
			phase := state.Phase
			report("Waiting for camera restart", 0, 0)
			_ = control.Close()
			control = nil
			// On process restart the recorded old attachment may already be
			// gone. Do not wait for a second reboot that the camera never owes.
			if device.attachment.fingerprint == state.Camera.RebootFrom {
				device, err = backend.Reboot(ctx, device)
				if err != nil {
					return err
				}
			}
			control, err = backend.Open(ctx, device)
			if err != nil {
				return err
			}
			if err := bindCameraRecovery(state, device, control.Identity(), archive); err != nil {
				return err
			}
			if phase == "reboot-subsystem" {
				if err := checkpoint("verifying"); err != nil {
					return err
				}
			} else {
				if err := checkpoint("merging"); err != nil {
					return err
				}
				if err := control.Subscribe(ctx); err != nil {
					return err
				}
			}
		case "verifying":
			// Open again to read a fresh version, including the no-reboot DONE
			// path; never use a version cached before activation.
			_ = control.Close()
			control = nil
			control, err = backend.Open(ctx, device)
			if err != nil {
				return err
			}
			if err := bindCameraRecovery(state, device, control.Identity(), archive); err != nil {
				return err
			}
			if compareVersions(control.Identity().Version, archive.Manifest.Version) != 0 {
				return errors.New("camera did not report the expected firmware version after updating")
			}
			return nil
		default:
			return errors.New("unknown camera update phase")
		}
	}
}

func waitCameraStage(ctx context.Context, control cameraControl, phase string, progress func(int)) (string, error) {
	for {
		event, err := control.Next(ctx)
		if err != nil {
			return "", err
		}
		if event.State == 20 {
			return "", errCameraUpdateFailed
		}
		if event.State == 16 {
			return "verifying", nil
		}
		if event.State == 26 {
			if phase == "subsystem" || phase == "merging" {
				return "reboot-subsystem", nil
			}
			return "reboot-system", nil
		}
		if event.Progress >= 0 {
			if progress != nil {
				progress(event.Progress)
			}
			if phase == "merging" {
				return "subsystem", nil
			}
		}
	}
}

func verifyCameraRelease(ctx context.Context, path string, archive *bulkCameraArchive, pid uint16) error {
	if archive == nil || !containsPID(archive.Profile.RuntimePIDs, pid) {
		return errors.New("camera firmware does not match the selected model")
	}
	checksum, err := firmwareFileMD5(path)
	if err != nil {
		return err
	}
	if checksum != base64.StdEncoding.EncodeToString(archive.MD5[:]) {
		return errors.New("camera archive checksum changed after validation")
	}
	evidence, err := firmwareModelCatalog.FirmwareRelease(ctx, pid, archive.Manifest.Version)
	if err != nil {
		return err
	}
	if !firmwareReleaseMatchesDevice(checksum, pid, evidence) || evidence.HasUnspecifiedFirmwareProtocol || len(evidence.FirmwareProtocols) != 1 || evidence.FirmwareProtocols[0] != 18 {
		return errors.New("camera firmware requires matching official protocol-18 metadata and checksum")
	}
	return nil
}

func installBulkCameraChecked(snapshot *firmwareSnapshot, accepted bool, validateTarget func() error, preferredPID uint16) error {
	archive, err := loadBulkCameraArchive(snapshot.path)
	if err != nil {
		return err
	}
	devices, err := enumerateBoundUSB()
	if err != nil {
		return err
	}
	var matches []USBDevice
	for _, d := range devices {
		if d.VendorID == JabraVendorID && !d.ViaDongle && containsPID(archive.Profile.RuntimePIDs, d.ProductID) && (preferredPID == 0 || preferredPID == d.ProductID) {
			matches = append(matches, d)
		}
	}
	if len(matches) != 1 {
		return fmt.Errorf("connect exactly one matching %s by USB; found %d", archive.Profile.Name, len(matches))
	}
	device := matches[0]
	prepared, err := prepareFirmwareTransfer(snapshot.path, archive.Manifest)
	if err != nil {
		return err
	}
	state := prepared.State
	if prepared.Recovery {
		previous, err := loadFirmwareRecoveryState()
		if err != nil {
			return err
		}
		if err := validCameraRecovery(previous); err != nil {
			return err
		}
		previous.Attempt = state.Attempt
		state = previous
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	metadata, finish := context.WithTimeout(ctx, MetadataTimeout)
	err = verifyCameraRelease(metadata, snapshot.path, archive, device.ProductID)
	finish()
	if err != nil {
		return err
	}
	if !accepted {
		word := "INSTALL"
		if prepared.Recovery {
			word = "RECOVER"
		}
		fmt.Fprintf(os.Stderr, "Firmware: %s %s\nSelected USB: 0b0e:%04x\nKeep USB and power connected. End calls and close other Jabra tools first.\n", archive.Profile.Name, archive.Manifest.Version, device.ProductID)
		if !confirmFirmwareAction(os.Stdin, os.Stderr, word) {
			return errors.New("camera firmware install cancelled")
		}
	}
	if validateTarget != nil {
		if err := validateTarget(); err != nil {
			return err
		}
	}
	commandLineRiskAccepted.Store(true)
	defer commandLineRiskAccepted.Store(false)
	if err := requireHardwareWrites(); err != nil {
		return err
	}
	last := ""
	err = runCameraInstall(ctx, nativeCameraBackend{}, device, snapshot.path, archive, &state, func() error { return saveFirmwareRecoveryState(state) }, func(stage string, done, total int64) {
		text := stage
		if total > 0 {
			text = fmt.Sprintf("%s: %d%%", stage, done*100/total)
		}
		if text != last {
			fmt.Fprintln(os.Stderr, text)
			last = text
		}
	})
	if err != nil {
		return fmt.Errorf("camera update stopped; keep this file and USB port for recovery: %w", err)
	}
	if err := clearFirmwareRecoveryState(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Camera firmware %s installed and confirmed on the same device.\n", archive.Manifest.Version)
	return nil
}
