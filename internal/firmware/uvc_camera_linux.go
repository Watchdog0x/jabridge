package firmware

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

type cameraUVC interface {
	Command(context.Context, byte, uint32, []byte, int) ([]byte, error)
}
type nativeCameraUVC struct {
	file   *os.File
	device USBDevice
}

func cameraVideoPaths(device USBDevice) ([]string, error) {
	if err := validateUSBDevice(device); err != nil {
		return nil, err
	}
	nodes, err := os.ReadDir("/sys/class/video4linux")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, node := range nodes {
		if !strings.HasPrefix(node.Name(), "video") {
			continue
		}
		parent, err := filepath.EvalSymlinks(filepath.Join("/sys/class/video4linux", node.Name(), "device"))
		if err == nil && strings.HasPrefix(parent, device.attachment.realPath+string(filepath.Separator)) {
			paths = append(paths, filepath.Join("/dev", node.Name()))
		}
	}
	return paths, nil
}

func validateCameraVideoHandle(file *os.File, device USBDevice) error {
	if err := validateUSBDevice(device); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return errors.New("camera video handle is not a character device")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || unix.Major(uint64(stat.Rdev)) != 81 {
		return errors.New("camera handle is not a video node")
	}
	parent, err := filepath.EvalSymlinks(fmt.Sprintf("/sys/dev/char/%d:%d/device", unix.Major(uint64(stat.Rdev)), unix.Minor(uint64(stat.Rdev))))
	if err != nil || !strings.HasPrefix(parent, device.attachment.realPath+string(filepath.Separator)) {
		return errors.New("opened video handle does not belong to the selected USB attachment")
	}
	return nil
}

func openCameraUVC(ctx context.Context, device USBDevice) (*nativeCameraUVC, error) {
	if !uvcCameraPID(device.ProductID) || device.VendorID != JabraVendorID || device.ViaDongle {
		return nil, errors.New("not a supported PanaCast 20 USB device")
	}
	if err := validateUSBDevice(device); err != nil {
		return nil, err
	}
	paths, err := cameraVideoPaths(device)
	if err != nil {
		return nil, err
	}
	var selected *nativeCameraUVC
	var failures []error
	for _, path := range paths {
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		file := os.NewFile(uintptr(fd), path)
		if err := validateCameraVideoHandle(file, device); err != nil {
			_ = file.Close()
			failures = append(failures, err)
			continue
		}
		// Metadata nodes belong to the same camera but cannot be used to send
		// extension controls. Require a video-capture capability on the FD.
		var capability [104]byte
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), 0x80685600, uintptr(unsafe.Pointer(&capability[0])))
		caps := binary.LittleEndian.Uint32(capability[84:88])
		if caps&0x80000000 != 0 {
			caps = binary.LittleEndian.Uint32(capability[88:92])
		}
		if errno != 0 || caps&(1|0x1000) == 0 {
			_ = file.Close()
			continue
		}
		camera := &nativeCameraUVC{file: file, device: device}
		if err := camera.checkControls(ctx); err != nil {
			_ = file.Close()
			failures = append(failures, err)
			continue
		}
		if selected != nil {
			_ = file.Close()
			_ = selected.Close()
			return nil, errors.New("multiple PanaCast 20 firmware video interfaces")
		}
		selected = camera
	}
	if selected == nil {
		return nil, errors.Join(append([]error{errors.New("PanaCast 20 firmware video interface is unavailable; run jabridge setup and reconnect the camera")}, failures...)...)
	}
	return selected, nil
}
func (c *nativeCameraUVC) Close() error { return c.file.Close() }
func (c *nativeCameraUVC) query(ctx context.Context, selector, query byte, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data) == 0 || len(data) > 1031 {
		return errors.New("invalid camera extension-control length")
	}
	if err := validateCameraVideoHandle(c.file, c.device); err != nil {
		return err
	}
	if query == 1 {
		if err := requireHardwareWrites(); err != nil {
			return err
		}
	}
	var pin runtime.Pinner
	pin.Pin(&data[0])
	defer pin.Unpin()
	request := struct {
		Unit, Selector, Query byte
		Size                  uint16
		Data                  uintptr
	}{4, selector, query, uint16(len(data)), uintptr(unsafe.Pointer(&data[0]))}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, c.file.Fd(), 0xc0007521|unsafe.Sizeof(request)<<16, uintptr(unsafe.Pointer(&request)))
	runtime.KeepAlive(data)
	runtime.KeepAlive(c.file)
	if errno != 0 {
		return errno
	}
	return ctx.Err()
}
func (c *nativeCameraUVC) checkControls(ctx context.Context) error {
	for _, control := range []struct {
		selector byte
		size     uint16
	}{{9, 7}, {10, 256}, {11, 263}} {
		length := make([]byte, 2)
		if err := c.query(ctx, control.selector, 0x85, length); err != nil {
			return err
		}
		if binary.LittleEndian.Uint16(length) != control.size {
			return errors.New("PanaCast 20 extension-control size does not match the updater")
		}
	}
	return nil
}
func (c *nativeCameraUVC) Command(ctx context.Context, op byte, address uint32, data []byte, response int) ([]byte, error) {
	selector, packet, err := uvcVendorPacket(op, address, data, response)
	if err != nil {
		return nil, err
	}
	if err := c.query(ctx, selector, 1, packet); err != nil {
		return nil, err
	}
	if response == 0 {
		return nil, nil
	}
	result := make([]byte, 256)
	if err := c.query(ctx, 10, 0x81, result); err != nil {
		return nil, err
	}
	return result[:response], nil
}
