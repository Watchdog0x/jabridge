package firmware

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Watchdog0x/jabridge/internal/history"

	"golang.org/x/sys/unix"
)

type sitelInstallBackend interface {
	runtime(context.Context, USBDevice) (*sitelRuntime, sitelIdentity, func() error, error)
	boot(context.Context, USBDevice) (*sitelRequester, func() error, error)
	wait(context.Context, USBDevice, uint16) (USBDevice, error)
}
type nativeSitelBackend struct{}

func (nativeSitelBackend) runtime(ctx context.Context, device USBDevice) (*sitelRuntime, sitelIdentity, func() error, error) {
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var last error
	for {
		r, closeConnection, err := openSitelRuntime(device)
		if err == nil {
			id, readErr := r.identify(ready, device)
			if readErr == nil {
				return r, id, closeConnection, nil
			}
			_ = closeConnection()
			err = readErr
		}
		last = err
		if err := waitDFU(ready, 100*time.Millisecond); err != nil {
			return nil, sitelIdentity{}, nil, fmt.Errorf("sitel runtime did not become ready: %v: %w", last, err)
		}
	}
}
func (nativeSitelBackend) boot(ctx context.Context, device USBDevice) (*sitelRequester, func() error, error) {
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		raw, err := openSitelBoot(device)
		if err == nil {
			link := &sitelLink{io: raw, in: raw.in, out: raw.out, timeout: 2 * time.Second}
			if err := link.start(ready); err != nil {
				_ = raw.file.Close()
				return nil, nil, hidAccessFailure("hid-handshake", err)
			}
			return &sitelRequester{link: link, address: 1, timeout: 30 * time.Second}, raw.file.Close, nil
		}
		if waitErr := waitDFU(ready, 100*time.Millisecond); waitErr != nil {
			return nil, nil, fmt.Errorf("sitel bootloader did not become ready: %w: %w", err, waitErr)
		}
	}
}
func (nativeSitelBackend) wait(ctx context.Context, previous USBDevice, pid uint16) (USBDevice, error) {
	for {
		if err := ctx.Err(); err != nil {
			return USBDevice{}, err
		}
		if validateUSBDevice(previous) != nil {
			devices, err := enumerateBoundUSB()
			if err != nil {
				return USBDevice{}, err
			}
			for _, device := range devices {
				if device.SysPath != previous.SysPath || device.ProductID != pid || device.VendorID != JabraVendorID {
					continue
				}
				if previous.Serial != "" && device.Serial != "" && previous.Serial != device.Serial {
					return USBDevice{}, errors.New("USB serial changed during Sitel update")
				}
				return device, nil
			}
		}
		if err := waitDFU(ctx, 100*time.Millisecond); err != nil {
			return USBDevice{}, err
		}
	}
}

func sitelDisconnect(err error) bool {
	return errors.Is(err, unix.ENODEV) || errors.Is(err, unix.ENXIO) || errors.Is(err, unix.ESHUTDOWN) || errors.Is(err, io.EOF)
}

// A runtime reboot can remove HID before its reply arrives. This only permits
// waiting for the already-requested transition, never resending the command.
// The caller still verifies the new attachment, expected PID, port and identity
// before opening the bootloader or writing firmware.
func sitelRebootMayHaveStarted(err error) bool {
	if errors.Is(err, errManagementNotSent) || errors.Is(err, errSitelRejected) {
		return false
	}
	return sitelDisconnect(err) || errors.Is(err, unix.EIO) || errors.Is(err, context.DeadlineExceeded)
}

func waitSitelDevice(ctx context.Context, backend sitelInstallBackend, previous USBDevice, pid uint16) (USBDevice, error) {
	device, err := backend.wait(ctx, previous, pid)
	if err != nil {
		return device, err
	}
	if device.SysPath != previous.SysPath || device.VendorID != JabraVendorID || device.ProductID != pid || device.attachment == nil || previous.attachment == nil || device.attachment.fingerprint == previous.attachment.fingerprint || (device.Serial != "" && previous.Serial != "" && device.Serial != previous.Serial) {
		return USBDevice{}, errors.New("sitel update target changed during reconnect")
	}
	return device, nil
}

