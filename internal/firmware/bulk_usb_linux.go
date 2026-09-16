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
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type bulkUSBLayout struct{ Interface, Input, Output byte }

func parseCameraBulkInterface(descriptors []byte, active byte) (bulkUSBLayout, error) {
	var candidates []bulkUSBLayout
	var current *bulkUSBLayout
	config := byte(0)
	for offset := 0; offset < len(descriptors); {
		if offset+2 > len(descriptors) {
			return bulkUSBLayout{}, errors.New("truncated USB descriptor")
		}
		length := int(descriptors[offset])
		if length < 2 || offset+length > len(descriptors) {
			return bulkUSBLayout{}, errors.New("invalid USB descriptor length")
		}
		d := descriptors[offset : offset+length]
		offset += length
		switch d[1] {
		case 2:
			if length < 9 {
				return bulkUSBLayout{}, errors.New("short USB configuration")
			}
			config = d[5]
			current = nil
		case 4:
			current = nil
			if length < 9 {
				return bulkUSBLayout{}, errors.New("short USB interface")
			}
			if config == active && d[3] == 0 && d[5] == 0xff && d[6] == 0xcc && d[7] == 1 {
				candidates = append(candidates, bulkUSBLayout{Interface: d[2]})
				current = &candidates[len(candidates)-1]
			}
		case 5:
			if length < 7 {
				return bulkUSBLayout{}, errors.New("short USB endpoint")
			}
			if current == nil || d[3]&3 != 2 {
				continue
			}
			packetSize := binary.LittleEndian.Uint16(d[4:6])
			if d[2]&15 == 0 || d[2]&0x70 != 0 || (packetSize != 8 && packetSize != 16 && packetSize != 32 && packetSize != 64 && packetSize != 512 && packetSize != 1024) {
				return bulkUSBLayout{}, errors.New("camera bulk endpoint has an invalid address or packet size")
			}
			if d[2]&0x80 != 0 {
				if current.Input != 0 {
					return bulkUSBLayout{}, errors.New("ambiguous camera bulk input")
				}
				current.Input = d[2]
			} else {
				if current.Output != 0 {
					return bulkUSBLayout{}, errors.New("ambiguous camera bulk output")
				}
				current.Output = d[2]
			}
		}
	}
	if active == 0 || len(candidates) != 1 || candidates[0].Input == 0 || candidates[0].Output == 0 {
		return bulkUSBLayout{}, errors.New("need one camera bulk interface FF:CC:01 with both endpoints")
	}
	return candidates[0], nil
}

type nativeCameraBulk struct {
	usb    *dfuUSB
	layout bulkUSBLayout
}

func openCameraBulk(device USBDevice) (*nativeCameraBulk, error) {
	if _, ok := bulkCameraProfileForPID(device.ProductID); !ok || device.VendorID != JabraVendorID || device.ViaDongle {
		return nil, errors.New("not a supported USB bulk camera")
	}
	if err := validateUSBDevice(device); err != nil {
		return nil, err
	}
	descriptors, err := os.ReadFile(filepath.Join(device.SysPath, "descriptors"))
	if err != nil {
		return nil, err
	}
	config, err := strconv.ParseUint(readTextSysfs(filepath.Join(device.SysPath, "bConfigurationValue")), 10, 8)
	if err != nil {
		return nil, err
	}
	layout, err := parseCameraBulkInterface(descriptors, byte(config))
	if err != nil {
		return nil, err
	}
	usb, err := openDFUUSB(device)
	if err != nil {
		return nil, err
	}
	if err := usb.Claim(layout.Interface, false); err != nil {
		_ = usb.Close()
		return nil, err
	}
	if err := validateUSBDevice(device); err != nil {
		_ = usb.Close()
		return nil, err
	}
	return &nativeCameraBulk{usb: usb, layout: layout}, nil
}

func (b *nativeCameraBulk) transfer(ctx context.Context, endpoint byte, data []byte, timeout time.Duration) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(data) != bulkPacketSize {
		return 0, errors.New("invalid camera USB transfer length")
	}
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline))
	}
	var pinned runtime.Pinner
	pinned.Pin(&data[0])
	defer pinned.Unpin()
	request := struct {
		Endpoint, Length, Timeout uint32
		Data                      uintptr
	}{uint32(endpoint), uint32(len(data)), uint32(max(1, timeout.Milliseconds())), uintptr(unsafe.Pointer(&data[0]))}
	n, _, errno := syscall.Syscall(syscall.SYS_IOCTL, b.usb.file.Fd(), 0xc0005502|unsafe.Sizeof(request)<<16, uintptr(unsafe.Pointer(&request)))
	runtime.KeepAlive(data)
	runtime.KeepAlive(b.usb.file)
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}
func (b *nativeCameraBulk) Write(ctx context.Context, packet []byte) error {
	if len(packet) != bulkPacketSize || packet[0] != 0xaa || packet[1] < 1 || packet[1] > 4 {
		return errors.New("invalid camera USB command")
	}
	if packet[1] != 4 {
		if err := requireHardwareWrites(); err != nil {
			return err
		}
	}
	n, err := b.transfer(ctx, b.layout.Output, packet, 5*time.Second)
	if err != nil {
		return err
	}
	if n != len(packet) {
		return errors.New("short camera USB write")
	}
	return nil
}
func (b *nativeCameraBulk) Read(ctx context.Context) ([]byte, error) {
	packet := make([]byte, bulkPacketSize)
	for {
		n, err := b.transfer(ctx, b.layout.Input, packet, 250*time.Millisecond)
		if errors.Is(err, unix.ETIMEDOUT) || errors.Is(err, unix.EINTR) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if n < 6 || n > len(packet) {
			return nil, fmt.Errorf("invalid camera USB reply size %d", n)
		}
		return packet[:n], nil
	}
}
func (b *nativeCameraBulk) Close() error { return b.usb.Close() }
