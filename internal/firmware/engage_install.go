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
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type engageInstallBackend interface {
	runtime(context.Context, USBDevice) (*engageRuntime, engageIdentity, func() error, error)
	boot(context.Context, USBDevice) (*sitelRequester, func() error, error)
	wait(context.Context, USBDevice, uint16) (USBDevice, error)
}
type nativeEngageBackend struct{}

func (nativeEngageBackend) runtime(ctx context.Context, device USBDevice) (*engageRuntime, engageIdentity, func() error, error) {
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var last error
	for {
		r, closeConnection, err := openEngageRuntime(device)
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
			return nil, engageIdentity{}, nil, fmt.Errorf("engage runtime did not become ready: %v: %w", last, err)
		}
	}
}
func (nativeEngageBackend) boot(ctx context.Context, device USBDevice) (*sitelRequester, func() error, error) {
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		raw, err := openEngageBoot(device)
		if err == nil {
			link := &sitelLink{io: raw, in: raw.in, out: raw.out, timeout: 2 * time.Second}
			if err := link.start(ready); err != nil {
				_ = raw.file.Close()
				return nil, nil, err
			}
			return &sitelRequester{link: link, address: 1, timeout: 30 * time.Second}, raw.file.Close, nil
		}
		if waitErr := waitDFU(ready, 100*time.Millisecond); waitErr != nil {
			return nil, nil, fmt.Errorf("engage bootloader did not become ready: %v: %w", err, waitErr)
		}
	}
}
func (nativeEngageBackend) wait(ctx context.Context, previous USBDevice, pid uint16) (USBDevice, error) {
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
					return USBDevice{}, errors.New("USB serial changed during Engage update")
				}
				return device, nil
			}
		}
		if err := waitDFU(ctx, 100*time.Millisecond); err != nil {
			return USBDevice{}, err
		}
	}
}

func engageDisconnect(err error) bool {
	return errors.Is(err, unix.ENODEV) || errors.Is(err, unix.ENXIO) || errors.Is(err, unix.ESHUTDOWN) || errors.Is(err, io.EOF)
}

func waitEngageDevice(ctx context.Context, backend engageInstallBackend, previous USBDevice, pid uint16) (USBDevice, error) {
	device, err := backend.wait(ctx, previous, pid)
	if err != nil {
		return device, err
	}
	if device.SysPath != previous.SysPath || device.VendorID != JabraVendorID || device.ProductID != pid || device.attachment == nil || previous.attachment == nil || device.attachment.fingerprint == previous.attachment.fingerprint || (device.Serial != "" && previous.Serial != "" && device.Serial != previous.Serial) {
		return USBDevice{}, errors.New("engage update target changed during reconnect")
	}
	return device, nil
}

