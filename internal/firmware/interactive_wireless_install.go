package firmware

import (
	"encoding/hex"
	"errors"
	"path/filepath"
)

// Captured while the service owns HID. No direct HID traffic is needed to
// download and prepare an update; identities are re-read after service handoff.
type WirelessFirmwareSelection struct {
	ParentPID, ChildPID uint16
	ChildIdentity       string
	ParentAttachment    string
}

func (s WirelessFirmwareSelection) validate() error {
	digest, err := hex.DecodeString(s.ChildIdentity)
	if !SupportsWirelessFirmware(s.ParentPID, s.ChildPID) || err != nil || len(digest) != 32 || s.ParentAttachment == "" {
		return errors.New("wireless headset identity is not ready; wait for the device check")
	}
	current, err := CaptureInstallAttachment(s.ParentPID)
	if err != nil {
		return err
	}
	if current != s.ParentAttachment {
		return errors.New("wireless firmware adapter changed; select the headset again")
	}
	return nil
}

func PrepareWirelessInteractiveInstall(path string, selection WirelessFirmwareSelection) (*PreparedInstall, error) {
	if err := selection.validate(); err != nil {
		return nil, err
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	snapshot, err := freezeFirmwareFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = snapshot.Close() }()
	if _, err := loadSitelOTAArchive(snapshot.path); err != nil {
		return nil, err
	}
	return &PreparedInstall{path: path, pid: selection.ChildPID, archiveSHA: snapshot.digest, wireless: &selection}, nil
}

func (p *PreparedInstall) validateWireless() error {
	if err := p.wireless.validate(); err != nil {
		return err
	}
	snapshot, err := freezeFirmwareFile(p.path)
	if err != nil {
		return err
	}
	defer func() { _ = snapshot.Close() }()
	if snapshot.digest != p.archiveSHA {
		return errors.New("wireless firmware file changed after selection")
	}
	_, err = loadSitelOTAArchive(snapshot.path)
	return err
}
