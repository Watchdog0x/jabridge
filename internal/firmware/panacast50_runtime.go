package firmware

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
)

type panacast50Info struct {
	Identity                      cameraIdentity
	FWState, FWDetail, VideoState byte
	HaveFWState, HaveVideoState   bool
}

func panacast50RuntimePID(pid uint16) bool { return containsPID(panacast50PIDs, pid) }
func panacast50ModePID(pid uint16) bool    { return pid == 0x3010 || panacast50RuntimePID(pid) }
func panacast50IdentityHash(id cameraIdentity, runtimePID uint16) string {
	id.PID = runtimePID
	return id.hash()
}

func openPanaCast50Control(ctx context.Context, device USBDevice) (*sitelRuntime, cameraIdentity, func() error, error) {
	id := cameraIdentity{PID: device.ProductID, Port: device.SysPath}
	if device.VendorID != JabraVendorID || !panacast50ModePID(device.ProductID) || device.ViaDongle || device.attachment == nil {
		return nil, id, nil, errors.New("not a bound PanaCast 50")
	}
	r, closeConnection, err := openSitelRuntime(device)
	if err != nil {
		return nil, id, nil, err
	}
	fail := func(err error) (*sitelRuntime, cameraIdentity, func() error, error) {
		_ = closeConnection()
		return nil, id, nil, err
	}
	for _, address := range []byte{1, 8} {
		data, err := r.query(ctx, address, 0x11)
		if err == nil && len(data) == 2 && (binary.LittleEndian.Uint16(data) == device.ProductID || binary.LittleEndian.Uint16(data) == 0x3010) {
			id.Address = address
			break
		}
	}
	if id.Address == 0 {
		return fail(errors.New("PanaCast 50 management PID does not match its USB mode"))
	}
	id.Serial, err = r.serial(ctx, id.Address)
	if err != nil {
		return fail(err)
	}
	if id.Serial == "" {
		return fail(errors.New("PanaCast 50 serial is unavailable"))
	}
	id.Version, err = r.text(ctx, id.Address, 3)
	if err != nil {
		return fail(err)
	}
	if _, err := parseVersionTriplet(id.Version); err != nil {
		return fail(err)
	}
	return r, id, closeConnection, nil
}

type panacast50Backend interface {
	Inspect(context.Context, USBDevice) (panacast50Info, error)
	FileTransport(context.Context, USBDevice, cameraIdentity) (bool, error)
	StorageReady(context.Context) error
	Busy(context.Context, USBDevice, cameraIdentity) error
	Stage(context.Context, USBDevice, cameraIdentity, *panacast50Archive, string, func(int64, int64)) error
	Command(context.Context, USBDevice, cameraIdentity, byte) error
	Permission(context.Context, USBDevice, cameraIdentity) error
	Reset(context.Context, USBDevice, cameraIdentity) error
	Current(context.Context, USBDevice) (USBDevice, error)
	Sleep(context.Context, time.Duration) error
}
type nativePanaCast50Backend struct{}

func (nativePanaCast50Backend) Inspect(ctx context.Context, device USBDevice) (panacast50Info, error) {
	r, id, closeConnection, err := openPanaCast50Control(ctx, device)
	info := panacast50Info{Identity: id}
	if err != nil {
		return info, err
	}
	defer func() { _ = closeConnection() }()
	state, err := r.exchange(ctx, id.Address, 0x12, 0x40, []byte{0x12})
	if err != nil && !errors.Is(err, errSitelRejected) {
		return info, err
	}
	if err == nil {
		if len(state) < 2 {
			return info, errors.New("short PanaCast 50 update state")
		}
		info.FWState, info.FWDetail, info.HaveFWState = state[0], state[1], true
	}
	video, err := r.exchange(ctx, id.Address, 0x26, 0x40, []byte{2})
	if err != nil && !errors.Is(err, errSitelRejected) {
		return info, err
	}
	if err == nil {
		if len(video) < 1 {
			return info, errors.New("short PanaCast 50 video boot state")
		}
		info.VideoState, info.HaveVideoState = video[0], true
	}
	return info, nil
}

