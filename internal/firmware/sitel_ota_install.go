package firmware

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type sitelOTARecovery struct {
	ParentPID            uint16 `json:"parentPid"`
	ParentIdentitySHA256 string `json:"parentIdentitySha256"`
	Region               byte   `json:"region"`
}

func validSitelOTARecovery(state firmwareRecoveryState) error {
	part := state.SitelOTA
	pids, err := parseTargetPIDs(state.TargetUSBPIDs)
	if state.Protocol != 12 || part == nil || err != nil || len(pids) != 1 || pids[0] != sitelOTAImagePID || state.BootPID != sitelOTAImagePID || !SupportsWirelessFirmware(part.ParentPID, state.RuntimePID) || part.Region == 0 || state.USBPort == "" || state.ControllerIdentitySHA256 != "" {
		return errors.New("invalid wireless Engage recovery target")
	}
	for _, value := range []string{part.ParentIdentitySHA256, state.TargetIdentitySHA256} {
		data, err := hex.DecodeString(value)
		if err != nil || len(data) != 32 {
			return errors.New("wireless Engage recovery identity is missing or invalid")
		}
	}
	switch state.Phase {
	case "entering-wireless", "flashing-wireless", "leaving-wireless", "verifying-wireless":
	default:
		return errors.New("invalid wireless Engage recovery phase")
	}
	return nil
}

func bindSitelOTATarget(state *firmwareRecoveryState, target sitelOTATarget, archive *sitelOTAArchive) error {
	if state == nil || archive == nil || archive.Manifest == nil || target.Parent.attachment == nil || target.ParentIdentity == "" || target.childIdentity() == "" || !SupportsWirelessFirmware(target.Parent.ProductID, target.Child.PID) || target.Child.BootPID != sitelOTAImagePID || target.Child.Address != 4 || target.Parent.ViaDongle || target.Parent.VendorID != JabraVendorID {
		return errors.New("incomplete wireless Engage target")
	}
	if target.Region != archive.Region {
		return errors.New("sound-prompt region does not match the wireless headset")
	}
	port := filepath.Base(target.Parent.SysPath)
	if state.Protocol != 0 {
		if err := validSitelOTARecovery(*state); err != nil {
			return err
		}
		if state.RuntimePID != target.Child.PID || state.TargetIdentitySHA256 != target.childIdentity() || state.USBPort != port || state.SitelOTA.ParentPID != target.Parent.ProductID || state.SitelOTA.ParentIdentitySHA256 != target.ParentIdentity || state.SitelOTA.Region != target.Region {
			return errors.New("wireless Engage headset, adapter or USB port changed")
		}
		return nil
	}
	state.Protocol, state.RuntimePID, state.BootPID = 12, target.Child.PID, sitelOTAImagePID
	state.TargetIdentitySHA256, state.USBPort = target.childIdentity(), port
	state.SitelOTA = &sitelOTARecovery{ParentPID: target.Parent.ProductID, ParentIdentitySHA256: target.ParentIdentity, Region: target.Region}
	state.Phase = "entering-wireless"
	return validSitelOTARecovery(*state)
}

func runSitelOTAInstall(ctx context.Context, backend sitelOTABackend, selected sitelOTATarget, archive *sitelOTAArchive, state *firmwareRecoveryState, save func() error, progress func(byte, int, int)) error {
	if backend == nil || save == nil || state == nil || archive == nil || archive.Manifest == nil || state.ArchiveSHA256 == "" {
		return errors.New("incomplete wireless Engage installation plan")
	}
	if _, err := validateSitelOTAImages(archive.Images, archive.Manifest.Version); err != nil {
		return err
	}
	target, err := backend.inspect(ctx, selected.Parent)
	if err != nil {
		return err
	}
	if target.childIdentity() != selected.childIdentity() || target.ParentIdentity != selected.ParentIdentity || target.Parent.attachment == nil || selected.Parent.attachment == nil || target.Parent.attachment.fingerprint != selected.Parent.attachment.fingerprint {
		return errors.New("wireless Engage selection changed before installation")
	}
	recovery := state.Protocol == 12
	if err := bindSitelOTATarget(state, target, archive); err != nil {
		return err
	}
	checkpoint := func(phase string) error { state.Phase = phase; return save() }
	if state.Phase != "verifying-wireless" {
		conditions, err := backend.conditions(ctx, target)
		if err != nil {
			return err
		}
		if conditions != 0 && (conditions != 4 || !recovery) {
			return fmt.Errorf("wireless update is not ready (status 0x%02x); charge the headset and end calls first", conditions)
		}
		if state.Phase != "leaving-wireless" {
			if compareVersions(target.Child.Version, archive.Manifest.Version) > 0 || compareVersions(target.TunesVersion, archive.Images[1].File.Version) > 0 {
				return errors.New("wireless headset firmware is newer than this package; refusing a downgrade")
			}
			if conditions == 0 {
				if err := checkpoint("entering-wireless"); err != nil {
					return err
				}
				if err := backend.enter(ctx, target); err != nil {
					return err
				}
			}
			if err := checkpoint("flashing-wireless"); err != nil {
				return err
			}
			if err := transferSitelOTA(ctx, backend, target, archive, progress); err != nil {
				return err
			}
			if err := checkpoint("leaving-wireless"); err != nil {
				return err
			}
			conditions = 4
		}
		if conditions == 4 {
			if err := backend.exit(ctx, target); err != nil {
				return err
			}
		}
		if err := checkpoint("verifying-wireless"); err != nil {
			return err
		}
	}
	verify, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		observed, err := backend.inspect(verify, target.Parent)
		if err == nil {
			if err := bindSitelOTATarget(state, observed, archive); err != nil {
				return err
			}
			if observed.Child.Version == archive.Manifest.Version && observed.TunesVersion == archive.Images[1].File.Version {
				return nil
			}
		}
		if waitErr := backend.wait(verify, 250*time.Millisecond); waitErr != nil {
			return fmt.Errorf("wireless Engage firmware or sound-prompt version did not verify; retry with the same file and adapter: %w", waitErr)
		}
	}
}

