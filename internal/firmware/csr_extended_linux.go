package firmware

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// Unlike os.File.Write on a pollable descriptor, these operations remain
// cancellable while the device is not ready. No goroutine is left owning an FD.
type csrContextHID struct{ transport *HidrawTransport }

func (h csrContextHID) wait(ctx context.Context, events int16) (int16, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		wait := 50 * time.Millisecond
		if deadline, ok := ctx.Deadline(); ok {
			wait = min(wait, time.Until(deadline))
		}
		if wait <= 0 {
			return 0, context.DeadlineExceeded
		}
		fds := []unix.PollFd{{Fd: int32(h.transport.f.Fd()), Events: events}}
		n, err := unix.Poll(fds, int((wait+time.Millisecond-1)/time.Millisecond))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			continue
		}
		if fds[0].Revents&events != 0 {
			return fds[0].Revents, nil
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return 0, unix.ENODEV
		}
	}
}

func (h csrContextHID) Write(ctx context.Context, data []byte) error {
	if len(data) != h.transport.reportSize || data[0] != 5 {
		return errors.New("invalid extended CSR HID output")
	}
	for {
		if _, err := h.wait(ctx, unix.POLLOUT); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := unix.Write(int(h.transport.f.Fd()), data)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if err != nil {
			return err
		}
		if n != len(data) {
			return errors.New("short extended CSR HID write")
		}
		return nil
	}
}

func (h csrContextHID) Read(ctx context.Context) ([]byte, error) {
	buffer := make([]byte, 256)
	for {
		if _, err := h.wait(ctx, unix.POLLIN); err != nil {
			return nil, err
		}
		n, err := unix.Read(int(h.transport.f.Fd()), buffer)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, unix.ENODEV
		}
		if buffer[0] != 5 {
			continue
		}
		return append([]byte(nil), buffer[:n]...), nil
	}
}

type nativeExtendedCSRBackend struct{ device USBDevice }

func (b *nativeExtendedCSRBackend) Open(ctx context.Context) (csrExtendedConnection, error) {
	return openExtendedCSRConnection(ctx, b.device)
}

func (b *nativeExtendedCSRBackend) Reconnect(ctx context.Context, before csrExtendedIdentity) (csrExtendedConnection, error) {
	for {
		if err := ctx.Err(); err != nil {
			return csrExtendedConnection{}, err
		}
		if validateUSBDevice(b.device) != nil {
			devices, err := enumerateBoundUSB()
			if err != nil {
				return csrExtendedConnection{}, err
			}
			for _, device := range devices {
				if device.SysPath != b.device.SysPath || device.ProductID != b.device.ProductID || device.VendorID != JabraVendorID {
					continue
				}
				if b.device.Serial != "" && device.Serial != b.device.Serial {
					return csrExtendedConnection{}, errors.New("USB identity changed during extended CSR update")
				}
				connection, err := openExtendedCSRConnection(ctx, device)
				if err != nil {
					continue
				}
				if connection.Identity.Attachment == before.Attachment {
					_ = connection.Close()
					continue
				}
				b.device = device
				return connection, nil
			}
		}
		if err := waitDFU(ctx, 100*time.Millisecond); err != nil {
			return csrExtendedConnection{}, err
		}
	}
}

func openExtendedCSRConnection(ctx context.Context, device USBDevice) (csrExtendedConnection, error) {
	transport, err := openBoundCSR(device)
	if err != nil {
		return csrExtendedConnection{}, err
	}
	connection, err := identifyExtendedCSRConnection(ctx, csrContextHID{transport}, device, transport.reportSize, transport.Close)
	if err != nil {
		_ = transport.Close()
	}
	return connection, err
}

// Discovery uses only known IDENT queries on the selected USB attachment. An
// endpoint is usable only if its own PID, serial, variant, firmware and language
// can be read. A successful version read alone never authorizes a flash route.
func identifyExtendedCSRConnection(ctx context.Context, transport csrStageIO, device USBDevice, reportSize int, closeConnection func() error) (csrExtendedConnection, error) {
	if device.attachment == nil || device.ViaDongle || device.VendorID != JabraVendorID {
		return csrExtendedConnection{}, errors.New("extended CSR requires a bound direct USB device")
	}
	var matches []csrExtendedConnection
	for _, address := range []byte{8, 1} {
		if err := ctx.Err(); err != nil {
			return csrExtendedConnection{}, err
		}
		session := &csrStageTransfer{io: transport, stage: csrExtendedStage{Address: address, ReportSize: reportSize, Timeout: time.Second}, seq: address * 16}
		pid, err := session.query(ctx, 2, 0x11)
		if err != nil || len(pid) != 2 || binary.LittleEndian.Uint16(pid) != device.ProductID {
			continue
		}
		serial, err := session.query(ctx, 2, 1)
		if err != nil {
			continue
		}
		serialText, err := decodeExtendedIdentityString(serial)
		if err != nil {
			continue
		}
		variant, err := session.query(ctx, 2, 2)
		if err != nil || len(variant) < 3 || len(variant) > 16 {
			continue
		}
		version, err := session.query(ctx, 2, 3)
		if err != nil {
			continue
		}
		versionText, err := decodeExtendedIdentityString(version)
		if err != nil {
			continue
		}
		if _, err := parseVersionTriplet(versionText); err != nil {
			continue
		}
		language, err := session.query(ctx, 0x13, 8)
		if err != nil || len(language) != 2 || binary.LittleEndian.Uint16(language) == 0 {
			continue
		}
		protocols, err := session.query(ctx, 2, 0x14)
		if err != nil || len(protocols) == 0 || len(protocols) > 16 {
			continue
		}
		matches = append(matches, csrExtendedConnection{IO: transport, Address: address, ReportSize: reportSize, Close: closeConnection, Identity: csrExtendedIdentity{
			PID: device.ProductID, Port: device.SysPath, Attachment: device.attachment.fingerprint, Serial: serialText,
			Variant: hex.EncodeToString(variant[1:]), Version: versionText, Language: binary.LittleEndian.Uint16(language), FirmwareProtocols: protocols,
		}})
	}
	if len(matches) != 1 {
		return csrExtendedConnection{}, fmt.Errorf("need one fully identified extended CSR endpoint; found %d", len(matches))
	}
	return matches[0], nil
}

func decodeExtendedIdentityString(payload []byte) (string, error) {
	if len(payload) < 2 || int(payload[0]) != len(payload)-1 {
		return "", errors.New("invalid identity string length")
	}
	for _, value := range payload[1:] {
		if value < 32 || value > 126 {
			return "", errors.New("invalid identity string")
		}
	}
	return string(payload[1:]), nil
}
