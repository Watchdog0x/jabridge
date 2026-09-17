package headsetvolume

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

// Attachment is captured when the service discovers this USB device. Keeping
// that original directory identity prevents a replacement at the same port
// from inheriting a command intended for the previous headset.
type Attachment struct {
	path         string
	info         os.FileInfo
	bus, address int
	profile      usbProfile
}

func address(path string) (int, int, error) {
	read := func(name string) (int, error) {
		b, err := os.ReadFile(filepath.Join(path, name))
		if err != nil {
			return 0, err
		}
		return strconv.Atoi(strings.TrimSpace(string(b)))
	}
	bus, err := read("busnum")
	if err != nil || bus < 1 || bus > 999 {
		return 0, 0, errors.New("invalid headset USB bus")
	}
	device, err := read("devnum")
	if err != nil || device < 1 || device > 127 {
		return 0, 0, errors.New("invalid headset USB address")
	}
	return bus, device, nil
}

func Capture(path string) (*Attachment, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return nil, errors.New("headset USB attachment is unavailable")
	}
	bus, device, err := address(real)
	if err != nil {
		return nil, err
	}
	pidBytes, err := os.ReadFile(filepath.Join(real, "idProduct"))
	if err != nil {
		return nil, err
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(string(pidBytes)), 16, 16)
	profile, known := profileForPID(uint16(pid))
	if err != nil || !known {
		return nil, errors.New("no headset volume profile for this USB model")
	}
	a := &Attachment{path: real, info: info, bus: bus, address: device, profile: profile}
	if err := a.validate(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Attachment) validate() error {
	if a == nil {
		return errors.New("headset attachment was not captured; reconnect the headset")
	}
	info, err := os.Stat(a.path)
	if err != nil || !os.SameFile(a.info, info) {
		return errors.New("headset USB attachment changed; reconnect and select it again")
	}
	bus, device, err := address(a.path)
	if err != nil || bus != a.bus || device != a.address {
		return errors.New("headset USB address changed; select it again")
	}
	for name, wanted := range map[string]uint64{"idVendor": VendorID, "idProduct": uint64(a.profile.pid), "bcdDevice": 0x0111} {
		b, err := os.ReadFile(filepath.Join(a.path, name))
		if err != nil {
			return err
		}
		value, err := strconv.ParseUint(strings.TrimSpace(string(b)), 16, 16)
		if err != nil || value != wanted {
			return errors.New("headset USB identity does not match the volume profile")
		}
	}
	return nil
}

type USB struct {
	file       *os.File
	attachment *Attachment
}

// Descriptor reads Linux's cached USB descriptors only. Debug reports can use
// this without issuing a volume query or initializing firmware volume handling.
func (a *Attachment) Descriptor() (Descriptor, error) {
	if err := a.validate(); err != nil {
		return Descriptor{}, err
	}
	data, err := os.ReadFile(filepath.Join(a.path, "descriptors"))
	if err != nil {
		return Descriptor{}, err
	}
	info, err := Inspect(data)
	if err != nil {
		return Descriptor{}, err
	}
	if binary.LittleEndian.Uint16(data[10:]) != a.profile.pid {
		return Descriptor{}, errors.New("cached headset descriptors belong to a different model")
	}
	if err := a.validate(); err != nil {
		return Descriptor{}, err
	}
	return info, nil
}

// Open sends only a standard device-descriptor read. No interface is claimed,
// no kernel driver is detached, and no USB configuration or reset is issued.
func Open(ctx context.Context, a *Attachment, pid uint16, version string) (*USB, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if a.profile.pid != pid || !Supported(pid, version) {
		return nil, errors.New("direct headset volume is not validated for this firmware")
	}
	if _, err := a.Descriptor(); err != nil {
		return nil, err
	}
	node := fmt.Sprintf("/dev/bus/usb/%03d/%03d", a.bus, a.address)
	fd, err := unix.Open(node, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("headset USB volume access: %w; run jabridge setup and reconnect the headset", err)
	}
	u := &USB{file: os.NewFile(uintptr(fd), node), attachment: a}
	fail := func(err error) (*USB, error) { _ = u.Close(); return nil, err }
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fail(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFCHR || unix.Major(uint64(stat.Rdev)) != 189 || unix.Minor(uint64(stat.Rdev)) != uint32((a.bus-1)*128+a.address-1) {
		return fail(errors.New("opened volume node is not the selected USB device"))
	}
	actual := make([]byte, 18)
	n, err := u.Control(ctx, 0x80, 6, 0x0100, 0, actual)
	if err != nil {
		return fail(err)
	}
	if n != 18 || actual[0] != 18 || actual[1] != 1 || binary.LittleEndian.Uint16(actual[8:]) != VendorID || binary.LittleEndian.Uint16(actual[10:]) != a.profile.pid || binary.LittleEndian.Uint16(actual[12:]) != 0x0111 {
		return fail(errors.New("opened headset volume device has a different identity"))
	}
	if err := a.validate(); err != nil {
		return fail(err)
	}
	return u, nil
}

type usbControl struct {
	kind, request        byte
	value, index, length uint16
	timeout              uint32
	data                 uintptr
}

func (u *USB) Control(ctx context.Context, kind, request byte, value, index uint16, data []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := u.attachment.validate(); err != nil {
		return 0, err
	}
	// The exposed transport cannot issue arbitrary device commands.
	identity := kind == 0x80 && request == 6 && value == 0x0100 && index == 0 && len(data) == 18
	volume := (kind == 0xa2 && request == 0x81 || kind == 0x22 && request == 1) && value == 0x0200 && index == 0x0200 && len(data) == 2
	if !identity && !volume {
		return 0, errors.New("unsupported headset volume USB request")
	}
	if kind == 0x22 {
		code := int16(binary.LittleEndian.Uint16(data))
		if int(code) < minimum || int(code) > maximum {
			return 0, errors.New("headset volume request exceeds its range")
		}
	}
	timeout := 2 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline))
	}
	if timeout <= 0 {
		return 0, context.DeadlineExceeded
	}
	var pinned runtime.Pinner
	pinned.Pin(&data[0])
	defer pinned.Unpin()
	control := usbControl{kind: kind, request: request, value: value, index: index, length: uint16(len(data)), timeout: uint32(max(1, timeout.Milliseconds())), data: uintptr(unsafe.Pointer(&data[0]))}
	n, _, errno := syscall.Syscall(syscall.SYS_IOCTL, u.file.Fd(), 0xc0005500|unsafe.Sizeof(control)<<16, uintptr(unsafe.Pointer(&control)))
	runtime.KeepAlive(data)
	runtime.KeepAlive(u.file)
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

func (u *USB) Close() error { return u.file.Close() }
