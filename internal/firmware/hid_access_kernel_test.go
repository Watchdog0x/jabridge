package firmware

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// These optional tests use Linux UHID and real hidraw open/ioctl/poll operations.
// Only USB ancestry is synthetic: UHID devices live under the virtual bus.
// They neither connect to nor send commands to a physical Jabra device.
func TestSitelLinuxUHIDOpenPath(t *testing.T) {
	if os.Getenv("JABRIDGE_TEST_UHID") != "1" {
		t.Skip("set JABRIDGE_TEST_UHID=1 with access to /dev/uhid")
	}
	for _, failure := range []string{"none", "descriptor", "identity", "parent", "missing-node", "changed-attachment"} {
		t.Run(failure, func(t *testing.T) {
			descriptor := []byte{0x06, 0x54, 0xff, 0x09, 1, 0xa1, 1, 0x15, 0x80, 0x25, 0x7f, 0x75, 8, 0x95, 64, 0x09, 1, 0x81, 2, 0x09, 1, 0x91, 2, 0xc0}
			if failure == "descriptor" {
				descriptor[1] = 0
			}
			pid := uint16(0x0e44)
			if failure == "identity" {
				pid = 0x0e45
			}
			fd, name := createSitelUHID(t, descriptor, pid)
			device := testBoundUSB(t)
			device.ProductID = 0x0e44
			if err := os.WriteFile(filepath.Join(device.SysPath, "idProduct"), []byte("0e44"), 0o600); err != nil {
				t.Fatal(err)
			}
			device.attachment = nil
			var err error
			device, err = bindUSBDevice(device)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			paths := hidrawPaths{class: filepath.Join(root, "class"), char: filepath.Join(root, "char"), dev: "/dev"}
			parent := filepath.Join(device.SysPath, "1-2:1.0", "simulated-hid")
			if err := os.MkdirAll(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			link := func(dir, target string) {
				t.Helper()
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(dir, "device")); err != nil {
					t.Fatal(err)
				}
			}
			link(filepath.Join(paths.class, name), parent)
			info, err := os.Stat(filepath.Join("/dev", name))
			if err != nil {
				t.Fatal(err)
			}
			stat := info.Sys().(*syscall.Stat_t)
			charParent := parent
			if failure == "parent" {
				charParent = t.TempDir()
			}
			link(filepath.Join(paths.char, fmt.Sprintf("%d:%d", unix.Major(uint64(stat.Rdev)), unix.Minor(uint64(stat.Rdev)))), charParent)
			if failure == "missing-node" {
				paths.dev = t.TempDir()
			}
			if failure == "changed-attachment" {
				if err := os.WriteFile(filepath.Join(device.SysPath, "devnum"), []byte("9"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := openSitelFirmwareHIDAt(device, paths)
			if failure != "none" {
				want := map[string]string{"descriptor": "hid-layout", "identity": "hid-info-mismatch", "parent": "hid-handle-parent", "missing-node": "hid-open-missing", "changed-attachment": "hid-usb-binding"}[failure]
				if err == nil {
					_ = raw.file.Close()
					t.Fatal("invalid kernel-backed device accepted", failure)
				}
				if HIDAccessFailureCode(err) != want {
					t.Fatal(failure, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = raw.file.Close() }()
			if raw.in.ReportID != 0 || raw.in.ReportBytes != 65 || raw.out != raw.in {
				t.Fatal(raw.in, raw.out)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- answerSitelUHIDHandshake(ctx, fd) }()
			linkProtocol := &sitelLink{io: raw, in: raw.in, out: raw.out, timeout: 100 * time.Millisecond}
			startErr := linkProtocol.start(ctx)
			peerErr := <-done
			if startErr != nil || peerErr != nil {
				t.Fatal(startErr, peerErr)
			}
			t.Log("Linux hidraw open, handle identity, descriptor parsing and unnumbered handshake passed; synthetic USB ancestry, no physical flash")
		})
	}
}

func createSitelUHID(t *testing.T, descriptor []byte, pid uint16) (int, string) {
	t.Helper()
	fd, err := unix.Open("/dev/uhid", unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) }) // closing destroys the virtual device
	marker := fmt.Sprintf("jabridge-kernel-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	event := make([]byte, 280+len(descriptor))
	binary.LittleEndian.PutUint32(event, 11) // UHID_CREATE2
	copy(event[4:132], "Jabridge synthetic firmware test")
	copy(event[196:260], marker)
	binary.LittleEndian.PutUint16(event[260:262], uint16(len(descriptor)))
	binary.LittleEndian.PutUint16(event[262:264], 3) // BUS_USB, simulated by UHID
	binary.LittleEndian.PutUint32(event[264:268], uint32(JabraVendorID))
	binary.LittleEndian.PutUint32(event[268:272], uint32(pid))
	copy(event[280:], descriptor)
	if _, err := unix.Write(fd, event); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir("/sys/class/hidraw")
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			data, _ := os.ReadFile(filepath.Join("/sys/class/hidraw", entry.Name(), "device/uevent"))
			if strings.Contains(string(data), "HID_UNIQ="+marker+"\n") {
				if _, err := os.Stat(filepath.Join("/dev", entry.Name())); err == nil {
					return fd, entry.Name()
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("virtual hidraw node did not appear")
	return -1, ""
}

func answerSitelUHIDHandshake(ctx context.Context, fd int) error {
	buffer := make([]byte, 8192)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, 10); err != nil {
			return err
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		n, err := unix.Read(fd, buffer)
		if err != nil {
			return err
		}
		if n < 4103 || binary.LittleEndian.Uint32(buffer) != 6 {
			continue
		} // UHID_OUTPUT
		size := binary.LittleEndian.Uint16(buffer[4100:4102])
		if size != 65 || buffer[4] != 0 || buffer[5] != 0x10 {
			return fmt.Errorf("unexpected unnumbered kernel output size/header: %d/%02x/%02x", size, buffer[4], buffer[5])
		}
		reply := make([]byte, 70)
		binary.LittleEndian.PutUint32(reply, 12) // UHID_INPUT2
		binary.LittleEndian.PutUint16(reply[4:6], 64)
		reply[6] = 0x20
		_, err = unix.Write(fd, reply)
		return err
	}
}
