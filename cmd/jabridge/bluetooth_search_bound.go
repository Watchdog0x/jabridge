package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Watchdog0x/jabridge/internal/btsearch"
	"github.com/Watchdog0x/jabridge/internal/firmware"
)

func usesDualModeSearch(device *jabra_DeviceInfo) bool {
	return device != nil && device.isDongle && (device.productID == 0x2e56 || device.productID == 0x2e57)
}

func typedSearchName(device btsearch.Device) string {
	kind := "Bluetooth Classic"
	if device.BluetoothType == 1 {
		kind = "Bluetooth LE"
	}
	return fmt.Sprintf("%s (%s)", device.Name, kind)
}

func openBoundSearchLink(device *jabra_DeviceInfo, binding *firmware.ControlBinding) (*searchLink, error) {
	file, layout, err := binding.OpenReader()
	if err != nil {
		return nil, err
	}
	return &searchLink{device: device, binding: binding, file: file, assembler: firmware.NewControlPacketAssembler(layout)}, nil
}

func (s *searchLink) Valid() error {
	if !searchMatchesCurrent(s.device) {
		return errors.New("selected dongle changed; search again")
	}
	current := deviceForID(s.device.deviceID)
	if current == nil || current.usbDevicePath != s.device.usbDevicePath || current.vendorID != s.device.vendorID {
		return errors.New("selected USB attachment changed; search again")
	}
	if s.pairing {
		selected, err := selectedDongleDevice()
		if err != nil || selected.instance != s.device.instance || selected.usbDevicePath != s.device.usbDevicePath {
			return errors.New("selected dongle changed while pairing")
		}
	}
	return s.binding.Validate()
}

func (s *searchLink) Command(ctx context.Context, opcode byte, payload []byte) error {
	_, err := s.boundExchange(ctx, gnpFlagCmd, gnpClassPairingDevice, opcode, payload)
	return err
}

func (s *searchLink) Confirm(ctx context.Context, device btsearch.Device) (bool, error) {
	if err := s.Valid(); err != nil {
		return false, err
	}
	list, err := readPairingList([]byte{device.BluetoothType}, func(op byte, payload []byte) ([]byte, error) {
		return s.boundExchange(ctx, gnpFlagQuery, gnpClassPairingDevice, op, payload)
	})
	if err != nil {
		return false, err
	}
	if err := s.Valid(); err != nil {
		return false, err
	}
	for _, entry := range list.pairedDevices {
		if entry.deviceBTAddr == device.Address && entry.bluetoothType == device.BluetoothType && entry.isConnected {
			return true, nil
		}
	}
	return false, nil
}

func (s *searchLink) boundExchange(ctx context.Context, flags, class, opcode byte, payload []byte) ([]byte, error) {
	operation, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ctx = operation
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !gnpIOMu.TryLock() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
	}
	defer gnpIOMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.Valid(); err != nil {
		return nil, err
	}
	transport, err := s.binding.Open()
	if err != nil {
		return nil, err
	}
	h := &hidrawConn{control: transport, reportSize: transport.Layout.OutputBytes}
	defer h.close()
	seq := nextSeq()
	packet, err := buildGNPReport(gnpSrcDongle, seq, flags, class, opcode, payload)
	if err != nil {
		return nil, err
	}
	if err := s.Valid(); err != nil {
		return nil, err
	}
	if err := transport.WriteContext(ctx, packet); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(2 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := s.Valid(); err != nil {
			return nil, err
		}
		reply, err := h.read(min(100*time.Millisecond, time.Until(deadline)))
		if errors.Is(err, firmware.ErrControlReadTimeout) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(reply) > 0 && reply[0] == gnpReportID {
			reply = reply[1:]
		}
		if flags == gnpFlagQuery {
			if matchesGNPQueryReply(reply, gnpSrcDongle, seq, class, opcode) {
				return parseGNPReplyPayload(reply, seq, class, opcode)
			}
		} else if matched, err := matchGNPWriteReply(reply, gnpSrcDongle, seq); matched {
			return nil, err
		}
	}
	return nil, errors.New("dongle management acknowledgement timed out")
}
