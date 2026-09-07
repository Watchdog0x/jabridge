package firmware

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// USB DFU 1.1, sections 6 and 7. This is not the CSR OTA partition protocol.
// Transfers include the CSR image header but exclude the USB DFU file suffix.
type dfuControl interface {
	Control(context.Context, byte, byte, uint16, uint16, []byte) (int, error)
	Reset() error
}

type dfuInterface struct {
	Number, Alternate, Protocol, Attributes byte
	DetachMS, TransferSize, Version         uint16
}

type dfuStatus struct {
	Code, State byte
	Poll        time.Duration
}

func parseDFUInterface(descriptors []byte, configuration byte) (dfuInterface, error) {
	var result dfuInterface
	var current dfuInterface
	selected, candidate, found := false, false, false
	for pos := 0; pos < len(descriptors); {
		if len(descriptors)-pos < 2 {
			return result, errors.New("truncated USB descriptor")
		}
		size := int(descriptors[pos])
		if size < 2 || size > len(descriptors)-pos {
			return result, errors.New("invalid USB descriptor length")
		}
		d := descriptors[pos : pos+size]
		switch d[1] {
		case 2:
			if size < 9 {
				return result, errors.New("short USB configuration")
			}
			selected, candidate = d[5] == configuration, false
		case 4:
			if size < 9 {
				return result, errors.New("short USB interface")
			}
			candidate = selected && d[5] == 0xfe && d[6] == 1
			current = dfuInterface{Number: d[2], Alternate: d[3], Protocol: d[7]}
		case 0x21:
			if !candidate {
				break // HID also uses descriptor type 0x21.
			}
			if size != 9 || found || current.Alternate != 0 || (current.Protocol != 1 && current.Protocol != 2) {
				return result, errors.New("unsupported or ambiguous USB DFU interface")
			}
			current.Attributes = d[2]
			current.DetachMS = binary.LittleEndian.Uint16(d[3:5])
			current.TransferSize = binary.LittleEndian.Uint16(d[5:7])
			current.Version = binary.LittleEndian.Uint16(d[7:9])
			if current.TransferSize == 0 || current.TransferSize > 4096 || current.Attributes&1 == 0 || current.Attributes&0xf0 != 0 ||
				(current.Version != 0x0100 && current.Version != 0x0110 && current.Version != 0x0101) {
				return result, errors.New("unsupported USB DFU version, size or download capability")
			}
			result, found = current, true
		}
		pos += size
	}
	if !found {
		return result, errors.New("no usable USB DFU functional descriptor")
	}
	return result, nil
}

func getDFUStatus(ctx context.Context, transport dfuControl, intf dfuInterface) (dfuStatus, error) {
	data := make([]byte, 6)
	n, err := transport.Control(ctx, 0xa1, 3, 0, uint16(intf.Number), data)
	if err != nil {
		return dfuStatus{}, fmt.Errorf("DFU status: %w", err)
	}
	if n != len(data) || data[4] > 10 {
		return dfuStatus{}, errors.New("invalid USB DFU status reply")
	}
	poll := uint32(data[1]) | uint32(data[2])<<8 | uint32(data[3])<<16
	return dfuStatus{Code: data[0], State: data[4], Poll: time.Duration(poll) * time.Millisecond}, nil
}

func dfuRequest(ctx context.Context, t dfuControl, intf dfuInterface, request byte, value uint16, data []byte) error {
	n, err := t.Control(ctx, 0x21, request, value, uint16(intf.Number), data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return fmt.Errorf("short DFU transfer: %d/%d", n, len(data))
	}
	return nil
}

func waitDFU(ctx context.Context, delay time.Duration) error {
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// A failed transfer is not retried in-place. Recovery starts again at block 0
// with the exact same checked image after an explicit user confirmation.
func prepareDFUDownload(ctx context.Context, t dfuControl, intf dfuInterface) (bool, error) {
	status, err := getDFUStatus(ctx, t, intf)
	if err != nil {
		return false, err
	}
	if status.State == 10 {
		if err := dfuRequest(ctx, t, intf, 4, 0, nil); err != nil {
			return false, fmt.Errorf("clear DFU error: %w", err)
		}
		status, err = getDFUStatus(ctx, t, intf)
		if err != nil {
			return false, err
		}
	}
	if status.Code != 0 {
		return false, fmt.Errorf("device reports DFU error 0x%02x", status.Code)
	}
	switch status.State {
	case 0: // appIDLE: the HID mode switch exposes the DFU runtime first.
		if err := dfuRequest(ctx, t, intf, 0, 1000, nil); err != nil && !dfuDisconnectError(err) {
			return false, fmt.Errorf("detach DFU runtime: %w", err)
		}
		if intf.Attributes&8 == 0 {
			if err := t.Reset(); err != nil && !dfuDisconnectError(err) {
				return false, err
			}
		}
		return true, nil // caller must close/reopen the same physical USB port.
	case 2:
		return false, nil
	case 5, 9: // interrupted download/upload, return to dfuIDLE before replay.
		if err := dfuRequest(ctx, t, intf, 6, 0, nil); err != nil {
			return false, err
		}
		status, err = getDFUStatus(ctx, t, intf)
		if err != nil {
			return false, err
		}
		if status.Code == 0 && status.State == 2 {
			return false, nil
		}
	}
	return false, fmt.Errorf("DFU is not ready for a new transfer (state %d)", status.State)
}

func transferDFU(ctx context.Context, t dfuControl, intf dfuInterface, payload []byte, progress func(int)) error {
	if len(payload) == 0 || intf.TransferSize == 0 || intf.TransferSize > 4096 {
		return errors.New("invalid DFU payload or transfer size")
	}
	chunkSize := int(intf.TransferSize)
	blocks := (len(payload) + chunkSize - 1) / chunkSize
	if blocks > 65535 {
		return errors.New("DFU block counter would overflow")
	}
	for block := 0; block <= blocks; block++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var chunk []byte
		if block < blocks {
			start := block * chunkSize
			chunk = payload[start:min(start+chunkSize, len(payload))]
		}
		if err := dfuRequest(ctx, t, intf, 1, uint16(block), chunk); err != nil {
			return fmt.Errorf("DFU block %d: %w", block, err)
		}
		if err := waitDFUBlock(ctx, t, intf, block == blocks); err != nil {
			return fmt.Errorf("DFU block %d status: %w", block, err)
		}
		if progress != nil {
			progress((block + 1) * 100 / (blocks + 1))
		}
	}
	return nil
}

func waitDFUBlock(parent context.Context, t dfuControl, intf dfuInterface, final bool) error {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	for {
		status, err := getDFUStatus(ctx, t, intf)
		if err != nil {
			// Some non-tolerant devices detach during manifestation. Re-enumeration
			// and installed-version verification are still mandatory for success.
			if final && dfuDisconnectError(err) {
				return nil
			}
			return err
		}
		if status.Code != 0 {
			return fmt.Errorf("device rejected transfer (DFU status 0x%02x)", status.Code)
		}
		if !final && status.State == 5 {
			return nil
		}
		if final && (status.State == 2 || status.State == 8) {
			return nil
		}
		if (!final && status.State != 3 && status.State != 4) ||
			(final && status.State != 6 && status.State != 7) {
			return fmt.Errorf("unexpected DFU state %d", status.State)
		}
		if err := waitDFU(ctx, status.Poll); err != nil {
			return err
		}
	}
}
