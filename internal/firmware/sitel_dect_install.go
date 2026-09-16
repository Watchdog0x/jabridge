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
	"strconv"
	"syscall"
	"time"
)

type sitelDECTRecovery struct {
	HeadsetIdentitySHA256 string `json:"headsetIdentitySha256"`
	HeadsetImagePID       uint16 `json:"headsetImagePid"`
	USBIdentitySHA256     string `json:"usbIdentitySha256,omitempty"`
	BaseRegion            byte   `json:"baseRegion,omitempty"`
	HeadsetRegion         byte   `json:"headsetRegion,omitempty"`
	MMIIdentitySHA256     string `json:"mmiIdentitySha256,omitempty"`
	RadioRevision         uint16 `json:"radioRevision,omitempty"`
	SettingsVerified      bool   `json:"settingsVerified,omitempty"`
	LanguageRegion        byte   `json:"languageRegion,omitempty"`
}

func validSitelDECTRecovery(state firmwareRecoveryState) error {
	profile, ok := sitelDECTProfileForPID(state.RuntimePID)
	pids, err := parseTargetPIDs(state.TargetUSBPIDs)
	if !ok || state.Protocol != 4 || state.SitelDECT == nil || !profile.runtime(state.RuntimePID) || state.BootPID != profile.BootPID || state.SitelDECT.HeadsetImagePID != profile.HeadsetImagePID || err != nil || len(pids) != 1 || pids[0] != profile.BootPID || state.USBPort == "" || state.ControllerIdentitySHA256 != "" {
		return errors.New("invalid DECT recovery identity")
	}
	for _, value := range []string{state.TargetIdentitySHA256, state.SitelDECT.HeadsetIdentitySHA256} {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != 32 {
			return errors.New("incomplete DECT recovery identity")
		}
	}
	if profile.USBImagePID != 0 {
		value, err := hex.DecodeString(state.SitelDECT.USBIdentitySHA256)
		if err != nil || len(value) != sha256.Size {
			return errors.New("DECT USB controller recovery identity is missing")
		}
	} else if state.SitelDECT.USBIdentitySHA256 != "" {
		return errors.New("unexpected DECT USB controller recovery identity")
	}
	if state.USBSerialSHA256 != "" {
		decoded, err := hex.DecodeString(state.USBSerialSHA256)
		if err != nil || len(decoded) != 32 {
			return errors.New("invalid DECT USB identity digest")
		}
	}
	if profile.Legacy && (state.SitelDECT.BaseRegion != 0 || state.SitelDECT.HeadsetRegion != 0) || !profile.Legacy && (state.SitelDECT.BaseRegion == 0 || state.SitelDECT.HeadsetRegion == 0) {
		return errors.New("invalid DECT recovery regions")
	}
	if profile.MMIImagePID != 0 {
		value, err := hex.DecodeString(state.SitelDECT.MMIIdentitySHA256)
		if err != nil || len(value) != sha256.Size || state.SitelDECT.LanguageRegion != 7 {
			return errors.New("missing Engage 75 display or language identity")
		}
		revision := state.SitelDECT.RadioRevision
		if revision != 0 && byte(revision) != 0x28 && byte(revision) != 0x35 || state.SitelDECT.SettingsVerified && revision == 0 {
			return errors.New("invalid Engage 75 radio recovery identity")
		}
	} else if state.SitelDECT.MMIIdentitySHA256 != "" || state.SitelDECT.RadioRevision != 0 || state.SitelDECT.SettingsVerified || state.SitelDECT.LanguageRegion != 0 {
		return errors.New("unexpected Engage 75 recovery fields")
	}
	switch state.Phase {
	case "entering-bootloader", "flashing", "booting-runtime":
	default:
		return errors.New("invalid DECT recovery phase")
	}
	return nil
}

