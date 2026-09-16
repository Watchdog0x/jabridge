package firmware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type conexantRecovery struct {
	Plus             bool                `json:"plus"`
	DescriptorSHA256 string              `json:"descriptorSha256"`
	Calibration      conexantCalibration `json:"calibration"`
}

func validConexantRecovery(state firmwareRecoveryState) error {
	part := state.Conexant
	pids, err := parseTargetPIDs(state.TargetUSBPIDs)
	if state.Protocol != 5 || part == nil || err != nil || len(pids) != 1 || !conexantPID(state.RuntimePID) || pids[0] != state.RuntimePID || state.USBPort == "" || state.BootPID != 0 || state.ControllerIdentitySHA256 != "" || (state.Phase != "flashing" && state.Phase != "verifying") {
		return errors.New("invalid UC Voice recovery target")
	}
	for _, digest := range []string{part.DescriptorSHA256, state.USBSerialSHA256, part.Calibration.SHA256} {
		if digest == "" {
			continue
		}
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size {
			return errors.New("invalid UC Voice recovery digest")
		}
	}
	cal := part.Calibration
	if part.DescriptorSHA256 == "" || cal.Size < 0 || cal.Size > 256 || uint64(cal.Start)+uint64(cal.Size) > 1<<16 || cal.Size == 0 && (cal.Start != 0 || cal.SHA256 != "") || cal.Size != 0 && cal.SHA256 == "" {
		return errors.New("invalid UC Voice calibration record")
	}
	return nil
}

func verifyConexantRelease(ctx context.Context, path string, image *conexantImage, pid uint16) error {
	if image == nil || image.Manifest == nil || !conexantPID(pid) {
		return errors.New("invalid UC Voice release target")
	}
	pids, err := parseTargetPIDs(image.Manifest.TargetUSBPIDs)
	if err != nil || len(pids) != 1 || pids[0] != pid {
		return errors.New("UC Voice firmware does not match the selected model")
	}
	evidence, err := firmwareModelCatalog.FirmwareRelease(ctx, pid, image.Manifest.Version)
	if err != nil {
		return err
	}
	checksum, err := firmwareFileMD5(path)
	if err != nil {
		return err
	}
	if !firmwareReleaseMatchesDevice(checksum, pid, evidence) || evidence.HasUnspecifiedFirmwareProtocol || len(evidence.FirmwareProtocols) != 1 || evidence.FirmwareProtocols[0] != 5 {
		return errors.New("UC Voice firmware requires matching official protocol-5 metadata and checksum")
	}
	return nil
}

func bindConexantRecovery(state *firmwareRecoveryState, device USBDevice, part conexantRecovery) error {
	if state == nil || device.attachment == nil || device.VendorID != JabraVendorID || device.ViaDongle || !conexantPID(device.ProductID) {
		return errors.New("UC Voice update target is not bound")
	}
	serial := ""
	if device.Serial != "" {
		serial = fmt.Sprintf("%x", sha256.Sum256([]byte(device.Serial)))
	}
	port := filepath.Base(device.SysPath)
	if state.Conexant != nil {
		if err := validConexantRecovery(*state); err != nil {
			return err
		}
		if state.RuntimePID != device.ProductID || state.USBPort != port || state.USBSerialSHA256 != serial || state.Conexant.Plus != part.Plus || state.Conexant.DescriptorSHA256 != part.DescriptorSHA256 {
			return errors.New("UC Voice recovery device, USB port or HID layout changed")
		}
		return nil
	}
	if state.Protocol != 0 {
		return errors.New("unfinished transfer uses a different firmware protocol")
	}
	state.Protocol, state.RuntimePID, state.USBPort, state.USBSerialSHA256 = 5, device.ProductID, port, serial
	state.Phase = "flashing"
	state.Conexant = &part
	return validConexantRecovery(*state)
}