// The recovery record is bound to the archive, original runtime identity and
// USB port. It is saved before entering the bootloader, not after a failed flash.
func runSitelInstall(ctx context.Context, backend sitelInstallBackend, device USBDevice, images []sitelPlannedImage, wanted string, state *firmwareRecoveryState, save func() error, progress func(byte, int, int)) (resultErr error) {
	if backend == nil || state == nil || save == nil || state.ArchiveSHA256 == "" || wanted == "" {
		return errors.New("incomplete Sitel installation plan")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stage := "sitel-plan"
	finish := history.Begin(history.Event{Component: "firmware", Action: stage, USBProduct: device.ProductID, Connection: "usb"})
	nextStage := func(action string) {
		finish(nil)
		stage = action
		finish = history.Begin(history.Event{Component: "firmware", Action: stage, USBProduct: device.ProductID, Connection: "usb"})
	}
	defer func() {
		if value := recover(); value != nil {
			finish(history.ErrPanic)
			panic(value)
		}
		finish(resultErr)
		if resultErr != nil {
			resultErr = fmt.Errorf("%s: %w", stage, resultErr)
		}
	}()
	profile, ok := sitelProfileForPID(device.ProductID)
	if !ok || device.VendorID != JabraVendorID || device.ViaDongle || device.attachment == nil {
		return errors.New("unsupported Sitel installation target")
	}
	if err := profile.validateImages(images, wanted); err != nil {
		return err
	}
	expectedID := uint32(JabraVendorID)<<16 | uint32(profile.BootPID)
	if state.USBSerialSHA256 != "" && device.Serial != "" && state.USBSerialSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(device.Serial))) {
		return errors.New("sitel recovery USB identity changed")
	}
	checkpoint := func(phase string) error { state.Phase = phase; return save() }
	var runtime *sitelRuntime
	var id sitelIdentity
	var closeConnection func() error
	defer func() {
		if closeConnection != nil {
			_ = closeConnection()
		}
	}()
	var err error
	if profile.runtime(device.ProductID) {
		nextStage("sitel-identify")
		runtime, id, closeConnection, err = backend.runtime(ctx, device)
		if err != nil {
			return err
		}
		if id.PID != device.ProductID || id.BootPID != profile.BootPID {
			return errors.New("sitel runtime identity does not match the installation target")
		}
		if id.ControllerAddress != 0 && compareVersions(id.ControllerVersion, wanted) > 0 {
			return errors.New("controller firmware is newer than this package; refusing an unsupported controller downgrade")
		}
		if state.TargetIdentitySHA256 != "" && state.TargetIdentitySHA256 != sitelIdentityHash(id) {
			return errors.New("recovery belongs to a different Sitel headset")
		}
		if state.ControllerIdentitySHA256 != "" && !engageControllerMatches(state.ControllerIdentitySHA256, id) {
			return errors.New("recovery controller identity changed")
		}
		state.TargetIdentitySHA256 = sitelIdentityHash(id)
		state.RuntimePID = id.PID
		state.BootPID = id.BootPID
		state.Protocol = 4
		state.USBPort = filepath.Base(device.SysPath)
		if device.Serial != "" {
			state.USBSerialSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(device.Serial)))
		}
		if id.ControllerAddress != 0 {
			state.ControllerIdentitySHA256 = engageControllerHash(id)
		}
		if (state.Phase == "controller" || state.Phase == "booting-runtime") && id.Version == wanted {
			goto activate
		}
		if err = checkpoint("entering-bootloader"); err != nil {
			return err
		}
		nextStage("sitel-enter")
		_, err = runtime.exchange(ctx, id.Address, 7, 0x80, nil)
		if err != nil && !sitelRebootMayHaveStarted(err) {
			return fmt.Errorf("enter Sitel bootloader: %w", err)
		}
		_ = closeConnection()
		closeConnection = nil
		{
			nextStage("sitel-wait-boot")
			wait, cancel := context.WithTimeout(ctx, 30*time.Second)
			device, err = waitSitelDevice(wait, backend, device, state.BootPID)
			cancel()
			if err != nil {
				return err
			}
		}
	} else if device.ProductID != profile.BootPID || state.Protocol != 4 || !profile.runtime(state.RuntimePID) || state.BootPID != profile.BootPID || state.TargetIdentitySHA256 == "" || state.USBPort != filepath.Base(device.SysPath) {
		return errors.New("bootloader recovery requires the saved Sitel identity and original USB port")
	}
	{
		if err = checkpoint("flashing"); err != nil {
			return err
		}
		nextStage("sitel-open-boot")
		peer, closeBoot, openErr := backend.boot(ctx, device)
		if openErr != nil {
			return openErr
		}
		closeConnection = closeBoot
		nextStage("sitel-prepare")
		info, prepared, prepareErr := prepareSitelTransfer(ctx, peer, images, wanted)
		if prepareErr != nil && info.ID == expectedID && info.Mode == 1 {
			nextStage("sitel-restart-boot")
			_, err = peer.request(ctx, 5, []byte{3})
			if err != nil && (!peer.link.lastWriteComplete || !sitelDisconnect(err)) {
				return err
			}
			_ = closeConnection()
			closeConnection = nil
			nextStage("sitel-wait-boot")
			wait, cancel := context.WithTimeout(ctx, 30*time.Second)
			device, err = waitSitelDevice(wait, backend, device, state.BootPID)
			cancel()
			if err != nil {
				return err
			}
			nextStage("sitel-open-boot")
			peer, closeBoot, err = backend.boot(ctx, device)
			if err != nil {
				return err
			}
			closeConnection = closeBoot
			nextStage("sitel-prepare")
			info, prepared, prepareErr = prepareSitelTransfer(ctx, peer, images, wanted)
		}
		if prepareErr != nil {
			return prepareErr
		}
		if info.ID != expectedID {
			return errors.New("sitel bootloader image identity mismatch")
		}
		nextStage("sitel-transfer")
		for _, image := range prepared {
			if err := transferSitelImage(ctx, peer, image, info, func(done, total int) {
				if progress != nil {
					progress(image.Target, done, total)
				}
			}); err != nil {
				return err
			}
		}
		nextStage("sitel-verify")
		for _, image := range prepared {
			if err := verifySitelImage(ctx, peer, image, info.SectorSize); err != nil {
				return err
			}
		}
		if err = checkpoint("booting-runtime"); err != nil {
			return err
		}
		nextStage("sitel-start-runtime")
		_, err = peer.request(ctx, 5, []byte{3})
		if err != nil && (!peer.link.lastWriteComplete || !sitelDisconnect(err)) {
			return fmt.Errorf("sitel runtime boot: %w", err)
		}
		_ = closeConnection()
		closeConnection = nil
		nextStage("sitel-wait-runtime")
		wait, cancel := context.WithTimeout(ctx, 60*time.Second)
		device, err = waitSitelDevice(wait, backend, device, state.RuntimePID)
		cancel()
		if err != nil {
			return err
		}
		nextStage("sitel-identify")
		runtime, id, closeConnection, err = backend.runtime(ctx, device)
		if err != nil {
			return err
		}
	}