func bindSitelDECTRecovery(state *firmwareRecoveryState, identity sitelDECTIdentity, device USBDevice, archive *sitelDECTArchive) error {
	if state == nil || identity.Base.BootPID != archive.Profile.BootPID || identity.Headset.BootPID != archive.Profile.HeadsetImagePID || !archive.Profile.runtime(identity.Base.PID) || identity.Headset.Address != 10 {
		return errors.New("DECT component identity does not match the archive")
	}
	if archive.Engage75 != nil && (identity.MMI.Address != 3 || identity.MMI.BootPID != 0x1117 || identity.LanguageRegion != 7) {
		return errors.New("engage 75 display or language changed")
	}
	baseHash, headHash := sitelIdentityHash(identity.Base), sitelIdentityHash(identity.Headset)
	usbHash := ""
	if archive.Profile.USBImagePID != 0 {
		if identity.USB.Address != 2 || identity.USB.BootPID != archive.Profile.USBImagePID {
			return errors.New("DECT USB controller does not match its archive")
		}
		usbHash = sitelIdentityHash(identity.USB)
	}
	port := filepath.Base(device.SysPath)
	if state.Protocol != 0 {
		if err := validSitelDECTRecovery(*state); err != nil {
			return err
		}
		if state.USBPort != port || state.TargetIdentitySHA256 != baseHash || state.SitelDECT.HeadsetIdentitySHA256 != headHash || state.SitelDECT.USBIdentitySHA256 != usbHash || state.SitelDECT.BaseRegion != identity.BaseRegion || state.SitelDECT.HeadsetRegion != identity.HeadsetRegion {
			return errors.New("DECT base, docked headset or region changed during recovery")
		}
		if archive.Engage75 != nil && (state.SitelDECT.MMIIdentitySHA256 != sitelIdentityHash(identity.MMI) || state.SitelDECT.LanguageRegion != identity.LanguageRegion) {
			return errors.New("engage 75 display changed during recovery")
		}
		return nil
	}
	state.Protocol, state.RuntimePID, state.BootPID = 4, identity.Base.PID, archive.Profile.BootPID
	state.USBPort, state.TargetIdentitySHA256, state.Phase = port, baseHash, "entering-bootloader"
	if device.Serial != "" {
		state.USBSerialSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(device.Serial)))
	}
	state.SitelDECT = &sitelDECTRecovery{HeadsetIdentitySHA256: headHash, HeadsetImagePID: identity.Headset.BootPID, USBIdentitySHA256: usbHash, BaseRegion: identity.BaseRegion, HeadsetRegion: identity.HeadsetRegion}
	if archive.Engage75 != nil {
		state.SitelDECT.MMIIdentitySHA256 = sitelIdentityHash(identity.MMI)
		state.SitelDECT.LanguageRegion = identity.LanguageRegion
	}
	return validSitelDECTRecovery(*state)
}

func dectComponentVersion(identity sitelDECTIdentity, file GnVFile) string {
	if file.SitelHidTargetID == "23" || file.SitelHidTargetID == "24" {
		return identity.RadioVersion
	}
	if file.GNPAddress == "3" {
		return identity.MMI.Version
	}
	if file.Content == "langpack" {
		return identity.Language
	}
	if file.Content == "graphics" {
		return identity.Graphics
	}
	if file.GNPAddress == "2" {
		return identity.USB.Version
	}
	if file.Target == "headset" {
		if file.Content == "tunepack" {
			return identity.HeadsetTunes
		}
		return identity.Headset.Version
	}
	if file.Content == "tunepack" {
		return identity.BaseTunes
	}
	return identity.Base.Version
}
func dectVersionsMatch(identity sitelDECTIdentity, archive *sitelDECTArchive) bool {
	if archive.Engage75 != nil && identity.RadioVersion != archive.Manifest.Version {
		return false
	}
	for _, image := range archive.Images {
		if dectComponentVersion(identity, image.File) != image.File.Version {
			return false
		}
	}
	return true
}

type sitelDECTPrepared struct {
	Image sitelPreparedImage
	Info  sitelDeviceInfo
	Peer  sitelRequest
}