func installConexantChecked(snapshot *firmwareSnapshot, accepted bool, validateTarget func() error, preferredPID uint16) error {
	image, err := loadConexantImage(snapshot.path)
	if err != nil {
		return err
	}
	pids, err := parseTargetPIDs(image.Manifest.TargetUSBPIDs)
	if err != nil {
		return err
	}
	if preferredPID != 0 && preferredPID != pids[0] {
		return errors.New("UC Voice firmware does not match the selected device")
	}
	devices, err := enumerateBoundUSB()
	if err != nil {
		return err
	}
	device, err := selectInteractiveUSBTarget(devices, pids[0])
	if err != nil {
		return err
	}
	prepared, err := prepareFirmwareTransfer(snapshot.path, image.Manifest)
	if err != nil {
		return err
	}
	state := prepared.State
	if prepared.Recovery {
		previous, err := loadFirmwareRecoveryState()
		if err != nil {
			return err
		}
		if err := validConexantRecovery(previous); err != nil {
			return err
		}
		previous.Attempt = state.Attempt
		state = previous
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	metadata, finish := context.WithTimeout(ctx, MetadataTimeout)
	err = verifyConexantRelease(metadata, snapshot.path, image, device.ProductID)
	finish()
	if err != nil {
		return err
	}
	hid, err := openConexantHID(device)
	if err != nil {
		return err
	}
	defer func() { _ = hid.raw.file.Close() }()
	client := hid.client()
	part := conexantRecovery{Plus: client.plus, DescriptorSHA256: hid.descriptorSHA}
	if !prepared.Recovery {
		part.Calibration, err = client.captureCalibration(ctx)
		if err != nil {
			return fmt.Errorf("read UC Voice calibration before update: %w", err)
		}
	}
	if err := bindConexantRecovery(&state, device, part); err != nil {
		return err
	}
	if err := client.verifyCalibration(ctx, state.Conexant.Calibration); err != nil {
		return err
	}
	if state.Phase == "verifying" {
		return finishConexantInstall(ctx, device, image.Manifest.Version)
	}
	installed, _, versionErr := readUSBDFUVersion(device)
	if versionErr != nil && !prepared.Recovery {
		return versionErr
	}
	if versionErr == nil && compareVersions(installed, image.Manifest.Version) > 0 {
		return errors.New("UC Voice firmware is newer than this file; refusing a downgrade")
	}
	if !accepted {
		word := "INSTALL"
		if prepared.Recovery {
			word = "RECOVER"
		}
		fmt.Fprintf(os.Stderr, "Firmware: %s %s\nSelected USB: 0b0e:%04x\nKeep the device connected. End calls and close other Jabra tools first.\n", image.Manifest.ProductName, image.Manifest.Version, device.ProductID)
		if !confirmFirmwareAction(os.Stdin, os.Stderr, word) {
			return errors.New("UC Voice firmware install cancelled")
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
	last := -1
	err = transferConexant(ctx, client, image.Records, state.Conexant.Calibration, func() error { return saveFirmwareRecoveryState(state) }, func(done, total int) {
		percent := done * 100 / total
		if percent != last {
			fmt.Fprintf(os.Stderr, "\rUC Voice firmware: %3d%%", percent)
			last = percent
		}
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return fmt.Errorf("UC Voice update stopped; keep this file and USB port for recovery: %w", err)
	}
	state.Phase = "verifying"
	if err := saveFirmwareRecoveryState(state); err != nil {
		return err
	}
	return finishConexantInstall(ctx, device, image.Manifest.Version)
}

func finishConexantInstall(ctx context.Context, device USBDevice, wanted string) error {
	check, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		if err := validateUSBDevice(device); err != nil {
			return errors.New("firmware data was written and checked; reconnect the same UC Voice device to the same USB port and run firmware install with this file again to check its version")
		}
		version, _, err := readUSBDFUVersion(device)
		if err == nil && version == wanted {
			if err := clearFirmwareRecoveryState(); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "UC Voice firmware %s installed and read back from the device.\n", wanted)
			return nil
		}
		if err := waitDFU(check, 250*time.Millisecond); err != nil {
			return errors.New("firmware data was written and checked, but the new version could not be confirmed; reconnect the same device and run firmware install with this file again")
		}
	}
}
