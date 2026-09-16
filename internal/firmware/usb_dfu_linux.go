package firmware

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type dfuUSB struct {
	file       *os.File
	intf       uint32
	claimed    bool
	disconnect bool
}

func dfuDisconnectError(err error) bool {
	return errors.Is(err, unix.ENODEV) || errors.Is(err, unix.ENXIO) || errors.Is(err, unix.ESHUTDOWN)
}

// Open by a previously selected physical sysfs path, never by "first Jabra".
// Validate the opened USB file's descriptor before claiming or writing to it.
func usbDFUDevicePath(device USBDevice) (string, error) {
	if device.attachment != nil {
		if err := validateUSBDevice(device); err != nil {
			return "", err
		}
		return device.attachment.node, nil
	}
	bus, err := strconv.Atoi(readTextSysfs(filepath.Join(device.SysPath, "busnum")))
	if err != nil || bus < 1 || bus > 999 {
		return "", errors.New("invalid USB bus number")
	}
	address, err := strconv.Atoi(readTextSysfs(filepath.Join(device.SysPath, "devnum")))
	if err != nil || address < 1 || address > 127 {
		return "", errors.New("invalid USB device number")
	}
	return fmt.Sprintf("/dev/bus/usb/%03d/%03d", bus, address), nil
}

// USBFirmwareAccessPaths lists only the USB nodes needed by registered native
// DFU and bulk camera profiles. Opening these nodes sends no USB request.
func USBFirmwareAccessPaths() ([]string, error) {
	devices, err := enumerateUSB()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, device := range devices {
		if uvcCameraPID(device.ProductID) {
			bound, err := bindUSBDevice(device)
			if err != nil {
				return nil, err
			}
			video, err := cameraVideoPaths(bound)
			if err != nil {
				return nil, err
			}
			paths = append(paths, video...)
			continue
		}
		_, dfu := usbDFUProfileForPID(device.ProductID)
		_, camera := bulkCameraProfileForPID(device.ProductID)
		if !dfu && !camera {
			continue
		}
		path, err := usbDFUDevicePath(device)
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func openDFUUSB(device USBDevice) (*dfuUSB, error) {
	if err := validateUSBDevice(device); err != nil {
		return nil, err
	}
	path, err := usbDFUDevicePath(device)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("USB update access: %w; run jabridge setup and reconnect the device", err)
	}
	t := &dfuUSB{file: os.NewFile(uintptr(fd), path)}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	descriptor := make([]byte, 18)
	n, err := t.Control(ctx, 0x80, 6, 0x0100, 0, descriptor)
	if err != nil || n != 18 || descriptor[0] != 18 || descriptor[1] != 1 ||
		binary.LittleEndian.Uint16(descriptor[8:10]) != device.VendorID ||
		binary.LittleEndian.Uint16(descriptor[10:12]) != device.ProductID {
		_ = t.Close()
		return nil, fmt.Errorf("opened USB device identity could not be confirmed: %w", errors.Join(err, errors.New("descriptor mismatch or short read")))
	}
	// Detect re-enumeration between resolving the port and opening its node.
	if current, err := usbDFUDevicePath(device); err != nil || current != path {
		_ = t.Close()
		return nil, errors.New("USB device changed while opening; retry before installing")
	}
	return t, nil
}

func (t *dfuUSB) Claim(number byte, detachKernel bool) error {
	t.intf = uint32(number)
	if detachKernel {
		request := usbfsIoctl{ifno: int32(number), ioctlCode: int32(usbfsDiscCode)}
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, t.file.Fd(),
			0xc0005512|unsafe.Sizeof(request)<<16, uintptr(unsafe.Pointer(&request)))
		if errno != 0 && errno != unix.ENODATA && errno != unix.EINVAL {
			return fmt.Errorf("release HID driver for update: %w", errno)
		}
		t.disconnect = errno == 0
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, t.file.Fd(), usbfsClaimInterface, uintptr(unsafe.Pointer(&t.intf)))
	if errno != 0 {
		return fmt.Errorf("claim USB update interface: %w", errno)
	}
	t.claimed = true
	return nil
}

