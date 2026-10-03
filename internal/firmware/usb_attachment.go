package firmware

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

type usbAttachment struct {
	realPath, node, fingerprint string
	info                        os.FileInfo
	bus, address                int
}

func bindUSBDevice(device USBDevice) (USBDevice, error) {
	if device.attachment != nil {
		return device, validateUSBDevice(device)
	}
	if device.VendorID != JabraVendorID || device.ViaDongle {
		return USBDevice{}, errors.New("firmware requires a directly attached Jabra USB device")
	}
	realPath, err := filepath.EvalSymlinks(device.SysPath)
	if err != nil {
		return USBDevice{}, err
	}
	info, err := os.Stat(realPath)
	if err != nil || !info.IsDir() {
		return USBDevice{}, errors.New("USB attachment is no longer present")
	}
	bus, address, err := usbAddress(realPath)
	if err != nil {
		return USBDevice{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return USBDevice{}, errors.New("cannot identify USB attachment instance")
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%04x:%04x\n%s\n%d:%d\n%d:%d", device.VendorID, device.ProductID, realPath, bus, address, stat.Dev, stat.Ino))))
	device.attachment = &usbAttachment{realPath: realPath, node: fmt.Sprintf("/dev/bus/usb/%03d/%03d", bus, address), fingerprint: fingerprint, info: info, bus: bus, address: address}
	return device, validateUSBDevice(device)
}

func usbAddress(path string) (int, int, error) {
	bus, err := strconv.Atoi(readTextSysfs(filepath.Join(path, "busnum")))
	if err != nil || bus < 1 || bus > 999 {
		return 0, 0, errors.New("invalid USB bus number")
	}
	address, err := strconv.Atoi(readTextSysfs(filepath.Join(path, "devnum")))
	if err != nil || address < 1 || address > 127 {
		return 0, 0, errors.New("invalid USB device number")
	}
	return bus, address, nil
}

func validateUSBDevice(device USBDevice) error {
	binding := device.attachment
	if binding == nil {
		return errors.New("USB device has no captured attachment; select it again")
	}
	realPath, err := filepath.EvalSymlinks(device.SysPath)
	if err != nil || realPath != binding.realPath {
		return errors.New("selected USB device changed")
	}
	info, err := os.Stat(realPath)
	if err != nil || !os.SameFile(info, binding.info) {
		return errors.New("selected USB attachment was replaced")
	}
	bus, address, err := usbAddress(realPath)
	if err != nil || bus != binding.bus || address != binding.address {
		return errors.New("selected USB device reconnected; select it again")
	}
	vid, err := readHexSysfs(filepath.Join(realPath, "idVendor"))
	if err != nil || vid != device.VendorID {
		return errors.New("selected USB vendor changed")
	}
	pid, err := readHexSysfs(filepath.Join(realPath, "idProduct"))
	if err != nil || pid != device.ProductID {
		return errors.New("selected USB model changed")
	}
	return nil
}

func enumerateBoundUSB() ([]USBDevice, error) {
	devices, err := enumerateUSB()
	if err != nil {
		return nil, err
	}
	var bound []USBDevice
	for _, device := range devices {
		if captured, err := bindUSBDevice(device); err == nil {
			bound = append(bound, captured)
		}
	}
	return bound, nil
}

func boundManagementPath(device USBDevice) (string, error) {
	if err := validateUSBDevice(device); err != nil {
		return "", err
	}
	nodes, err := os.ReadDir("/sys/class/hidraw")
	if err != nil {
		return "", err
	}
	var selected string
	for _, node := range nodes {
		resolved, err := filepath.EvalSymlinks(filepath.Join("/sys/class/hidraw", node.Name(), "device"))
		if err != nil || !strings.HasPrefix(resolved, device.attachment.realPath+string(filepath.Separator)) {
			continue
		}
		path := filepath.Join("/dev", node.Name())
		if !HasControlLayout(path) {
			continue
		}
		if selected != "" {
			return "", errors.New("multiple management interfaces on selected USB device")
		}
		selected = path
	}
	if selected == "" {
		return "", errors.New("selected USB device has no management interface")
	}
	return selected, validateUSBDevice(device)
}

// Validate the opened descriptor, not just its reusable /dev/hidrawN name.
func validateOpenedHID(file *os.File, device USBDevice) error {
	return validateOpenedHIDAt(file, device, linuxHidrawPaths().char)
}

func validateOpenedHIDAt(file *os.File, device USBDevice, charRoot string) error {
	if err := validateUSBDevice(device); err != nil {
		return hidAccessFailure("hid-usb-binding", err)
	}
	info, err := file.Stat()
	if err != nil {
		return hidAccessFailure("hid-handle-stat", err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return hidAccessFailure("hid-handle-type", errors.New("opened management handle is not a character device"))
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return hidAccessFailure("hid-handle-stat", errors.New("cannot identify opened management handle"))
	}
	path := filepath.Join(charRoot, fmt.Sprintf("%d:%d", unix.Major(uint64(stat.Rdev)), unix.Minor(uint64(stat.Rdev))), "device")
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !strings.HasPrefix(resolved, device.attachment.realPath+string(filepath.Separator)) {
		return hidAccessFailure("hid-handle-parent", errors.New("opened HID handle does not belong to the selected USB attachment"))
	}
	var raw struct {
		Bus             uint32
		Vendor, Product uint16
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), 0x80084803, uintptr(unsafe.Pointer(&raw))) // HIDIOCGRAWINFO
	if errno != 0 {
		return hidAccessFailure("hid-info-ioctl", errno)
	}
	if raw.Bus != 3 || raw.Vendor != device.VendorID || raw.Product != device.ProductID {
		return hidAccessFailure("hid-info-mismatch", fmt.Errorf("opened HID identity bus=%d VID=%04x PID=%04x does not match selected USB device", raw.Bus, raw.Vendor, raw.Product))
	}
	if err := validateUSBDevice(device); err != nil {
		return hidAccessFailure("hid-usb-binding", err)
	}
	return nil
}

func openBoundManagement(device USBDevice) (*ControlHidraw, error) {
	path, err := boundManagementPath(device)
	if err != nil {
		return nil, err
	}
	transport, err := OpenControlHidraw(path)
	if err != nil {
		return nil, err
	}
	if err := validateOpenedHID(transport.file, device); err != nil {
		_ = transport.Close()
		return nil, err
	}
	return transport, nil
}

func openBoundCSR(device USBDevice) (*HidrawTransport, error) {
	path, err := boundManagementPath(device)
	if err != nil {
		return nil, err
	}
	transport, err := OpenHidraw(path)
	if err != nil {
		return nil, err
	}
	if err := validateOpenedHID(transport.f, device); err != nil {
		_ = transport.Close()
		return nil, err
	}
	return transport, nil
}