func transferSitelOTA(ctx context.Context, backend sitelOTABackend, target sitelOTATarget, archive *sitelOTAArchive, progress func(byte, int, int)) error {
	peer, closeConnection, err := backend.channel(ctx, target)
	if err != nil {
		return err
	}
	defer func() { _ = closeConnection() }()
	info, images, err := prepareSitelOTATransfer(ctx, peer, archive)
	if err != nil {
		return err
	}
	for _, image := range images {
		if err := transferSitelImage(ctx, peer, image, info, func(done, total int) {
			if progress != nil {
				progress(image.Target, done, total)
			}
		}); err != nil {
			return err
		}
	}
	for _, image := range images {
		if err := verifySitelImage(ctx, peer, image, info.SectorSize); err != nil {
			return err
		}
	}
	return nil
}

func verifySitelOTARelease(ctx context.Context, path string, archive *sitelOTAArchive, pid uint16) error {
	if archive == nil || archive.Manifest == nil || !sitelOTAChildPID(pid) {
		return errors.New("invalid wireless Engage release target")
	}
	evidence, err := firmwareModelCatalog.FirmwareRelease(ctx, pid, archive.Manifest.Version)
	if err != nil {
		return err
	}
	digest, err := firmwareFileMD5(path)
	if err != nil {
		return err
	}
	if !firmwareReleaseMatchesDevice(digest, pid, evidence) || !containsPID(evidence.CompatiblePIDs, sitelOTAImagePID) || evidence.HasUnspecifiedFirmwareProtocol || len(evidence.FirmwareProtocols) != 1 || evidence.FirmwareProtocols[0] != 12 {
		return errors.New("wireless Engage requires matching official protocol-12 metadata and checksum")
	}
	return nil
}

func installSitelOTAChecked(snapshot *firmwareSnapshot, accepted bool, validateTarget func() error, preferredPID uint16, selection *WirelessFirmwareSelection) error {
	archive, err := loadSitelOTAArchive(snapshot.path)
	if err != nil {
		return err
	}
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
		if err := validSitelOTARecovery(previous); err != nil {
			return err
		}
		previous.Attempt = state.Attempt
		state = previous
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	devices, err := enumerateBoundUSB()
	if err != nil {
		return err
	}
	backend := nativeSitelOTABackend{}
	var candidates []sitelOTATarget
	var failures []error
	for _, parent := range devices {
		if !sitelOTAParentPID(parent.ProductID) || selection != nil && parent.ProductID != selection.ParentPID {
			continue
		}
		if prepared.Recovery && (parent.ProductID != state.SitelOTA.ParentPID || filepath.Base(parent.SysPath) != state.USBPort) {
			continue
		}
		target, err := backend.inspect(ctx, parent)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if preferredPID != 0 && target.Child.PID != preferredPID {
			continue
		}
		if selection != nil && (target.childIdentity() != selection.ChildIdentity || parent.attachment.fingerprint != selection.ParentAttachment) {
			return errors.New("selected wireless headset or adapter changed")
		}
		candidates = append(candidates, target)
	}
	if len(candidates) != 1 {
		return errors.Join(append([]error{fmt.Errorf("need one matching wireless Engage headset through its Link 400 or base; found %d", len(candidates))}, failures...)...)
	}
	target := candidates[0]
	metadata, finish := context.WithTimeout(ctx, MetadataTimeout)
	err = verifySitelOTARelease(metadata, snapshot.path, archive, target.Child.PID)
	finish()
	if err != nil {
		return err
	}
	// Validate a copy now. The running state must stay at protocol zero for a
	// fresh install so another application's active FUOTA session is rejected.
	preview := state
	if err := bindSitelOTATarget(&preview, target, archive); err != nil {
		return err
	}
	if !accepted && state.Phase != "verifying-wireless" {
		word := "INSTALL"
		if prepared.Recovery {
			word = "RECOVER"
		}
		fmt.Fprintf(os.Stderr, "Firmware: %s %s\nHeadset: 0b0e:%04x through USB 0b0e:%04x\nKeep both devices powered and connected. End calls and close other Jabra tools first.\n", archive.Manifest.ProductName, archive.Manifest.Version, target.Child.PID, target.Parent.ProductID)
		if !confirmFirmwareAction(os.Stdin, os.Stderr, word) {
			return errors.New("wireless Engage firmware install cancelled")
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
	lastTarget, lastPercent := byte(0), -1
	err = runSitelOTAInstall(ctx, backend, target, archive, &state, func() error { return saveFirmwareRecoveryState(state) }, func(target byte, done, total int) {
		percent := done * 100 / total
		if target != lastTarget || percent != lastPercent {
			fmt.Fprintf(os.Stderr, "\rWireless firmware target %d: %3d%%", target, percent)
			lastTarget, lastPercent = target, percent
			if done == total {
				fmt.Fprintln(os.Stderr)
			}
		}
	})
	if err != nil {
		return fmt.Errorf("wireless Engage update stopped; keep the same archive, headset and adapter for recovery: %w", err)
	}
	if err := clearFirmwareRecoveryState(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wireless Engage firmware %s and sound prompts %s installed and read back from the same headset.\n", archive.Manifest.Version, archive.Images[1].File.Version)
	return nil
}