func (t *dfuUSB) Control(ctx context.Context, kind, request byte, value, index uint16, data []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(data) > 65535 {
		return 0, errors.New("USB control transfer too large")
	}
	timeout := 2 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline))
	}
	var pointer uintptr
	var pinned runtime.Pinner
	if len(data) != 0 {
		pinned.Pin(&data[0])
		defer pinned.Unpin()
		pointer = uintptr(unsafe.Pointer(&data[0]))
	}
	control := usbfsCtrlTransfer{bmRequestType: kind, bRequest: request, wValue: value, wIndex: index,
		wLength: uint16(len(data)), timeout: uint32(max(1, timeout.Milliseconds())), data: pointer}
	n, _, errno := syscall.Syscall(syscall.SYS_IOCTL, t.file.Fd(),
		0xc0005500|unsafe.Sizeof(control)<<16, uintptr(unsafe.Pointer(&control)))
	runtime.KeepAlive(data)
	runtime.KeepAlive(t.file)
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

func (t *dfuUSB) Reset() error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, t.file.Fd(), 0x5514, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func (t *dfuUSB) Close() error {
	if t.claimed {
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, t.file.Fd(), usbfsRelease, uintptr(unsafe.Pointer(&t.intf)))
	}
	if t.disconnect {
		request := usbfsIoctl{ifno: int32(t.intf), ioctlCode: 0x5517} // USBDEVFS_CONNECT
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, t.file.Fd(),
			0xc0005512|unsafe.Sizeof(request)<<16, uintptr(unsafe.Pointer(&request)))
	}
	return t.file.Close()
}

func inspectDFUUSB(device USBDevice) (dfuInterface, error) {
	if err := validateUSBDevice(device); err != nil {
		return dfuInterface{}, err
	}
	descriptors, err := os.ReadFile(filepath.Join(device.SysPath, "descriptors"))
	if err != nil {
		return dfuInterface{}, err
	}
	config, err := strconv.ParseUint(readTextSysfs(filepath.Join(device.SysPath, "bConfigurationValue")), 10, 8)
	if err != nil || config == 0 {
		return dfuInterface{}, errors.New("USB update device has no active configuration")
	}
	return parseDFUInterface(descriptors, byte(config))
}

func dfuControlPath(device USBDevice) (string, byte, error) {
	if err := validateUSBDevice(device); err != nil {
		return "", 0, err
	}
	profile, ok := usbDFUProfileForPID(device.ProductID)
	if !ok || !profile.runtime(device.ProductID) {
		return "", 0, errors.New("no native USB DFU runtime profile for this device")
	}
	root, err := filepath.EvalSymlinks(device.SysPath)
	if err != nil {
		return "", 0, err
	}
	nodes, err := os.ReadDir("/sys/class/hidraw")
	if err != nil {
		return "", 0, err
	}
	var path string
	var number byte
	for _, node := range nodes {
		resolved, err := filepath.EvalSymlinks(filepath.Join("/sys/class/hidraw", node.Name(), "device"))
		if err != nil || !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			continue
		}
		candidate := filepath.Join("/dev", node.Name())
		layout, err := InspectControlLayout(candidate)
		// The descriptor must identify the unambiguous FF00:0001 management
		// interface. Its report ID can differ between runtime variants; the
		// same descriptor-derived layout is used to read the runtime version.
		if err != nil || layout.OutputID == 0 || layout.OutputBytes < 33 || layout.OutputBytes > 65 {
			continue
		}
		if path != "" {
			return "", 0, errors.New("multiple management interfaces; refusing to guess")
		}
		parent := filepath.Dir(resolved)
		value, err := readHexSysfs(filepath.Join(parent, "bInterfaceNumber"))
		if err != nil || value > 255 {
			return "", 0, errors.New("cannot identify management USB interface")
		}
		path, number = candidate, byte(value)
	}
	if path == "" {
		return "", 0, errors.New("model-matched management interface not found")
	}
	return path, number, nil
}
