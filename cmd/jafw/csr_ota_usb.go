// csr_ota_usb — pure-Go USB transport for CSR OTA firmware updates.
//
// Uses direct Linux usbfs ioctls instead of libusb.
// No external dependencies — pure Go stdlib + syscall.
//
// The hidraw transport is used for all normal GNP traffic. This USB
// transport is only needed for the footer partition write where the
// kernel's hidraw write timeout (~5s) is too short. The usbfs
// USBDEVFS_CONTROL ioctl lets us set arbitrary timeouts.
//
// Implementation uses /dev/bus/usb/BUS/DEV with:
//   - USBDEVFS_CLAIMINTERFACE to claim interface 3 (GNP HID)
//   - USBDEVFS_CONTROL for HID SET_REPORT (write with configurable timeout)
//   - USBDEVFS_BULK for interrupt IN reads (also with configurable timeout)
//
// This is exactly what libusb does under the hood on Linux, minus the
// abstraction layers and the C dependency.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// ── usbfs ioctl constants (from linux/usbdevice_fs.h) ─────────────────

const (
	// _IOC encoding: dir(2) | size(14) | type(8) | nr(8)
	// 'U' = 0x55
	usbfsControl        = 0xc0185500 // USBDEVFS_CONTROL — _IOWR('U', 0, struct usbdevfs_ctrltransfer)
	usbfsClaimInterface = 0x8004550f // USBDEVFS_CLAIMINTERFACE — _IOR('U', 15, unsigned int)
	usbfsRelease        = 0x80045510 // USBDEVFS_RELEASEINTERFACE — _IOR('U', 16, unsigned int)
	usbfsBulk           = 0xc0185502 // USBDEVFS_BULK — _IOWR('U', 2, struct usbdevfs_bulktransfer)
	// To detach a kernel driver from a specific interface:
	// ioctl(fd, USBDEVFS_IOCTL, &{ifno, USBDEVFS_DISCONNECT, NULL})
	usbfsIoctlCmd = 0xc0105512 // USBDEVFS_IOCTL — _IOWR('U', 18, struct usbdevfs_ioctl) [16 bytes on 64-bit]
	usbfsDiscCode = 0x5516     // USBDEVFS_DISCONNECT as ioctl_code inside usbdevfs_ioctl
)

// usbfsCtrlTransfer matches struct usbdevfs_ctrltransfer from the kernel.
type usbfsCtrlTransfer struct {
	bmRequestType uint8
	bRequest      uint8
	wValue        uint16
	wIndex        uint16
	wLength       uint16
	timeout       uint32 // milliseconds
	data          uintptr
}

// usbfsBulkTransfer matches struct usbdevfs_bulktransfer.
type usbfsBulkTransfer struct {
	ep      uint32
	len     uint32
	timeout uint32 // milliseconds
	data    uintptr
}

// usbfsIoctl matches struct usbdevfs_ioctl.
type usbfsIoctl struct {
	ifno      int32
	ioctlCode int32
	data      uintptr
}

// UsbfsTransport implements OtaTransport using direct Linux usbfs ioctls.
// Pure Go — no libusb, no cgo.
type UsbfsTransport struct {
	f       *os.File
	intfNum int
	timeout time.Duration
}

// OpenUsbfs opens a Jabra device via the Linux usbfs interface and claims
// the GNP HID interface (interface 3). The kernel HID driver is detached
// automatically via USBDEVFS_DISCONNECT.
//
// vid/pid identify the device. timeout is the per-transfer timeout for
// both reads and writes (set to 30s for footer writes).
func OpenUsbfs(vid, pid uint16, timeout time.Duration) (*UsbfsTransport, error) {
	devPath, err := findUsbDevPath(vid, pid)
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(devPath, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", devPath, err)
	}

	intfNum := 3 // GNP HID interface — confirmed from sysfs 1-5.1:1.3

	// Disconnect the kernel HID driver from interface 3 so we can claim it.
	// This uses USBDEVFS_IOCTL (per-interface wrapper) with USBDEVFS_DISCONNECT
	// as the inner ioctl code — the correct Linux way to detach a driver
	// from a specific interface without affecting other interfaces.
	disconn := usbfsIoctl{
		ifno:      int32(intfNum),
		ioctlCode: int32(usbfsDiscCode), // USBDEVFS_DISCONNECT
		data:      0,
	}
	// Ignore errors — the interface might not be claimed by a driver.
	syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), usbfsIoctlCmd,
		uintptr(unsafe.Pointer(&disconn)))

	// Claim interface 3.
	ifNum := uint32(intfNum)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(),
		usbfsClaimInterface, uintptr(unsafe.Pointer(&ifNum)))
	if errno != 0 {
		f.Close()
		return nil, fmt.Errorf("claim interface %d: %w", intfNum, errno)
	}

	return &UsbfsTransport{
		f:       f,
		intfNum: intfNum,
		timeout: timeout,
	}, nil
}

