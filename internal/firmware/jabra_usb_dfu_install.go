package firmware

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Watchdog0x/jabridge/internal/history"
	"golang.org/x/sys/unix"
)

type usbDFUSession interface {
	dfuControl
	Claim(byte, bool) error
	Close() error
}

type usbDFUBackend struct {
	enumerate func() ([]USBDevice, error)
	open      func(USBDevice) (usbDFUSession, error)
	inspect   func(USBDevice) (dfuInterface, error)
	enter     func(context.Context, USBDevice, byte) error
	version   func(USBDevice) (string, byte, error)
	wait      func(context.Context, time.Duration) error
}

func nativeUSBDFUBackend() usbDFUBackend {
	return usbDFUBackend{
		enumerate: enumerateUSB, open: func(d USBDevice) (usbDFUSession, error) { return openDFUUSB(d) },
		inspect: inspectDFUUSB, enter: enterUSBDFU, version: readUSBDFUVersion, wait: waitDFU,
	}
}

func selectUSBDFUTarget(devices []USBDevice, profile usbDFUProfile) (USBDevice, error) {
	var selected USBDevice
	for _, device := range devices {
		if device.VendorID != JabraVendorID || device.ViaDongle ||
			(!profile.runtime(device.ProductID) && device.ProductID != profile.DFUPID) {
			continue
		}
		if selected.SysPath != "" {
			return USBDevice{}, fmt.Errorf("connect only one %s for firmware installation", profile.Name)
		}
		selected = device
	}
	if selected.SysPath == "" {
		return selected, fmt.Errorf("connect %s directly by USB; this firmware cannot be sent through a Link dongle", profile.Name)
	}
	return selected, nil
}

func usbDFUModePacket(layout ControlLayout, address byte) ([]byte, error) {
	if (address != 1 && address != 8) || layout.OutputID == 0 || layout.OutputBytes < 33 || layout.OutputBytes > 65 {
		return nil, errors.New("unsupported Jabra USB DFU management address")
	}
	// Legacy GNP command 7 has no class/subcommand pair: inner length is 5.
	// The report number/size are separately checked against the HID descriptor.
	packet := make([]byte, layout.OutputBytes)
	copy(packet, []byte{layout.OutputID, address, 0, 1, 0x85, 7})
	return packet, nil
}

func readUSBDFUVersion(device USBDevice) (string, byte, error) {
	path, _, err := dfuControlPath(device)
	if err != nil {
		return "", 0, err
	}
	transport, err := OpenControlHidraw(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = transport.Close() }()
	for index, address := range []byte{8, 1} {
		version, err := QueryFirmwareVersion(transport, address, byte(index+1), time.Second)
		if err == nil {
			if _, err := parseVersionTriplet(version); err == nil {
				return version, address, nil
			}
		}
	}
	return "", 0, errors.New("device did not return a valid installed firmware version")
}

func waitUSBDFUDevice(ctx context.Context, port string, ready func(USBDevice) bool, backend usbDFUBackend) (USBDevice, error) {
	for {
		if err := ctx.Err(); err != nil {
			return USBDevice{}, err
		}
		devices, err := backend.enumerate()
		if err != nil {
			return USBDevice{}, err
		}
		for _, device := range devices {
			if device.SysPath == port && device.VendorID == JabraVendorID && ready(device) {
				return device, nil
			}
		}
		if err := backend.wait(ctx, 100*time.Millisecond); err != nil {
			return USBDevice{}, fmt.Errorf("waiting for Jabra USB DFU on the same USB port: %w", err)
		}
	}
}

func enterUSBDFU(ctx context.Context, device USBDevice, address byte) error {
	path, intf, err := dfuControlPath(device)
	if err != nil {
		return err
	}
	layout, err := InspectControlLayout(path)
	if err != nil {
		return err
	}
	packet, err := usbDFUModePacket(layout, address)
	if err != nil {
		return err
	}
	transport, err := openDFUUSB(device)
	if err != nil {
		return err
	}
	defer func() { _ = transport.Close() }()
	if err := transport.Claim(intf, true); err != nil {
		return err
	}
	n, err := transport.Control(ctx, 0x21, 9, 0x0200|uint16(layout.OutputID), uint16(intf), packet)
	if err != nil && !dfuDisconnectError(err) && !errors.Is(err, unix.EPIPE) {
		return fmt.Errorf("enter USB DFU update mode: %w", err)
	}
	if err == nil && n != len(packet) {
		return errors.New("short USB DFU update-mode command")
	}
	return nil // Only the subsequent same-port DFU enumeration proves entry.
}

