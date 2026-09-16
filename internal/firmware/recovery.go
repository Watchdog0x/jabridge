package firmware

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

const firmwareRecoveryStateVersion = 1

type firmwareRecoveryState struct {
	FormatVersion            int                 `json:"formatVersion"`
	ArchiveSHA256            string              `json:"archiveSha256"`
	ProductName              string              `json:"productName"`
	FirmwareVersion          string              `json:"firmwareVersion"`
	TargetUSBPIDs            []string            `json:"targetUsbPids"`
	Attempt                  int                 `json:"attempt"`
	StartedAt                string              `json:"startedAt"`
	USBPort                  string              `json:"usbPort,omitempty"`
	TargetIdentitySHA256     string              `json:"targetIdentitySha256,omitempty"`
	Language                 uint16              `json:"language,omitempty"`
	Protocol                 int                 `json:"protocol,omitempty"`
	RuntimePID               uint16              `json:"runtimePid,omitempty"`
	BootPID                  uint16              `json:"bootPid,omitempty"`
	Phase                    string              `json:"phase,omitempty"`
	ControllerIdentitySHA256 string              `json:"controllerIdentitySha256,omitempty"`
	USBSerialSHA256          string              `json:"usbSerialSha256,omitempty"`
	Conexant                 *conexantRecovery   `json:"conexant,omitempty"`
	SitelOTA                 *sitelOTARecovery   `json:"sitelOta,omitempty"`
	SitelDECT                *sitelDECTRecovery  `json:"sitelDect,omitempty"`
	Camera                   *cameraRecovery     `json:"camera,omitempty"`
	UVCCamera                *uvcCameraRecovery  `json:"uvcCamera,omitempty"`
	PanaCast50               *panacast50Recovery `json:"panaCast50,omitempty"`
}

type firmwareTransferPreparation struct {
	State    firmwareRecoveryState
	Recovery bool
}