activate:
	nextStage("sitel-activate")
	if id.Version != wanted || sitelIdentityHash(id) != state.TargetIdentitySHA256 {
		return errors.New("sitel headset firmware or identity did not verify after restart")
	}
	if state.ControllerIdentitySHA256 != "" && !engageControllerMatches(state.ControllerIdentitySHA256, id) {
		return errors.New("sitel controller identity did not verify after restart")
	}
	if err = checkpoint("controller"); err != nil {
		return err
	}
	if err = runtime.activateController(ctx, id, wanted); err != nil {
		if !sitelDisconnect(err) {
			return err
		}
		_ = closeConnection()
		closeConnection = nil
		return verifyEngageControllerReattach(ctx, backend, device, state, wanted)
	}
	return nil
}

func isSitelManifest(manifest *BuildVector) bool {
	if manifest == nil {
		return false
	}
	for _, file := range manifest.Files {
		if file.SitelHidTargetID != "" || strings.EqualFold(filepath.Ext(file.Name), ".hex") {
			return true
		}
	}
	return false
}
func installSitelChecked(snapshot *firmwareSnapshot, accepted bool, validateTarget func() error, preferredPID uint16) error {
	manifest, images, err := loadSitelImages(snapshot.path)
	if err != nil {
		return err
	}
	profile, err := sitelProfileForManifest(manifest)
	if err != nil {
		return err
	}
	transfer, err := prepareFirmwareTransfer(snapshot.path, manifest)
	if err != nil {
		return err
	}
	state := transfer.State
	previous, previousErr := loadFirmwareRecoveryState()
	if transfer.Recovery {
		state.RuntimePID = previous.RuntimePID
		state.BootPID = previous.BootPID
		state.Phase = previous.Phase
		state.TargetIdentitySHA256 = previous.TargetIdentitySHA256
		state.ControllerIdentitySHA256 = previous.ControllerIdentitySHA256
		state.USBPort = previous.USBPort
		state.Protocol = previous.Protocol
		state.USBSerialSHA256 = previous.USBSerialSHA256
	}
	devices, err := enumerateBoundUSB()
	if err != nil {
		return err
	}
	var candidates []USBDevice
	for _, device := range devices {
		if device.VendorID != JabraVendorID || device.ViaDongle {
			continue
		}
		if preferredPID != 0 && device.ProductID != preferredPID {
			continue
		}
		if profile.runtime(device.ProductID) || device.ProductID == profile.BootPID && transfer.Recovery {
			if transfer.Recovery && state.USBPort != filepath.Base(device.SysPath) {
				continue
			}
			candidates = append(candidates, device)
		}
	}
	if len(candidates) != 1 {
		return fmt.Errorf("connect exactly one matching %s on its original USB port; found %d", profile.Name, len(candidates))
	}
	device := candidates[0]
	lookupPID := device.ProductID
	if lookupPID == profile.BootPID {
		lookupPID = state.RuntimePID
	}
	ctx, cancel := context.WithTimeout(context.Background(), MetadataTimeout)
	evidence, err := firmwareModelCatalog.FirmwareRelease(ctx, lookupPID, manifest.Version)
	cancel()
	if err != nil {
		return err
	}
	checksum, err := firmwareFileMD5(snapshot.path)
	if err != nil {
		return err
	}
	if !firmwareReleaseMatchesDevice(checksum, lookupPID, evidence) || evidence.HasUnspecifiedFirmwareProtocol || len(evidence.FirmwareProtocols) != 1 || evidence.FirmwareProtocols[0] != 4 {
		return errors.New("sitel firmware requires matching official protocol-4 metadata and checksum")
	}
	if previousErr == nil && previous.Protocol != 0 && previous.Protocol != 4 {
		return errors.New("unfinished transfer uses a different firmware protocol")
	}
	if !accepted {
		word := "INSTALL"
		if transfer.Recovery {
			word = "RECOVER"
		}
		fmt.Fprintf(os.Stderr, "Firmware: %s %s\nSelected USB: 0b0e:%04x\nKeep the device connected.\n", manifest.ProductName, manifest.Version, device.ProductID)
		if engageHasController(lookupPID) {
			fmt.Fprintln(os.Stderr, "Keep Link Call Control connected too.")
		}
		fmt.Fprintln(os.Stderr, "End calls and close other Jabra tools first.")
		if !confirmFirmwareAction(os.Stdin, os.Stderr, word) {
			return errors.New("sitel firmware install cancelled")
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel = context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	lastTarget, lastPercent := byte(0), -1
	err = runSitelInstall(ctx, nativeSitelBackend{}, device, images, manifest.Version, &state, func() error { return saveFirmwareRecoveryState(state) }, func(target byte, done, total int) {
		percent := done * 100 / total
		if target != lastTarget || percent != lastPercent {
			fmt.Fprintf(os.Stderr, "\rFirmware target %d: %3d%%", target, percent)
			lastTarget, lastPercent = target, percent
			if done == total {
				fmt.Fprintln(os.Stderr)
			}
		}
	})
	if err != nil {
		return fmt.Errorf("sitel update stopped; keep this archive for recovery: %w", err)
	}
	if err := clearFirmwareRecoveryState(); err != nil {
		return err
	}
	if state.ControllerIdentitySHA256 != "" {
		fmt.Fprintf(os.Stderr, "%s firmware %s installed and verified, including Link Call Control.\n", profile.Name, manifest.Version)
	} else {
		fmt.Fprintf(os.Stderr, "%s firmware %s installed and verified.\n", profile.Name, manifest.Version)
	}
	return nil
}