func installUSBDFU(path string, accepted bool) error {
	image, err := loadJabraDFUImage(path)
	if err != nil {
		return err
	}
	devices, err := enumerateUSB()
	if err != nil {
		return err
	}
	device, err := selectUSBDFUTarget(devices, image.Profile)
	if err != nil {
		return err
	}
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalContext, 10*time.Minute)
	defer cancel()
	transfer, err := prepareFirmwareTransfer(path, image.Manifest)
	if err != nil {
		return err
	}
	if transfer.State.ArchiveSHA256 != image.SHA {
		return errors.New("firmware archive changed during preparation")
	}
	previous, previousErr := loadFirmwareRecoveryState()
	if previousErr != nil && !errors.Is(previousErr, fs.ErrNotExist) {
		return previousErr
	}
	port := filepath.Base(device.SysPath)
	if previousErr == nil && previous.USBPort != "" && previous.USBPort != port {
		return errors.New("reconnect the device to its original USB port for recovery")
	}
	transfer.State.USBPort = port
	if previousErr == nil && previous.USBPort == port && sameFirmwareRecoveryTarget(previous, transfer.State) {
		fmt.Fprintln(os.Stderr, "Using the previously verified firmware file for recovery.")
	} else if err := verifyUSBDFURelease(ctx, image, device); err != nil {
		return err
	}
	// Test USB-node access before the mode switch can leave normal HID mode.
	usb, err := openDFUUSB(device)
	if err != nil {
		return err
	}
	if err := usb.Close(); err != nil {
		return err
	}
	var address byte
	if image.Profile.runtime(device.ProductID) {
		version, destination, err := readUSBDFUVersion(device)
		if err != nil {
			return err
		}
		address = destination
		fmt.Fprintf(os.Stderr, "%s: installed %s, selected %s.\n", image.Profile.Name, version, image.Manifest.Version)
	} else {
		if _, err := inspectDFUUSB(device); err != nil {
			return err
		}
		transfer.Recovery = true
	}
	confirmation := "INSTALL"
	if transfer.Recovery {
		confirmation = "RECOVER"
	}
	fmt.Fprintf(os.Stderr, "%s native firmware preview. Real-device installation and interrupted-update recovery still need testing.\n", image.Profile.Name)
	if !accepted && !confirmFirmwareAction(os.Stdin, os.Stderr, confirmation) {
		return errors.New("firmware install cancelled")
	}
	if err := saveFirmwareRecoveryState(transfer.State); err != nil {
		return err
	}
	if err := runUSBDFU(ctx, device, address, image, nativeUSBDFUBackend()); err != nil {
		return fmt.Errorf("USB DFU firmware update stopped: %w. Keep the same file and USB port, then run firmware install again to retry recovery", err)
	}
	if err := clearFirmwareRecoveryState(); err != nil {
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "%s firmware %s installed and read back from the device.\n", image.Profile.Name, image.Manifest.Version)
	return err
}

func verifyUSBDFURelease(ctx context.Context, image *jabraDFUImage, device USBDevice) error {
	// The suffix identifies the bootloader. Runtime siblings must additionally
	// match Jabra's published release bytes; never infer compatibility by range.
	if device.VendorID != JabraVendorID || device.ViaDongle ||
		(!image.Profile.runtime(device.ProductID) && device.ProductID != image.Profile.DFUPID) {
		return errors.New("USB DFU firmware target does not match the attached device")
	}
	lookupPID := device.ProductID
	if lookupPID == image.Profile.DFUPID {
		lookupPID = image.Profile.RuntimePIDs[0]
	}
	evidence, err := firmwareModelCatalog.FirmwareRelease(ctx, lookupPID, image.Manifest.Version)
	if err != nil || !firmwareReleaseMatchesDevice(image.MD5, lookupPID, evidence) ||
		!slices.Contains(evidence.FirmwareProtocols, 1) {
		return fmt.Errorf("USB DFU firmware does not match the official protocol-1 release: %w", errors.Join(err, errors.New("release validation failed")))
	}
	return nil
}