func prepareSitelDECTTransfer(ctx context.Context, backend sitelDECTBackend, connection *sitelDECTConnection, archive *sitelDECTArchive) ([]sitelDECTPrepared, error) {
	byTarget := map[byte]sitelDECTPrepared{}
	for _, address := range archive.Profile.endpoints() {
		peer := connection.peer(address)
		expectedPID := archive.Profile.BootPID
		if address == 10 {
			expectedPID = archive.Profile.HeadsetImagePID
		}
		if address == 3 {
			expectedPID = archive.Profile.MMIImagePID
		}
		if address == 2 {
			expectedPID = archive.Profile.USBImagePID
		}
		var info sitelDeviceInfo
		ready, cancel := context.WithTimeout(ctx, 15*time.Second)
		switched := false
		for {
			data, err := peer.request(ready, 0, nil)
			if err != nil {
				cancel()
				return nil, err
			}
			info, err = decodeSitelInfo(data)
			if err != nil {
				cancel()
				return nil, err
			}
			if info.ID != uint32(JabraVendorID)<<16|uint32(expectedPID) {
				cancel()
				return nil, errors.New("DECT bootloader component image ID mismatch")
			}
			if info.Mode != 1 {
				break
			}
			if address == archive.Profile.carrier() {
				cancel()
				return nil, errors.New("DECT base is still in application mode")
			}
			if !switched {
				if _, err := peer.request(ready, 5, []byte{3}); err != nil {
					cancel()
					return nil, err
				}
				switched = true
			}
			if err := backend.sleep(ready, 100*time.Millisecond); err != nil {
				cancel()
				return nil, err
			}
		}
		cancel()
		var images []sitelPlannedImage
		for _, image := range archive.Images {
			if image.File.GNPAddress == strconv.Itoa(int(address)) {
				images = append(images, image)
			}
		}
		prepared, err := prepareSitelTargets(ctx, peer, images, info, 3, strconv.Itoa(int(address)))
		if err != nil {
			return nil, err
		}
		for _, image := range prepared {
			byTarget[image.Target] = sitelDECTPrepared{Image: image, Info: info, Peer: peer}
		}
	}
	var ordered []sitelDECTPrepared
	for _, target := range archive.Profile.Targets {
		if archive.Engage75 != nil && (target == 23 || target == 24) {
			continue
		}
		image, ok := byTarget[target]
		if !ok {
			return nil, errors.New("DECT component was not prepared")
		}
		ordered = append(ordered, image)
	}
	return ordered, nil
}

