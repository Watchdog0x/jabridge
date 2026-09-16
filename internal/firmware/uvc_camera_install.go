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

type uvcCameraRecovery struct {
	OriginalVersion string `json:"originalVersion"`
	BootRequired    bool   `json:"bootRequired"`
	RebootFrom      string `json:"rebootFrom,omitempty"`
}

func validUVCCameraRecovery(state firmwareRecoveryState) error {
	part := state.UVCCamera
	pids, err := parseTargetPIDs(state.TargetUSBPIDs)
	if state.Protocol != 11 || part == nil || !uvcCameraPID(state.RuntimePID) || state.BootPID != 0x3020 || err != nil || len(pids) != 1 || pids[0] != 0x3020 || state.USBPort == "" || state.ControllerIdentitySHA256 != "" {
		return errors.New("invalid PanaCast 20 recovery target")
	}
	if _, err := parseVersionTriplet(part.OriginalVersion); err != nil {
		return err
	}
	if part.BootRequired != uvcBootRequired(state.RuntimePID, part.OriginalVersion) {
		return errors.New("invalid PanaCast 20 bootloader migration")
	}
	for i, value := range []string{state.TargetIdentitySHA256, state.USBSerialSHA256} {
		if i == 1 && value == "" {
			continue
		}
		data, err := hex.DecodeString(value)
		if err != nil || len(data) != sha256.Size {
			return errors.New("invalid PanaCast 20 recovery identity")
		}
	}
	switch state.Phase {
	case "boot":
		if !part.BootRequired {
			return errors.New("unexpected PanaCast 20 boot update")
		}
	case "main", "settling", "verifying":
	case "rebooting":
		data, err := hex.DecodeString(part.RebootFrom)
		if err != nil || len(data) != sha256.Size {
			return errors.New("missing PanaCast 20 reboot attachment")
		}
	default:
		return errors.New("invalid PanaCast 20 recovery phase")
	}
	if state.Phase != "rebooting" && part.RebootFrom != "" {
		return errors.New("unexpected PanaCast 20 reboot attachment")
	}
	return nil
}

func bindUVCCameraRecovery(state *firmwareRecoveryState, device USBDevice, id cameraIdentity) error {
	if state == nil || !uvcCameraPID(device.ProductID) || device.VendorID != JabraVendorID || device.ViaDongle || device.attachment == nil || id.PID != device.ProductID || id.Port != device.SysPath || id.Serial == "" {
		return errors.New("PanaCast 20 update target is not bound")
	}
	serial := ""
	if device.Serial != "" {
		serial = fmt.Sprintf("%x", sha256.Sum256([]byte(device.Serial)))
	}
	port := filepath.Base(device.SysPath)
	if state.Protocol != 0 {
		if err := validUVCCameraRecovery(*state); err != nil {
			return err
		}
		if state.RuntimePID != device.ProductID || state.USBPort != port || state.TargetIdentitySHA256 != id.hash() || state.USBSerialSHA256 != serial {
			return errors.New("PanaCast 20 recovery device or USB port changed")
		}
		return nil
	}
	state.Protocol, state.RuntimePID, state.BootPID = 11, device.ProductID, 0x3020
	state.USBPort, state.USBSerialSHA256, state.TargetIdentitySHA256 = port, serial, id.hash()
	state.UVCCamera = &uvcCameraRecovery{OriginalVersion: id.Version, BootRequired: uvcBootRequired(device.ProductID, id.Version)}
	state.Phase = "main"
	if state.UVCCamera.BootRequired {
		state.Phase = "boot"
	}
	return validUVCCameraRecovery(*state)
}

type uvcCameraBackend interface {
	Identity(context.Context, USBDevice) (cameraIdentity, error)
	Image(context.Context, USBDevice, uvcCameraImage, func(int, int)) error
	Reset(context.Context, USBDevice, cameraIdentity) error
	Reboot(context.Context, USBDevice) (USBDevice, error)
	Sleep(context.Context, time.Duration) error
}
type nativeUVCCameraBackend struct{}