func runUSBDFU(ctx context.Context, original USBDevice, address byte, image *jabraDFUImage, backend usbDFUBackend) (resultErr error) {
	if image == nil || image.Manifest == nil || len(image.Payload) == 0 || original.VendorID != JabraVendorID || original.ViaDongle ||
		(!image.Profile.runtime(original.ProductID) && original.ProductID != image.Profile.DFUPID) {
		return errors.New("USB DFU target does not match the validated image profile")
	}
	finish := history.Begin(history.Event{Component: "firmware", Action: "run", USBProduct: original.ProductID, Connection: "usb"})
	defer history.EndDeferred(finish, &resultErr)
	record := func(action string) {
		history.Record(history.Event{Component: "firmware", Action: action, Phase: "observed", USBProduct: original.ProductID, Connection: "usb"})
	}
	device := original
	if image.Profile.runtime(device.ProductID) {
		record("dfu-enter")
		fmt.Fprintln(os.Stderr, "Switching Jabra USB DFU to update mode...")
		if err := backend.enter(ctx, device, address); err != nil {
			return err
		}
	}
	for attempts := 0; attempts < 3; attempts++ {
		record("dfu-runtime")
		wait, cancel := context.WithTimeout(ctx, 30*time.Second)
		found, err := waitUSBDFUDevice(wait, original.SysPath, func(d USBDevice) bool { return d.ProductID == image.Profile.DFUPID }, backend)
		cancel()
		if err != nil {
			return err
		}
		device = found
		intf, err := backend.inspect(device)
		if err != nil {
			return err
		}
		usb, err := backend.open(device)
		if err != nil {
			return err
		}
		if err := usb.Claim(intf.Number, false); err != nil {
			_ = usb.Close()
			return err
		}
		detached, err := prepareDFUDownload(ctx, usb, intf)
		if err != nil {
			_ = usb.Close()
			return err
		}
		if detached {
			_ = usb.Close()
			if err := backend.wait(ctx, 2*time.Second); err != nil {
				return err
			}
			continue
		}
		last := -1
		record("dfu-transfer")
		err = transferDFU(ctx, usb, intf, image.Payload, func(percent int) {
			if percent/5 != last {
				last = percent / 5
				fmt.Fprintf(os.Stderr, "Sending firmware: %d%%\n", percent)
			}
		})
		if err == nil {
			resetErr := usb.Reset()
			if resetErr != nil && !dfuDisconnectError(resetErr) {
				err = resetErr
			}
		}
		_ = usb.Close()
		if err != nil {
			return err
		}
		record("dfu-verify")
		return verifyUSBDFUAfterFlash(ctx, original.SysPath, image.Manifest.Version, image.Profile, backend)
	}
	return errors.New("device stayed in DFU runtime after detach")
}

func verifyUSBDFUAfterFlash(parent context.Context, port, wanted string, profile usbDFUProfile, backend usbDFUBackend) error {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	resetDFU := false
	for {
		device, err := waitUSBDFUDevice(ctx, port, func(d USBDevice) bool {
			return profile.runtime(d.ProductID) || d.ProductID == profile.DFUPID
		}, backend)
		if err != nil {
			return err
		}
		if profile.runtime(device.ProductID) {
			version, _, err := backend.version(device)
			if err == nil {
				if version != wanted {
					return fmt.Errorf("installed firmware reads %s, expected %s", version, wanted)
				}
				return nil
			}
		} else if !resetDFU {
			// Jabra USB DFU may first return to DFU appIDLE. Only reset again after
			// that state is read, not while flash manifestation is in progress.
			intf, err := backend.inspect(device)
			if err == nil {
				usb, err := backend.open(device)
				if err == nil {
					if err := usb.Claim(intf.Number, false); err == nil {
						status, statusErr := getDFUStatus(ctx, usb, intf)
						if statusErr == nil && status.Code == 0 && status.State == 0 {
							err = usb.Reset()
							resetDFU = err == nil || dfuDisconnectError(err)
						}
					}
					_ = usb.Close()
				}
			}
		}
		if err := backend.wait(ctx, 500*time.Millisecond); err != nil {
			return fmt.Errorf("installed USB DFU firmware could not be read back: %w", err)
		}
	}
}

func acquireFirmwareInstallLock() (*os.File, error) {
	return acquireDeviceLease(unix.LOCK_EX)
}

// AcquireDeviceAccess prevents normal device owners from starting while the
// firmware CLI holds exclusive access. The caller keeps the lease until all
// its hardware operations have stopped, then closes it.
func AcquireDeviceAccess() (*os.File, error) {
	return acquireDeviceLease(unix.LOCK_SH)
}

func acquireDeviceLease(mode int) (*os.File, error) {
	path, err := firmwareRecoveryStatePath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	fd, err := unix.Open(strings.TrimSuffix(path, ".json")+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path+".lock")
	if err := unix.Flock(fd, mode|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("device access is busy in another Jabridge process; wait for the current operation to finish")
	}
	return file, nil
}