func runSitelDECTInstall(ctx context.Context, backend sitelDECTBackend, device USBDevice, archive *sitelDECTArchive, state *firmwareRecoveryState, save func() error, progress func(byte, int, int)) error {
	if backend == nil || state == nil || save == nil || state.ArchiveSHA256 == "" || device.attachment == nil || device.VendorID != JabraVendorID || device.ViaDongle {
		return errors.New("incomplete DECT installation plan")
	}
	if err := archive.validate(); err != nil {
		return err
	}
	checkpoint := func(phase string) error { state.Phase = phase; return save() }
	profile := archive.Profile
	if state.USBSerialSHA256 != "" && state.USBSerialSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(device.Serial))) {
		return errors.New("DECT USB serial changed during recovery")
	}
	if profile.runtime(device.ProductID) {
		identity, err := backend.runtime(ctx, device, archive)
		if err != nil {
			return err
		}
		if archive.Engage75 != nil && identity.RadioVersion == "" && (state.Protocol != 4 || state.SitelDECT == nil || state.SitelDECT.RadioRevision == 0 || state.Phase == "entering-bootloader") {
			return errors.New("engage 75 radio version is unavailable without a bound recovery record")
		}
		if err := bindSitelDECTRecovery(state, identity, device, archive); err != nil {
			return err
		}
		if state.Phase == "booting-runtime" && dectVersionsMatch(identity, archive) && (archive.Engage75 == nil || state.SitelDECT.SettingsVerified) {
			return nil
		}
		for _, image := range archive.Images {
			if compareVersions(dectComponentVersion(identity, image.File), image.File.Version) > 0 {
				return errors.New("a DECT component is newer than this archive; refusing a downgrade")
			}
		}
		if archive.Engage75 != nil && compareVersions(identity.RadioVersion, archive.Manifest.Version) > 0 {
			return errors.New("engage 75 radio is newer than this archive; refusing a downgrade")
		}
		if err := checkpoint("entering-bootloader"); err != nil {
			return err
		}
		if err := backend.enter(ctx, device, identity.Base.Address); err != nil {
			return err
		}
		wait, cancel := context.WithTimeout(ctx, 45*time.Second)
		next, err := backend.wait(wait, device, profile.BootPID)
		cancel()
		if err != nil {
			return err
		}
		device = next
	} else {
		if device.ProductID != profile.BootPID {
			return errors.New("DECT firmware does not match this USB device")
		}
		if err := validSitelDECTRecovery(*state); err != nil {
			return err
		}
		if state.BootPID != profile.BootPID {
			return errors.New("DECT recovery record belongs to another model")
		}
		if state.USBPort != filepath.Base(device.SysPath) {
			return errors.New("DECT recovery needs its original USB port")
		}
	}
	if err := checkpoint("flashing"); err != nil {
		return err
	}
	connection, err := backend.boot(ctx, device)
	if err != nil {
		return err
	}
	defer func() {
		if connection != nil {
			_ = connection.Close()
		}
	}()
	data, err := connection.peer(profile.carrier()).request(ctx, 0, nil)
	if err != nil {
		return err
	}
	info, err := decodeSitelInfo(data)
	if err != nil {
		return err
	}
	if info.ID != uint32(JabraVendorID)<<16|uint32(profile.BootPID) {
		return errors.New("DECT base bootloader identity mismatch")
	}
	if info.Mode == 1 {
		_, err := connection.peer(profile.carrier()).request(ctx, 5, []byte{3})
		if err != nil && (!connection.Root.link.lastWriteComplete || !sitelDisconnect(err)) {
			return err
		}
		_ = connection.Close()
		connection = nil
		wait, cancel := context.WithTimeout(ctx, 45*time.Second)
		next, err := backend.wait(wait, device, profile.BootPID)
		cancel()
		if err != nil {
			return err
		}
		device = next
		connection, err = backend.boot(ctx, device)
		if err != nil {
			return err
		}
	}
	prepared, err := prepareSitelDECTTransfer(ctx, backend, connection, archive)
	if err != nil {
		return err
	}
	if archive.Engage75 != nil {
		if err := transferEngage75Components(ctx, backend, connection, archive, prepared, state, save, progress); err != nil {
			return err
		}
	} else {
		for _, image := range prepared {
			if err := transferDECTPrepared(ctx, image, progress); err != nil {
				return err
			}
		}
	}
	for _, image := range prepared {
		if err := verifySitelImage(ctx, image.Peer, image.Image, image.Info.SectorSize); err != nil {
			return err
		}
	}
	if err := checkpoint("booting-runtime"); err != nil {
		return err
	}
	// The original DECT sequence boots the docked headset before its USB base.
	if _, err := connection.peer(10).request(ctx, 5, []byte{3}); err != nil {
		return err
	}
	_, err = connection.peer(profile.carrier()).request(ctx, 5, []byte{3})
	if err != nil && (!connection.Root.link.lastWriteComplete || !sitelDisconnect(err)) {
		return err
	}
	_ = connection.Close()
	connection = nil
	wait, cancel := context.WithTimeout(ctx, 90*time.Second)
	device, err = backend.wait(wait, device, state.RuntimePID)
	cancel()
	if err != nil {
		return err
	}
	identity, err := backend.runtime(ctx, device, archive)
	if err != nil {
		return err
	}
	if err := bindSitelDECTRecovery(state, identity, device, archive); err != nil {
		return err
	}
	if !dectVersionsMatch(identity, archive) {
		return errors.New("DECT component versions did not verify after restart")
	}
	return nil
}