func withPanaCast50Control(ctx context.Context, device USBDevice, expected cameraIdentity, action func(*sitelRuntime, byte) error) error {
	r, id, closeConnection, err := openPanaCast50Control(ctx, device)
	if err != nil {
		return managementNotSent(err)
	}
	defer func() { _ = closeConnection() }()
	if id.hash() != expected.hash() {
		return managementNotSent(errors.New("PanaCast 50 changed before the operation"))
	}
	return action(r, id.Address)
}
func (nativePanaCast50Backend) FileTransport(ctx context.Context, device USBDevice, id cameraIdentity) (bool, error) {
	err := withPanaCast50Control(ctx, device, id, func(r *sitelRuntime, address byte) error {
		_, err := listCameraFiles(ctx, nativeCameraFiles{runtime: r, address: address, device: device})
		return err
	})
	if errors.Is(err, errSitelRejected) {
		return false, nil
	}
	return err == nil, err
}
func (nativePanaCast50Backend) StorageReady(ctx context.Context) error {
	connection, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	if err := connection.Object(udisksService, dbus.ObjectPath("/org/freedesktop/UDisks2/Manager")).CallWithContext(ctx, "org.freedesktop.DBus.Peer.Ping", 0).Err; err != nil {
		return fmt.Errorf("camera storage mode requires the UDisks2 system service: %w", err)
	}
	return nil
}
func (nativePanaCast50Backend) Busy(ctx context.Context, device USBDevice, id cameraIdentity) error {
	return withPanaCast50Control(ctx, device, id, func(r *sitelRuntime, address byte) error {
		for _, query := range []struct {
			class, op byte
			camera    bool
		}{{0x12, 0, false}, {0x26, 0x1d, true}} {
			data, err := r.exchange(ctx, address, query.class, 0x40, []byte{query.op})
			// Older storage-mode firmware may lack this read. The operator
			// still confirms that camera/call applications have been closed.
			if errors.Is(err, errSitelRejected) {
				continue
			}
			if err != nil {
				return err
			}
			if len(data) < 1 {
				return errors.New("camera busy state is unavailable")
			}
			if query.camera && data[0] != 0 || !query.camera && data[0]&4 != 0 {
				return errors.New("PanaCast 50 is in use; close camera apps and end calls before updating")
			}
		}
		return nil
	})
}
func (nativePanaCast50Backend) Stage(ctx context.Context, device USBDevice, id cameraIdentity, archive *panacast50Archive, method string, progress func(int64, int64)) error {
	if method == "mass" {
		return stageCameraMassStorage(ctx, device, archive, progress)
	}
	if method != "gnp" {
		return errors.New("unknown PanaCast 50 file transport")
	}
	return withPanaCast50Control(ctx, device, id, func(r *sitelRuntime, address byte) error {
		return transferCameraGNPFile(ctx, nativeCameraFiles{runtime: r, address: address, device: device}, bytes.NewReader(archive.Data), int64(len(archive.Data)), archive.MD5, progress)
	})
}
func (nativePanaCast50Backend) Command(ctx context.Context, device USBDevice, id cameraIdentity, step byte) error {
	if step > 2 {
		return cameraCommandNotStarted(errors.New("invalid PanaCast 50 update step"))
	}
	if err := requireHardwareWrites(); err != nil {
		return cameraCommandNotStarted(err)
	}
	err := withPanaCast50Control(ctx, device, id, func(r *sitelRuntime, address byte) error {
		_, err := r.exchangeTimeout(ctx, address, 7, 0x80, []byte{3, step}, 10*time.Second)
		return err
	})
	if errors.Is(err, errSitelRejected) || errors.Is(err, errManagementNotSent) {
		return cameraCommandNotStarted(err)
	}
	return err
}
func (nativePanaCast50Backend) Permission(ctx context.Context, device USBDevice, id cameraIdentity) error {
	if err := requireHardwareWrites(); err != nil {
		return err
	}
	return withPanaCast50Control(ctx, device, id, func(r *sitelRuntime, address byte) error {
		data, err := r.exchange(ctx, address, 13, 0x40, []byte{0x11})
		if err != nil {
			return err
		}
		if len(data) != 1 || data[0] != 1 {
			return errors.New("PanaCast 50 did not grant permission to leave update mode")
		}
		return nil
	})
}
func (nativePanaCast50Backend) Reset(ctx context.Context, device USBDevice, id cameraIdentity) error {
	if err := requireHardwareWrites(); err != nil {
		return cameraCommandNotStarted(err)
	}
	err := withPanaCast50Control(ctx, device, id, func(r *sitelRuntime, address byte) error {
		_, err := r.exchange(ctx, address, 13, 0x80, []byte{0x12})
		return err
	})
	if errors.Is(err, errSitelRejected) || errors.Is(err, errManagementNotSent) {
		return cameraCommandNotStarted(err)
	}
	return err
}
func (nativePanaCast50Backend) Current(ctx context.Context, previous USBDevice) (USBDevice, error) {
	if err := ctx.Err(); err != nil {
		return USBDevice{}, err
	}
	devices, err := enumerateBoundUSB()
	if err != nil {
		return USBDevice{}, err
	}
	var matches []USBDevice
	for _, device := range devices {
		if device.SysPath == previous.SysPath && device.VendorID == JabraVendorID && panacast50ModePID(device.ProductID) {
			matches = append(matches, device)
		}
	}
	if len(matches) != 1 {
		return USBDevice{}, errPanaCast50Absent
	}
	return matches[0], nil
}
func (nativePanaCast50Backend) Sleep(ctx context.Context, d time.Duration) error {
	return waitDFU(ctx, d)
}

var errPanaCast50Absent = errors.New("PanaCast 50 is restarting")