func (nativeUVCCameraBackend) Identity(ctx context.Context, device USBDevice) (cameraIdentity, error) {
	if !uvcCameraPID(device.ProductID) {
		return cameraIdentity{}, errors.New("not a PanaCast 20")
	}
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		r, closeConnection, err := openSitelRuntime(device)
		if err == nil {
			id, readErr := r.identifyUSBCamera(ready, device, 0x3020, 11)
			_ = closeConnection()
			if readErr == nil {
				return id, nil
			}
			err = readErr
		}
		if waitErr := waitDFU(ready, 250*time.Millisecond); waitErr != nil {
			return cameraIdentity{}, fmt.Errorf("PanaCast 20 did not become ready: %v: %w", err, waitErr)
		}
	}
}
func (nativeUVCCameraBackend) Image(ctx context.Context, device USBDevice, image uvcCameraImage, progress func(int, int)) error {
	transport, err := openCameraUVC(ctx, device)
	if err != nil {
		return err
	}
	defer func() { _ = transport.Close() }()
	page, err := uvcCameraPageSize(ctx, transport)
	if err != nil {
		return err
	}
	if page == 1024 {
		length := make([]byte, 2)
		if err := transport.query(ctx, 23, 0x85, length); err != nil {
			return err
		}
		if length[0] != 7 || length[1] != 4 {
			return errors.New("PanaCast 20 large-page control is unavailable")
		}
	}
	return transferUVCCameraImage(ctx, transport, image, page, waitDFU, progress)
}
func (nativeUVCCameraBackend) Reset(ctx context.Context, device USBDevice, expected cameraIdentity) error {
	if err := requireHardwareWrites(); err != nil {
		return cameraCommandNotStarted(err)
	}
	r, closeConnection, err := openSitelRuntime(device)
	if err != nil {
		return cameraCommandNotStarted(err)
	}
	defer func() { _ = closeConnection() }()
	id, err := r.identifyUSBCamera(ctx, device, 0x3020, 11)
	if err != nil {
		return cameraCommandNotStarted(err)
	}
	if id.hash() != expected.hash() {
		return cameraCommandNotStarted(errors.New("camera changed before restart"))
	}
	_, err = r.exchange(ctx, id.Address, 13, 0x80, []byte{0x12})
	if errors.Is(err, errSitelRejected) || errors.Is(err, errManagementNotSent) {
		return cameraCommandNotStarted(err)
	}
	return err
}
func (nativeUVCCameraBackend) Reboot(ctx context.Context, d USBDevice) (USBDevice, error) {
	return (nativeCameraBackend{}).Reboot(ctx, d)
}
func (nativeUVCCameraBackend) Sleep(ctx context.Context, d time.Duration) error {
	return waitDFU(ctx, d)
}

func runUVCCameraInstall(ctx context.Context, backend uvcCameraBackend, device USBDevice, archive *uvcCameraArchive, state *firmwareRecoveryState, save func() error, progress func(string, int, int)) error {
	if backend == nil || archive == nil || archive.Manifest == nil || state == nil || save == nil {
		return errors.New("incomplete PanaCast 20 update plan")
	}
	id, err := backend.Identity(ctx, device)
	if err != nil {
		return err
	}
	if err := bindUVCCameraRecovery(state, device, id); err != nil {
		return err
	}
	if compareVersions(id.Version, archive.Manifest.Version) > 0 {
		return errors.New("PanaCast 20 firmware is newer than this file; refusing a downgrade")
	}
	checkpoint := func(phase string) error {
		state.Phase = phase
		state.UVCCamera.RebootFrom = ""
		if phase == "rebooting" {
			state.UVCCamera.RebootFrom = device.attachment.fingerprint
		}
		return save()
	}
	for {
		switch state.Phase {
		case "boot", "main":
			phase := state.Phase
			image := archive.Main
			if phase == "boot" {
				image = archive.Boot
			}
			if err := checkpoint(phase); err != nil {
				return err
			}
			if err := backend.Image(ctx, device, image, func(done, total int) {
				if progress != nil {
					progress(phase, done, total)
				}
			}); err != nil {
				return err
			}
			next := "settling"
			if phase == "boot" {
				next = "main"
			}
			if err := checkpoint(next); err != nil {
				return err
			}
		case "settling":
			if progress != nil {
				progress("Finishing camera writes", 0, 0)
			}
			// Linux JabraCLI adds this delay after its five-second UVC settle.
			// Completion still requires the new version on the same camera.
			if err := backend.Sleep(ctx, 20*time.Second); err != nil {
				return err
			}
			if err := checkpoint("rebooting"); err != nil {
				return err
			}
			if err := backend.Reset(ctx, device, id); err != nil {
				if errors.Is(err, errCameraCommandNotStarted) || errors.Is(err, errSitelRejected) {
					if saveErr := checkpoint("settling"); saveErr != nil {
						return errors.Join(err, saveErr)
					}
					return err
				}
				if !errors.Is(err, context.DeadlineExceeded) && !sitelDisconnect(err) {
					return err
				}
			}
			// A lost reset ACK may mean it already rebooted. Keep the saved
			// intent and check reconnection instead of sending another reset.
		case "rebooting":
			if device.attachment.fingerprint == state.UVCCamera.RebootFrom {
				device, err = backend.Reboot(ctx, device)
				if err != nil {
					return err
				}
			}
			id, err = backend.Identity(ctx, device)
			if err != nil {
				return err
			}
			if err := bindUVCCameraRecovery(state, device, id); err != nil {
				return err
			}
			if err := checkpoint("verifying"); err != nil {
				return err
			}
		case "verifying":
			id, err = backend.Identity(ctx, device)
			if err != nil {
				return err
			}
			if err := bindUVCCameraRecovery(state, device, id); err != nil {
				return err
			}
			if compareVersions(id.Version, archive.Manifest.Version) != 0 {
				if err := checkpoint("main"); err != nil {
					return err
				}
				return errors.New("PanaCast 20 returned without the expected firmware; retry the same file to transfer its main image again")
			}
			return nil
		default:
			return errors.New("unknown PanaCast 20 update phase")
		}
	}
}

