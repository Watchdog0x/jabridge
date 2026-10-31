package firmware

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ControlBinding holds the same USB attachment and open-descriptor checks used
// by firmware, for non-firmware management operations. It never follows a PID
// match to a different port. An available USB serial is captured from sysfs;
// unrelated GNP serial formats are not assumed to be interchangeable with it.
type ControlBinding struct {
	device USBDevice
}

func BindControlDevice(device USBDevice) (*ControlBinding, error) {
	bound, err := bindUSBDevice(device)
	if err != nil {
		return nil, err
	}
	bound.Serial = readTextSysfs(filepath.Join(bound.SysPath, "serial"))
	return &ControlBinding{device: bound}, nil
}

func (b *ControlBinding) Validate() error {
	if b == nil {
		return errors.New("no captured USB control attachment")
	}
	if err := validateUSBDevice(b.device); err != nil {
		return err
	}
	if b.device.Serial != "" && readTextSysfs(filepath.Join(b.device.SysPath, "serial")) != b.device.Serial {
		return errors.New("selected USB control device serial changed")
	}
	return nil
}

func (b *ControlBinding) Open() (*ControlHidraw, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	transport, err := openBoundManagement(b.device)
	if err != nil {
		return nil, err
	}
	if err := b.Validate(); err != nil {
		_ = transport.Close()
		return nil, err
	}
	return transport, nil
}

// OpenReader creates an independent read-only event subscription before a
// command is sent. The caller owns the returned descriptor and its lifetime.
func (b *ControlBinding) OpenReader() (*os.File, ControlLayout, error) {
	if err := b.Validate(); err != nil {
		return nil, ControlLayout{}, err
	}
	path, err := boundManagementPath(b.device)
	if err != nil {
		return nil, ControlLayout{}, err
	}
	transport, err := openControlHidraw(path, unix.O_RDONLY)
	if err != nil {
		return nil, ControlLayout{}, err
	}
	if err := errors.Join(validateOpenedHID(transport.file, b.device), b.Validate()); err != nil {
		_ = transport.Close()
		return nil, ControlLayout{}, err
	}
	return transport.file, transport.Layout, nil
}
