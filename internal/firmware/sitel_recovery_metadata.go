package firmware

import (
	"errors"
	"fmt"
	"io/fs"
)

// SitelRecoveryInfo contains only public model/version and stage data suitable
// for a debug report. Private identities, paths and hashes are never exposed.
type SitelRecoveryInfo struct {
	UpdateMode     bool
	RuntimePID     uint16
	Version, Phase string
}

func ReadSitelRecoveryInfo(pid uint16) (SitelRecoveryInfo, error) {
	state, recovery, err := sitelRecoveryForPID(pid)
	info := SitelRecoveryInfo{UpdateMode: recovery}
	if err != nil || !recovery {
		return info, err
	}
	info.RuntimePID, info.Version, info.Phase = state.RuntimePID, state.FirmwareVersion, state.Phase
	return info, nil
}

// Bootloader PIDs are absent from the model catalog. Use the recorded runtime
// variant and exact unfinished release; a sibling PID or today's latest release
// must not be guessed. Installation separately validates the live USB binding.
func sitelRecoveryForPID(pid uint16) (firmwareRecoveryState, bool, error) {
	profile, ok := sitelProfileForPID(pid)
	if !ok || profile.BootPID != pid {
		return firmwareRecoveryState{}, false, nil
	}
	state, err := loadFirmwareRecoveryState()
	if errors.Is(err, fs.ErrNotExist) {
		return state, true, errors.New("headset is in firmware update mode, but its saved recovery record is missing")
	}
	if err != nil {
		return state, true, fmt.Errorf("read firmware recovery record: %w", err)
	}
	if state.Protocol != 4 || state.BootPID != pid || !profile.runtime(state.RuntimePID) || state.TargetIdentitySHA256 == "" {
		return state, true, errors.New("saved firmware recovery record does not match this device")
	}
	return state, true, nil
}

func verifyRecoveryDownload(pid uint16, path string) error {
	state, recovery, err := sitelRecoveryForPID(pid)
	if err != nil || !recovery {
		return err
	}
	digest, err := firmwareArchiveSHA256(path)
	if err != nil {
		return err
	}
	if digest != state.ArchiveSHA256 {
		return errors.New("downloaded firmware differs from the unfinished update; recovery requires the original archive")
	}
	return nil
}
