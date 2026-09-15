package firmware

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PreparedInstall binds the selected USB attachment and archive before the UI
// gives the terminal to the existing native installer. It does not authorize
// a write: Install still requires the normal INSTALL or RECOVER confirmation.
type PreparedInstall struct {
	path       string
	pid        uint16
	binding    string
	attachment string
	archiveSHA string
}

func PrepareInteractiveInstall(path string, pid uint16, attachment string) (*PreparedInstall, error) {
	if attachment == "" {
		return nil, errors.New("no selected USB attachment")
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
	binding, err := interactiveInstallBinding(snapshot.path, pid)
	if err != nil {
		return nil, err
	}
	plan := &PreparedInstall{path: path, pid: pid, binding: binding, attachment: attachment, archiveSHA: snapshot.digest}
	if err := plan.validate(); err != nil {
		return nil, err
	}
	return plan, nil
}

func (p *PreparedInstall) validate() error {
	if p == nil || p.binding == "" {
		return errors.New("no firmware target prepared")
	}
	attachment, err := CaptureInstallAttachment(p.pid)
	if err != nil {
		return err
	}
	if attachment != p.attachment {
		return errors.New("selected USB device changed; select it again before updating")
	}
	current, err := interactiveInstallBinding(p.path, p.pid)
	if err != nil {
		return err
	}
	if current != p.binding {
		return errors.New("device or firmware file changed; return to Firmware and select the device again")
	}
	return nil
}

// CaptureInstallAttachment is read-only sysfs discovery. Capture it before a
// download so a replacement device cannot inherit an earlier UI selection.
func CaptureInstallAttachment(pid uint16) (string, error) {
	devices, err := enumerateUSB()
	if err != nil {
		return "", err
	}
	device, err := selectInteractiveUSBTarget(devices, pid)
	if err != nil {
		return "", err
	}
	return installAttachmentFingerprint(device)
}

func installAttachmentFingerprint(device USBDevice) (string, error) {
	bound, err := bindUSBDevice(device)
	if err != nil {
		return "", err
	}
	return bound.attachment.fingerprint, nil
}

func (p *PreparedInstall) Install() (err error) {
	defer func() {
		if value := recover(); value != nil {
			if failure, ok := value.(commandFailure); ok {
				err = errors.New(failure.message)
				return
			}
			panic(value)
		}
	}()
	if p == nil {
		return errors.New("no firmware target prepared")
	}
	cmdInstallForTarget([]string{p.path}, p.validate, p.archiveSHA, p.pid)
	return nil
}

func interactiveInstallBinding(path string, pid uint16) (string, error) {
	devices, err := enumerateUSB()
	if err != nil {
		return "", err
	}
	return interactiveInstallBindingForDevices(path, pid, devices)
}

func interactiveInstallBindingForDevices(path string, pid uint16, devices []USBDevice) (string, error) {
	snapshot, err := freezeFirmwareFile(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = snapshot.Close() }()
	path = snapshot.path
	device, err := selectInteractiveUSBTarget(devices, pid)
	if err != nil {
		return "", err
	}
	manifest, err := parseFirmwareManifest(path)
	if err != nil {
		return "", err
	}
	if isSitelManifest(manifest) {
		profile, err := sitelProfileForManifest(manifest)
		if err != nil {
			return "", err
		}
		if !profile.runtime(pid) && pid != profile.BootPID {
			return "", fmt.Errorf("this firmware is for %s; it does not match the selected device (0b0e:%04x)", profile.Name, pid)
		}
		if _, _, err := loadSitelImages(path); err != nil {
			return "", err
		}
	} else if isUSBDFUManifest(manifest) {
		image, err := loadJabraDFUImage(path)
		if err != nil {
			return "", err
		}
		selected, err := selectUSBDFUTarget(devices, image.Profile)
		if err != nil {
			return "", err
		}
		if selected.SysPath != device.SysPath {
			return "", errors.New("firmware does not target the selected USB device")
		}
	} else {
		check := validateNativeCSRArchive
		if isExtendedCSRManifest(manifest) {
			check = validateExtendedCSRArchive
		}
		if err := check(path); err != nil {
			return "", err
		}
		pids, err := parseTargetPIDs(manifest.TargetUSBPIDs)
		if err != nil {
			return "", err
		}
		// Sibling PIDs need explicit official release evidence, not a name
		// match. The selected PID is also passed to the installer itself.
		if pids[0] != pid {
			if !isExtendedCSRManifest(manifest) {
				return "", errors.New("this archive cannot be routed to the selected device by the native installer")
			}
			if _, err := validatedCSRProtocol(path, device); err != nil {
				return "", err
			}
		}
	}
	digest, err := firmwareArchiveSHA256(path)
	if err != nil {
		return "", err
	}
	bus, err := os.ReadFile(filepath.Join(device.SysPath, "busnum"))
	if err != nil {
		return "", err
	}
	address, err := os.ReadFile(filepath.Join(device.SysPath, "devnum"))
	if err != nil {
		return "", err
	}
	// Replugging changes devnum. Neither a sibling model, another USB port,
	// nor a replacement archive may consume the previous confirmation.
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%04x\n%s\n%s\n%s\n%s", pid, device.SysPath, strings.TrimSpace(string(bus)), strings.TrimSpace(string(address)), digest)))), nil
}

func selectInteractiveUSBTarget(devices []USBDevice, pid uint16) (USBDevice, error) {
	var matches []USBDevice
	for _, device := range devices {
		if device.VendorID == JabraVendorID && device.ProductID == pid && !device.ViaDongle {
			matches = append(matches, device)
		}
	}
	if len(matches) != 1 {
		return USBDevice{}, fmt.Errorf("connect exactly one 0b0e:%04x directly by USB for this update; found %d", pid, len(matches))
	}
	return matches[0], nil
}