func verifyUVCCameraRelease(ctx context.Context, path string, archive *uvcCameraArchive, pid uint16) error {
	if archive == nil || !uvcCameraPID(pid) {
		return errors.New("PanaCast 20 firmware does not match the selected model")
	}
	evidence, err := firmwareModelCatalog.FirmwareRelease(ctx, pid, archive.Manifest.Version)
	if err != nil {
		return err
	}
	checksum, err := firmwareFileMD5(path)
	if err != nil {
		return err
	}
	if !firmwareReleaseMatchesDevice(checksum, pid, evidence) || evidence.HasUnspecifiedFirmwareProtocol || len(evidence.FirmwareProtocols) != 1 || evidence.FirmwareProtocols[0] != 11 {
		return errors.New("PanaCast 20 firmware requires matching official protocol-11 metadata and checksum")
	}
	return nil
}

func installUVCCameraChecked(snapshot *firmwareSnapshot, accepted bool, validateTarget func() error, preferredPID uint16) error {
	archive, err := loadUVCCameraArchive(snapshot.path)
	if err != nil {
		return err
	}
	devices, err := enumerateBoundUSB()
	if err != nil {
		return err
	}
	var matches []USBDevice
	for _, d := range devices {
		if d.VendorID == JabraVendorID && !d.ViaDongle && uvcCameraPID(d.ProductID) && (preferredPID == 0 || d.ProductID == preferredPID) {
			matches = append(matches, d)
		}
	}
	if len(matches) != 1 {
		return fmt.Errorf("connect exactly one matching PanaCast 20 by USB; found %d", len(matches))
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
		if err := validUVCCameraRecovery(previous); err != nil {
			return err
		}
		previous.Attempt = state.Attempt
		state = previous
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	metadata, finish := context.WithTimeout(ctx, MetadataTimeout)
	err = verifyUVCCameraRelease(metadata, snapshot.path, archive, device.ProductID)
	finish()
	if err != nil {
		return err
	}
	if !accepted {
		word := "INSTALL"
		if prepared.Recovery {
			word = "RECOVER"
		}
		fmt.Fprintf(os.Stderr, "Firmware: PanaCast 20 %s\nSelected USB: 0b0e:%04x\nKeep USB connected. End calls and close other camera tools first.\n", archive.Manifest.Version, device.ProductID)
		if !confirmFirmwareAction(os.Stdin, os.Stderr, word) {
			return errors.New("PanaCast 20 firmware install cancelled")
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
	err = runUVCCameraInstall(ctx, nativeUVCCameraBackend{}, device, archive, &state, func() error { return saveFirmwareRecoveryState(state) }, func(stage string, done, total int) {
		text := stage
		if total > 0 {
			text = fmt.Sprintf("Camera %s: %d%%", stage, int64(done)*100/int64(total))
		}
		if text != last {
			fmt.Fprintln(os.Stderr, text)
			last = text
		}
	})
	if err != nil {
		return fmt.Errorf("PanaCast 20 update stopped; keep this file and USB port for recovery: %w", err)
	}
	if err := clearFirmwareRecoveryState(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "PanaCast 20 firmware %s installed and confirmed on the same camera.\n", archive.Manifest.Version)
	return nil
}