func prepareFirmwareTransfer(path string, manifest *BuildVector) (firmwareTransferPreparation, error) {
	if manifest == nil {
		return firmwareTransferPreparation{}, errors.New("firmware manifest is missing")
	}
	digest, err := firmwareArchiveSHA256(path)
	if err != nil {
		return firmwareTransferPreparation{}, err
	}
	targets, err := canonicalTargetPIDs(manifest.TargetUSBPIDs)
	if err != nil {
		return firmwareTransferPreparation{}, err
	}
	wanted := firmwareRecoveryState{
		FormatVersion: firmwareRecoveryStateVersion,
		ArchiveSHA256: digest, ProductName: manifest.ProductName,
		FirmwareVersion: manifest.Version, TargetUSBPIDs: targets,
		Attempt: 1, StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	previous, err := loadFirmwareRecoveryState()
	if errors.Is(err, fs.ErrNotExist) {
		return firmwareTransferPreparation{State: wanted}, nil
	}
	if err != nil {
		return firmwareTransferPreparation{}, err
	}
	if !sameFirmwareRecoveryTarget(previous, wanted) {
		return firmwareTransferPreparation{}, fmt.Errorf(
			"unfinished %s %s transfer expects archive SHA-256 %s; use that exact archive before starting another update",
			previous.ProductName, previous.FirmwareVersion, previous.ArchiveSHA256,
		)
	}
	wanted.Attempt = previous.Attempt + 1
	return firmwareTransferPreparation{State: wanted, Recovery: true}, nil
}

func sameFirmwareRecoveryTarget(left, right firmwareRecoveryState) bool {
	return left.FormatVersion == firmwareRecoveryStateVersion &&
		left.ArchiveSHA256 == right.ArchiveSHA256 &&
		left.ProductName == right.ProductName &&
		left.FirmwareVersion == right.FirmwareVersion &&
		reflect.DeepEqual(left.TargetUSBPIDs, right.TargetUSBPIDs)
}

func firmwareArchiveSHA256(path string) (string, error) {
	file, info, err := openFirmwareRead(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, MaxFirmwareSize+1))
	if err != nil {
		return "", err
	}
	if written != info.Size() {
		return "", errors.New("firmware archive changed while hashing")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func canonicalTargetPIDs(values []string) ([]string, error) {
	pids, err := parseTargetPIDs(values)
	if err != nil {
		return nil, err
	}
	targets := make([]string, 0, len(pids))
	for _, pid := range pids {
		targets = append(targets, fmt.Sprintf("0x%04X", pid))
	}
	sort.Strings(targets)
	return targets, nil
}

func firmwareRecoveryStatePath() (string, error) {
	directory := strings.TrimSpace(os.Getenv("JABRIDGE_FIRMWARE_STATE_DIR"))
	if directory == "" {
		directory = strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	}
	if directory == "" {
		homeDirectory, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		directory = filepath.Join(homeDirectory, ".local", "state")
	}
	if !filepath.IsAbs(directory) {
		return "", errors.New("firmware state directory must be absolute")
	}
	return filepath.Join(directory, "jabridge", "firmware-recovery.json"), nil
}

func loadFirmwareRecoveryState() (firmwareRecoveryState, error) {
	path, err := firmwareRecoveryStatePath()
	if err != nil {
		return firmwareRecoveryState{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return firmwareRecoveryState{}, err
	}
	if !info.Mode().IsRegular() || info.Size() < 2 || info.Size() > 16*1024 {
		return firmwareRecoveryState{}, fmt.Errorf("invalid firmware recovery state file %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return firmwareRecoveryState{}, err
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(io.LimitReader(file, 16*1024+1))
	decoder.DisallowUnknownFields()
	var state firmwareRecoveryState
	if err := decoder.Decode(&state); err != nil {
		return firmwareRecoveryState{}, fmt.Errorf("decode firmware recovery state: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return firmwareRecoveryState{}, errors.New("firmware recovery state has trailing data")
	}
	if state.FormatVersion != firmwareRecoveryStateVersion || len(state.ArchiveSHA256) != sha256.Size*2 ||
		state.ProductName == "" || state.FirmwareVersion == "" || len(state.TargetUSBPIDs) == 0 || state.Attempt < 1 {
		return firmwareRecoveryState{}, errors.New("firmware recovery state is incomplete")
	}
	if state.Protocol == 4 && state.SitelDECT != nil {
		if err := validSitelDECTRecovery(state); err != nil {
			return firmwareRecoveryState{}, err
		}
	} else if state.SitelDECT != nil {
		return firmwareRecoveryState{}, errors.New("unexpected DECT recovery record")
	} else if state.Protocol == 4 {
		profile, ok := sitelProfileForPID(state.RuntimePID)
		pids, err := parseTargetPIDs(state.TargetUSBPIDs)
		if !ok || !profile.runtime(state.RuntimePID) || state.BootPID != profile.BootPID || state.USBPort == "" || err != nil || len(pids) != 1 || pids[0] != profile.BootPID {
			return firmwareRecoveryState{}, errors.New("sitel recovery identity does not match the firmware target")
		}
		if !engageHasController(state.RuntimePID) && state.ControllerIdentitySHA256 != "" {
			return firmwareRecoveryState{}, errors.New("unexpected controller in Sitel recovery record")
		}
		switch state.Phase {
		case "entering-bootloader", "flashing", "booting-runtime", "controller":
		default:
			return firmwareRecoveryState{}, errors.New("invalid Sitel recovery phase")
		}
		for _, value := range []string{state.TargetIdentitySHA256, state.ControllerIdentitySHA256, state.USBSerialSHA256} {
			if value == "" {
				continue
			}
			decoded, err := hex.DecodeString(value)
			if err != nil || len(decoded) != sha256.Size {
				return firmwareRecoveryState{}, errors.New("invalid Sitel recovery identity digest")
			}
		}
		if state.TargetIdentitySHA256 == "" {
			return firmwareRecoveryState{}, errors.New("sitel recovery headset identity is missing")
		}
	}
	if state.Protocol == 5 {
		if err := validConexantRecovery(state); err != nil {
			return firmwareRecoveryState{}, err
		}
	} else if state.Conexant != nil {
		return firmwareRecoveryState{}, errors.New("unexpected UC Voice recovery record")
	}
	if state.Protocol == 12 {
		if err := validSitelOTARecovery(state); err != nil {
			return firmwareRecoveryState{}, err
		}
	} else if state.SitelOTA != nil {
		return firmwareRecoveryState{}, errors.New("unexpected wireless Engage recovery record")
	}
	if state.Protocol == 18 {
		if err := validCameraRecovery(state); err != nil {
			return firmwareRecoveryState{}, err
		}
	} else if state.Camera != nil {
		return firmwareRecoveryState{}, errors.New("unexpected camera recovery record")
	}
	if state.Protocol == 11 {
		if err := validUVCCameraRecovery(state); err != nil {
			return firmwareRecoveryState{}, err
		}
	} else if state.UVCCamera != nil {
		return firmwareRecoveryState{}, errors.New("unexpected PanaCast 20 recovery record")
	}
	if state.Protocol == 10 || state.Protocol == 13 {
		if err := validPanaCast50Recovery(state); err != nil {
			return firmwareRecoveryState{}, err
		}
	} else if state.PanaCast50 != nil {
		return firmwareRecoveryState{}, errors.New("unexpected PanaCast 50 recovery record")
	}
	return state, nil
}

func saveFirmwareRecoveryState(state firmwareRecoveryState) error {
	path, err := firmwareRecoveryStatePath()
	if err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular firmware recovery state %s", path)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	newParents, err := createFirmwareStateDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".firmware-recovery-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		_ = temporary.Close()
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	cleanup = false
	// The rename itself must be durable before an installer changes hardware.
	// Syncing only the temporary file does not persist its directory entry.
	if err := syncFirmwareStateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	for _, parent := range newParents {
		if err := syncFirmwareStateDirectory(parent); err != nil {
			return err
		}
	}
	return nil
}

func createFirmwareStateDirectory(path string) ([]string, error) {
	var parents []string
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return nil, errors.New("firmware state parent is not a directory")
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil, err
		}
		parents = append(parents, parent)
	}
	return parents, os.MkdirAll(path, 0o700)
}

func syncFirmwareStateDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func clearFirmwareRecoveryState() error {
	path, err := firmwareRecoveryStatePath()
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to remove non-regular firmware recovery state %s", path)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncFirmwareStateDirectory(filepath.Dir(path))
}