func verifySitelDECTRelease(ctx context.Context, path string, archive *sitelDECTArchive, pid uint16) error {
	if !archive.Profile.runtime(pid) {
		return errors.New("DECT release target is not a known runtime variant")
	}
	evidence, err := firmwareModelCatalog.FirmwareRelease(ctx, pid, archive.Manifest.Version)
	if err != nil {
		return err
	}
	digest, err := firmwareFileMD5(path)
	if err != nil {
		return err
	}
	if !firmwareReleaseMatchesDevice(digest, pid, evidence) || evidence.HasUnspecifiedFirmwareProtocol || len(evidence.FirmwareProtocols) != 1 || evidence.FirmwareProtocols[0] != 4 {
		return errors.New("DECT firmware requires matching official protocol-4 metadata and checksum")
	}
	return nil
}

func installSitelDECTChecked(snapshot *firmwareSnapshot, accepted bool, validateTarget func() error, preferredPID uint16) error {
	archive, err := loadSitelDECTArchive(snapshot.path)
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
		if err := validSitelDECTRecovery(previous); err != nil {
			return err
		}
		previous.Attempt = state.Attempt
		state = previous
	}
	devices, err := enumerateBoundUSB()
	if err != nil {
		return err
	}
	var matches []USBDevice
	for _, device := range devices {
		if preferredPID != 0 && device.ProductID != preferredPID {
			continue
		}
		if prepared.Recovery && state.USBPort != filepath.Base(device.SysPath) {
			continue
		}
		if archive.Profile.runtime(device.ProductID) || prepared.Recovery && device.ProductID == archive.Profile.BootPID {
			matches = append(matches, device)
		}
	}
	if len(matches) != 1 {
		return fmt.Errorf("connect one matching %s base and dock its headset; found %d bases", archive.Profile.Name, len(matches))
	}
	device := matches[0]
	lookup := device.ProductID
	if lookup == archive.Profile.BootPID {
		lookup = state.RuntimePID
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	metadata, finish := context.WithTimeout(ctx, MetadataTimeout)
	err = verifySitelDECTRelease(metadata, snapshot.path, archive, lookup)
	finish()
	if err != nil {
		return err
	}
	if !accepted {
		word := "INSTALL"
		if prepared.Recovery {
			word = "RECOVER"
		}
		fmt.Fprintf(os.Stderr, "Firmware: %s %s\nKeep the base connected and the headset docked until the update finishes. End calls and close other Jabra tools first.\n", archive.Profile.Name, archive.Manifest.Version)
		if !confirmFirmwareAction(os.Stdin, os.Stderr, word) {
			return errors.New("DECT firmware install cancelled")
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
	err = runSitelDECTInstall(ctx, nativeSitelDECTBackend{}, device, archive, &state, func() error { return saveFirmwareRecoveryState(state) }, func(target byte, done, total int) {
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
		return fmt.Errorf("DECT update stopped; keep this archive, base and headset for recovery: %w", err)
	}
	if err := clearFirmwareRecoveryState(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s firmware installed and verified.\n", archive.Profile.Name)
	return nil
}

func dectComponentLabel(target byte) string {
	switch target {
	case 1:
		return "Headset firmware"
	case 28:
		return "Headset sounds"
	case 23:
		return "Bluetooth firmware"
	case 24:
		return "Bluetooth settings"
	case 22:
		return "Display firmware"
	case 3, 6, 21:
		return "Base firmware"
	case 7:
		return "USB controller firmware"
	case 5:
		return "Language files"
	case 4:
		return "Graphics"
	case 27:
		return "Base sounds"
	default:
		return "Firmware"
	}
}