// The recovery record is bound to the archive, original runtime identity and
// USB port. It is saved before entering the bootloader, not after a failed flash.
func runEngageInstall(ctx context.Context, backend engageInstallBackend, device USBDevice, images []sitelPlannedImage, wanted string, state *firmwareRecoveryState, save func() error, progress func(byte, int, int)) error {
	if backend == nil || state == nil || save == nil || state.ArchiveSHA256 == "" || wanted == "" {
		return errors.New("incomplete Engage installation plan")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if state.USBSerialSHA256 != "" && device.Serial != "" && state.USBSerialSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(device.Serial))) {
		return errors.New("engage recovery USB identity changed")
	}
	checkpoint := func(phase string) error { state.Phase = phase; return save() }
	var runtime *engageRuntime
	var id engageIdentity
	var closeConnection func() error
	defer func() {
		if closeConnection != nil {
			_ = closeConnection()
		}
	}()
	var err error
	if engageRuntimePID(device.ProductID) {
		runtime, id, closeConnection, err = backend.runtime(ctx, device)
		if err != nil {
			return err
		}
		if id.ControllerAddress != 0 && compareVersions(id.ControllerVersion, wanted) > 0 {
			return errors.New("controller firmware is newer than this package; refusing an unsupported controller downgrade")
		}
		if state.TargetIdentitySHA256 != "" && state.TargetIdentitySHA256 != engageIdentityHash(id) {
			return errors.New("recovery belongs to a different Engage headset")
		}
		if state.ControllerIdentitySHA256 != "" && !engageControllerMatches(state.ControllerIdentitySHA256, id) {
			return errors.New("recovery controller identity changed")
		}
		state.TargetIdentitySHA256 = engageIdentityHash(id)
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
		_, err = runtime.exchange(ctx, 1, 7, 0x80, nil)
		if err != nil && !engageDisconnect(err) {
			return fmt.Errorf("enter Engage bootloader: %w", err)
		}
		_ = closeConnection()
		closeConnection = nil
		{
			wait, cancel := context.WithTimeout(ctx, 30*time.Second)
			device, err = waitEngageDevice(wait, backend, device, state.BootPID)
			cancel()
			if err != nil {
				return err
			}
		}
	} else if device.ProductID != 0x4050 || state.Protocol != 4 || !engageRuntimePID(state.RuntimePID) || state.BootPID != 0x4050 || state.TargetIdentitySHA256 == "" || state.USBPort != filepath.Base(device.SysPath) {
		return errors.New("bootloader recovery requires the saved Engage identity and original USB port")
	}
	{
		if err = checkpoint("flashing"); err != nil {
			return err
		}
		peer, closeBoot, openErr := backend.boot(ctx, device)
		if openErr != nil {
			return openErr
		}
		closeConnection = closeBoot
		info, prepared, prepareErr := prepareSitelTransfer(ctx, peer, images, wanted)
		if prepareErr != nil && info.ID == 0x0b0e4050 && info.Mode == 1 {
			_, err = peer.request(ctx, 5, []byte{3})
			if err != nil && (!peer.link.lastWriteComplete || !engageDisconnect(err)) {
				return err
			}
			_ = closeConnection()
			closeConnection = nil
			wait, cancel := context.WithTimeout(ctx, 30*time.Second)
			device, err = waitEngageDevice(wait, backend, device, state.BootPID)
			cancel()
			if err != nil {
				return err
			}
			peer, closeBoot, err = backend.boot(ctx, device)
			if err != nil {
				return err
			}
			closeConnection = closeBoot
			info, prepared, prepareErr = prepareSitelTransfer(ctx, peer, images, wanted)
		}
		if prepareErr != nil {
			return prepareErr
		}
		if info.ID != 0x0b0e4050 {
			return errors.New("engage bootloader image identity mismatch")
		}
		for _, image := range prepared {
			if err := transferSitelImage(ctx, peer, image, info, func(done, total int) {
				if progress != nil {
					progress(image.Target, done, total)
				}
			}); err != nil {
				return err
			}
		}
		for _, image := range prepared {
			if err := verifySitelImage(ctx, peer, image, info.SectorSize); err != nil {
				return err
			}
		}
		if err = checkpoint("booting-runtime"); err != nil {
			return err
		}
		_, err = peer.request(ctx, 5, []byte{3})
		if err != nil && (!peer.link.lastWriteComplete || !engageDisconnect(err)) {
			return fmt.Errorf("engage runtime boot: %w", err)
		}
		_ = closeConnection()
		closeConnection = nil
		wait, cancel := context.WithTimeout(ctx, 60*time.Second)
		device, err = waitEngageDevice(wait, backend, device, state.RuntimePID)
		cancel()
		if err != nil {
			return err
		}
		runtime, id, closeConnection, err = backend.runtime(ctx, device)
		if err != nil {
			return err
		}
	}
activate:
	if id.Version != wanted || engageIdentityHash(id) != state.TargetIdentitySHA256 {
		return errors.New("engage headset firmware or identity did not verify after restart")
	}
	if state.ControllerIdentitySHA256 != "" && !engageControllerMatches(state.ControllerIdentitySHA256, id) {
		return errors.New("engage controller identity did not verify after restart")
	}
	if err = checkpoint("controller"); err != nil {
		return err
	}
	if err = runtime.activateController(ctx, id, wanted); err != nil {
		if !engageDisconnect(err) {
			return err
		}
		_ = closeConnection()
		closeConnection = nil
		return verifyEngageControllerReattach(ctx, backend, device, state, wanted)
	}
	return nil
}

