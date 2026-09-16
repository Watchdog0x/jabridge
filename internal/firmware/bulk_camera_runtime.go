package firmware

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

type cameraIdentity struct {
	Address               byte
	PID                   uint16
	Port, Serial, Version string
}

func (id cameraIdentity) hash() string {
	return extendedRecoveryIdentity(csrExtendedIdentity{PID: id.PID, Port: id.Port, Serial: id.Serial})
}

func (r *sitelRuntime) identifyCamera(ctx context.Context, device USBDevice) (cameraIdentity, error) {
	id := cameraIdentity{PID: device.ProductID, Port: device.SysPath}
	profile, ok := bulkCameraProfileForPID(id.PID)
	if !ok || device.VendorID != JabraVendorID || device.ViaDongle || device.attachment == nil {
		return id, errors.New("not a bound USB bulk camera")
	}
	return r.identifyUSBCamera(ctx, device, profile.ArchivePID, 18)
}

func (r *sitelRuntime) identifyUSBCamera(ctx context.Context, device USBDevice, archivePID uint16, protocol byte) (cameraIdentity, error) {
	id := cameraIdentity{PID: device.ProductID, Port: device.SysPath}
	if device.VendorID != JabraVendorID || device.ViaDongle || device.attachment == nil {
		return id, errors.New("not a bound USB camera")
	}
	for _, address := range []byte{8, 1} {
		data, err := r.query(ctx, address, 0x11)
		if err == nil && len(data) == 2 && (binary.LittleEndian.Uint16(data) == id.PID || binary.LittleEndian.Uint16(data) == archivePID) {
			id.Address = address
			break
		}
	}
	if id.Address == 0 {
		return id, errors.New("camera management identity does not match USB")
	}
	var err error
	id.Serial, err = r.serial(ctx, id.Address)
	if err != nil {
		return id, err
	}
	if id.Serial == "" {
		return id, errors.New("camera has no readable serial identity")
	}
	id.Version, err = r.text(ctx, id.Address, 3)
	if err != nil {
		return id, err
	}
	if err := validateCameraVersion(id.Version); err != nil {
		return id, err
	}
	boot, err := r.query(ctx, id.Address, 0x13)
	if err != nil || len(boot) != 2 || binary.LittleEndian.Uint16(boot) != archivePID {
		return id, errors.New("camera firmware product ID does not match its archive")
	}
	protocols, err := r.query(ctx, id.Address, 0x14)
	if err != nil || !bytes.Contains(protocols, []byte{protocol}) {
		return id, errors.New("camera does not report the expected firmware protocol")
	}
	return id, nil
}

type cameraUpdateEvent struct {
	State    byte
	Progress int
}

func decodeCameraEvent(packet []byte, address byte) (cameraUpdateEvent, bool, error) {
	event := cameraUpdateEvent{Progress: -1}
	if len(packet) < 6 || packet[0] != 0 || packet[1] != address || packet[3]&0xc0 != 0 || packet[4] != 0x12 {
		return event, false, nil
	}
	switch packet[5] {
	case 0x12:
		if len(packet) < 7 {
			return event, false, errors.New("short camera update state")
		}
		event.State = packet[6]
		return event, true, nil
	case 0x27:
		if len(packet) < 10 {
			return event, false, errors.New("short camera update progress")
		}
		if packet[6] != 1 && packet[6] != 2 {
			return event, false, nil
		}
		if packet[9] > 100 {
			return event, false, errors.New("invalid camera update progress")
		}
		event.Progress = int(packet[9])
		return event, true, nil
	}
	return event, false, nil
}

var errCameraCommandNotStarted = errors.New("camera operation did not start")

func cameraCommandNotStarted(err error) error {
	return fmt.Errorf("%w: %w", errCameraCommandNotStarted, err)
}

type cameraControl interface {
	Identity() cameraIdentity
	Subscribe(context.Context) error
	Start(context.Context) error
	Next(context.Context) (cameraUpdateEvent, error)
	Close() error
}

type nativeCameraControl struct {
	runtime *sitelRuntime
	id      cameraIdentity
	close   func() error
}

func (c *nativeCameraControl) Identity() cameraIdentity { return c.id }
func (c *nativeCameraControl) Close() error             { return c.close() }
func (c *nativeCameraControl) Subscribe(ctx context.Context) error {
	_, err := c.runtime.exchange(ctx, c.id.Address, 13, 0, []byte{1, 5, 0})
	return err
}
func (c *nativeCameraControl) Start(ctx context.Context) error {
	if err := requireHardwareWrites(); err != nil {
		return cameraCommandNotStarted(err)
	}
	_, err := c.runtime.exchange(ctx, c.id.Address, 7, 0x80, []byte{3, 2})
	if errors.Is(err, errSitelRejected) || errors.Is(err, errManagementNotSent) {
		return cameraCommandNotStarted(err)
	}
	return err
}
func (c *nativeCameraControl) Next(ctx context.Context) (cameraUpdateEvent, error) {
	for {
		packet, err := c.runtime.nextEvent(ctx)
		if err != nil {
			return cameraUpdateEvent{}, err
		}
		event, ok, err := decodeCameraEvent(packet, c.id.Address)
		if err != nil || ok {
			return event, err
		}
	}
}

type cameraInstallBackend interface {
	Open(context.Context, USBDevice) (cameraControl, error)
	Stage(context.Context, USBDevice, string, *bulkCameraArchive, func(int64, int64)) error
	Reboot(context.Context, USBDevice) (USBDevice, error)
}
type nativeCameraBackend struct{}

func (nativeCameraBackend) Open(ctx context.Context, device USBDevice) (cameraControl, error) {
	ready, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		r, closeConnection, err := openSitelRuntime(device)
		if err == nil {
			id, readErr := r.identifyCamera(ready, device)
			if readErr == nil {
				return &nativeCameraControl{r, id, closeConnection}, nil
			}
			_ = closeConnection()
			err = readErr
		}
		if waitErr := waitDFU(ready, 250*time.Millisecond); waitErr != nil {
			return nil, fmt.Errorf("camera did not become ready: %v: %w", err, waitErr)
		}
	}
}
func (nativeCameraBackend) Stage(ctx context.Context, device USBDevice, path string, archive *bulkCameraArchive, progress func(int64, int64)) error {
	transport, err := openCameraBulk(device)
	if err != nil {
		return err
	}
	defer func() { _ = transport.Close() }()
	file, info, err := openFirmwareRead(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if info.Size() != archive.Size {
		return errors.New("camera archive size changed")
	}
	return transferBulkCamera(ctx, transport, file, archive.Size, archive.MD5, progress)
}
func (nativeCameraBackend) Reboot(ctx context.Context, device USBDevice) (USBDevice, error) {
	disconnect, cancel := context.WithTimeout(ctx, 30*time.Second)
	for validateUSBDevice(device) == nil {
		if err := waitDFU(disconnect, 100*time.Millisecond); err != nil {
			cancel()
			return USBDevice{}, fmt.Errorf("camera did not disconnect for reboot: %w", err)
		}
	}
	cancel()
	reconnect, finish := context.WithTimeout(ctx, 3*time.Minute)
	defer finish()
	return waitSitelDevice(reconnect, nativeSitelBackend{}, device, device.ProductID)
}