// Write sends a HID output report via USB SET_REPORT control transfer
// on EP 0. The timeout is configurable — unlike hidraw's kernel-level
// timeout which is fixed at ~5s.
//
// SET_REPORT control transfer (USB HID 1.11 spec §7.2.2):
//
//	bmRequestType: 0x21  (class, interface, host-to-device)
//	bRequest:      0x09  (SET_REPORT)
//	wValue:        0x0200 | report_id  (output report type=2, id=5 → 0x0205)
//	wIndex:        interface number (3)
//	data:          report bytes including report ID
func (t *UsbfsTransport) Write(report []byte) error {
	if len(report) < 7 {
		return fmt.Errorf("usbfs write: report too short: %d bytes", len(report))
	}
	reportID := report[0] // 0x05
	wValue := uint16(0x0200) | uint16(reportID)
	timeoutMs := uint32(t.timeout.Milliseconds())

	ctrl := usbfsCtrlTransfer{
		bmRequestType: 0x21,
		bRequest:      0x09,
		wValue:        wValue,
		wIndex:        uint16(t.intfNum),
		wLength:       uint16(len(report)),
		timeout:       timeoutMs,
		data:          uintptr(unsafe.Pointer(&report[0])),
	}

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, t.f.Fd(),
		usbfsControl, uintptr(unsafe.Pointer(&ctrl)))
	if errno != 0 {
		return fmt.Errorf("usbfs SET_REPORT: %w", errno)
	}
	return nil
}

// Read pulls the next HID input report via USB bulk/interrupt transfer.
// Timeout is the per-read deadline. Only returns reports with the GNP
// report ID (0x05) — other HID reports are silently discarded.
func (t *UsbfsTransport) Read(timeout time.Duration) ([]byte, error) {
	timeoutMs := uint32(timeout.Milliseconds())
	buf := make([]byte, 256)

	deadline := time.Now().Add(timeout)
	for {
		bulk := usbfsBulkTransfer{
			ep:      0x81, // interrupt IN endpoint — confirmed from Evolve2 85
			len:     uint32(len(buf)),
			timeout: timeoutMs,
			data:    uintptr(unsafe.Pointer(&buf[0])),
		}

		r1, _, errno := syscall.Syscall(syscall.SYS_IOCTL, t.f.Fd(),
			usbfsBulk, uintptr(unsafe.Pointer(&bulk)))
		if errno != 0 {
			return nil, fmt.Errorf("usbfs read: %w", errno)
		}
		n := int(r1)
		if n > 0 && buf[0] == GnpReportID {
			return append([]byte(nil), buf[:n]...), nil
		}
		// Non-GNP report — discard and retry if within deadline.
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("usbfs read timeout after %s", timeout)
		}
	}
}

// Close releases the interface and closes the device file.
func (t *UsbfsTransport) Close() error {
	if t.f == nil {
		return nil
	}
	// Release interface.
	ifNum := uint32(t.intfNum)
	syscall.Syscall(syscall.SYS_IOCTL, t.f.Fd(),
		usbfsRelease, uintptr(unsafe.Pointer(&ifNum)))
	err := t.f.Close()
	t.f = nil
	return err
}

// findUsbDevPath locates the /dev/bus/usb/BUS/DEV path for a device
// identified by VID:PID. Searches sysfs for the matching device.
func findUsbDevPath(vid, pid uint16) (string, error) {
	root := "/sys/bus/usb/devices"
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", root, err)
	}

	for _, entry := range entries {
		dir := filepath.Join(root, entry.Name())
		vidFile, err := os.ReadFile(filepath.Join(dir, "idVendor"))
		if err != nil {
			continue
		}
		pidFile, err := os.ReadFile(filepath.Join(dir, "idProduct"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(vidFile)) == fmt.Sprintf("%04x", vid) &&
			strings.TrimSpace(string(pidFile)) == fmt.Sprintf("%04x", pid) {
			// Read busnum and devnum for the /dev/bus/usb path.
			busData, err := os.ReadFile(filepath.Join(dir, "busnum"))
			if err != nil {
				continue
			}
			devData, err := os.ReadFile(filepath.Join(dir, "devnum"))
			if err != nil {
				continue
			}
			busNum := strings.TrimSpace(string(busData))
			devNum := strings.TrimSpace(string(devData))
			return fmt.Sprintf("/dev/bus/usb/%03s/%03s", busNum, devNum), nil
		}
	}
	return "", fmt.Errorf("device %04x:%04x not found in sysfs", vid, pid)
}