// A controller restart may re-enumerate the combined USB device. Re-open only
// the original port/model and verify both identities and versions. Never replay
// the activation command merely because its final reply was lost.
func verifyEngageControllerReattach(ctx context.Context, backend engageInstallBackend, previous USBDevice, state *firmwareRecoveryState, wanted string) error {
	wait, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	device, err := waitEngageDevice(wait, backend, previous, state.RuntimePID)
	if err != nil {
		return err
	}
	for {
		_, id, closeConnection, err := backend.runtime(wait, device)
		if err == nil {
			_ = closeConnection()
			if engageIdentityHash(id) != state.TargetIdentitySHA256 || !engageControllerMatches(state.ControllerIdentitySHA256, id) {
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
			next, changed := waitEngageDevice(retry, backend, device, state.RuntimePID)
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

func isSitelManifest(manifest *BuildVector) bool {
	if manifest == nil {
		return false
	}
	for _, file := range manifest.Files {
		if file.SitelHidTargetID != "" {
			return true
		}
	}
	return false
}
func loadEngageImages(path string) (*BuildVector, []sitelPlannedImage, error) {
	manifest, files, err := parseGnVArchive(path)
	if err != nil {
		return nil, nil, err
	}
	pids, err := parseTargetPIDs(manifest.TargetUSBPIDs)
	if err != nil || len(pids) != 1 || pids[0] != 0x4050 {
		return nil, nil, errors.New("not an Engage 50 II firmware package")
	}
	if _, err := parseVersionTriplet(manifest.Version); err != nil {
		return nil, nil, err
	}
	images, err := planSitelImages(manifest, files)
	if err != nil {
		return nil, nil, err
	}
	if len(images) != 3 {
		return nil, nil, errors.New("engage package needs three distinct images")
	}
	for i, target := range []string{"03", "29", "27"} {
		if images[i].File.SitelHidTargetID != target || images[i].File.GNPAddress != "1" || images[i].File.Version != manifest.Version {
			return nil, nil, errors.New("unsupported Engage image order or metadata")
		}
	}
	return manifest, images, nil
}

func installEngageChecked(snapshot *firmwareSnapshot, accepted bool, validateTarget func() error, preferredPID uint16) error {
	manifest, images, err := loadEngageImages(snapshot.path)
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
		if preferredPID != 0 && device.ProductID != preferredPID {
			continue
		}
		if engageRuntimePID(device.ProductID) || device.ProductID == 0x4050 && transfer.Recovery {
			if transfer.Recovery && state.USBPort != filepath.Base(device.SysPath) {
				continue
			}
			candidates = append(candidates, device)
		}
	}
	if len(candidates) != 1 {
		return fmt.Errorf("connect exactly one matching Engage 50 II on its original USB port; found %d", len(candidates))
	}
	device := candidates[0]
	lookupPID := device.ProductID
	if lookupPID == 0x4050 {
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
		return errors.New("engage firmware requires matching official protocol-4 metadata and checksum")
	}
	if previousErr == nil && previous.Protocol != 0 && previous.Protocol != 4 {
		return errors.New("unfinished transfer uses a different firmware protocol")
	}
	if !accepted {
		word := "INSTALL"
		if transfer.Recovery {
			word = "RECOVER"
		}
		fmt.Fprintf(os.Stderr, "Firmware: %s %s\nSelected USB: 0b0e:%04x\nKeep the headset and Link Call Control connected.\n", manifest.ProductName, manifest.Version, device.ProductID)
		fmt.Fprintln(os.Stderr, "End calls and close other Jabra tools first.")
		if !confirmFirmwareAction(os.Stdin, os.Stderr, word) {
			return errors.New("engage firmware install cancelled")
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
	err = runEngageInstall(ctx, nativeEngageBackend{}, device, images, manifest.Version, &state, func() error { return saveFirmwareRecoveryState(state) }, func(target byte, done, total int) {
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
		return fmt.Errorf("engage update stopped; keep this archive for recovery: %w", err)
	}
	if err := clearFirmwareRecoveryState(); err != nil {
		return err
	}
	if state.ControllerIdentitySHA256 != "" {
		fmt.Fprintf(os.Stderr, "Engage firmware %s installed and verified, including Link Call Control.\n", manifest.Version)
	} else {
		fmt.Fprintf(os.Stderr, "Engage headset firmware %s installed and verified. No controller was connected.\n", manifest.Version)
	}
	return nil
}
