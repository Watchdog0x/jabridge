package firmware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// Conexant has no transaction IDs. Keep one request outstanding, and stop
// on the first timeout or unexpected reply; a retry could consume a late reply.
type conexantIO interface {
	Write(context.Context, []byte) error
	Read(context.Context) ([]byte, error)
}

type conexantClient struct {
	io      conexantIO
	plus    bool
	maxData int
	wait    func(context.Context, time.Duration) error
}

func (c *conexantClient) send(ctx context.Context, packet []byte) error {
	if c.io == nil || c.wait == nil || c.maxData < 1 || c.maxData > 255 {
		return errors.New("incomplete UC Voice transport")
	}
	if err := c.wait(ctx, 50*time.Millisecond); err != nil {
		return err
	}
	request, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return c.io.Write(request, packet)
}

func (c *conexantClient) receive(ctx context.Context, count int) ([]byte, error) {
	request, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	reply, err := c.io.Read(request)
	if err != nil {
		return nil, err
	}
	if len(reply) < count {
		return nil, errors.New("short UC Voice firmware reply")
	}
	return append([]byte(nil), reply[:count]...), nil
}

func (c *conexantClient) read(ctx context.Context, address uint32, count int) ([]byte, error) {
	if count < 1 || uint64(address)+uint64(count) > 1<<16 || !c.plus && uint64(address)+uint64(count) > 1<<15 {
		return nil, errors.New("invalid UC Voice read range")
	}
	var result []byte
	for len(result) < count {
		n := 1
		var packet []byte
		var err error
		if c.plus {
			n = min(count-len(result), c.maxData)
			packet, err = conexantPlusPacket(address+uint32(len(result)), nil, n, false)
		} else {
			packet, err = conexantLegacyPacket(address+uint32(len(result)), 0, false)
		}
		if err != nil {
			return nil, err
		}
		if err := c.send(ctx, packet); err != nil {
			return nil, err
		}
		replySize := n
		if !c.plus {
			replySize = 2
		}
		reply, err := c.receive(ctx, replySize)
		if err != nil {
			return nil, err
		}
		if c.plus {
			result = append(result, reply...)
		} else {
			result = append(result, reply[1])
		}
	}
	return result, nil
}

func (c *conexantClient) writeVerified(ctx context.Context, address uint32, data []byte) error {
	if len(data) < 1 || len(data) > c.maxData || uint64(address)+uint64(len(data)) > 1<<16 {
		return errors.New("invalid UC Voice write range")
	}
	var packet []byte
	var err error
	if c.plus {
		packet, err = conexantPlusPacket(address, data, len(data), true)
	} else {
		if len(data) != 1 || address >= 1<<15 {
			return errors.New("legacy UC Voice writes one byte at a time")
		}
		packet, err = conexantLegacyPacket(address, data[0], true)
	}
	if err != nil {
		return err
	}
	if err := c.send(ctx, packet); err != nil {
		return err
	}
	if !c.plus {
		reply, err := c.receive(ctx, 2)
		if err != nil {
			return err
		}
		if reply[1] != data[0] {
			return errors.New("UC Voice firmware write acknowledgement mismatch")
		}
	}
	got, err := c.read(ctx, address, len(data))
	if err != nil {
		return err
	}
	if !bytes.Equal(got, data) {
		return errors.New("UC Voice firmware readback mismatch")
	}
	return nil
}

// Only a digest of calibration is persisted. A retry uses its original range:
// the patch may already have replaced the EEPROM pointer at 0x38/0x39.
type conexantCalibration struct {
	Start  uint32 `json:"start"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

func (c *conexantClient) captureCalibration(ctx context.Context) (conexantCalibration, error) {
	pointer, err := c.read(ctx, 0x38, 2)
	if err != nil {
		return conexantCalibration{}, err
	}
	start := uint32(binary.LittleEndian.Uint16(pointer))
	header, err := c.read(ctx, start, 2)
	if err != nil {
		return conexantCalibration{}, err
	}
	count := int(header[0])
	if header[1] != 3 || count == 0 {
		return conexantCalibration{}, nil
	}
	if !c.plus {
		count++
	} // Legacy exclusion includes both end points.
	calibration := conexantCalibration{Start: start, Size: count}
	data, err := c.read(ctx, start, count)
	if err != nil {
		return conexantCalibration{}, err
	}
	calibration.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	return calibration, nil
}

func (c *conexantClient) verifyCalibration(ctx context.Context, calibration conexantCalibration) error {
	if calibration.Size == 0 {
		if calibration.Start != 0 || calibration.SHA256 != "" {
			return errors.New("invalid empty calibration record")
		}
		return nil
	}
	if calibration.Size < 1 || calibration.Size > 256 || uint64(calibration.Start)+uint64(calibration.Size) > 1<<16 || len(calibration.SHA256) != 64 {
		return errors.New("invalid UC Voice calibration record")
	}
	data, err := c.read(ctx, calibration.Start, calibration.Size)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != calibration.SHA256 {
		return errors.New("UC Voice calibration changed; stopping the update")
	}
	return nil
}

func conexantTransferPlan(records []conexantRecord, plus bool, maxData int, calibration conexantCalibration) ([]conexantRecord, error) {
	if len(records) == 0 || maxData < 1 || maxData > 255 || calibration.Size < 0 || calibration.Size > 256 || uint64(calibration.Start)+uint64(calibration.Size) > 1<<16 {
		return nil, errors.New("invalid UC Voice transfer plan")
	}
	start, end := calibration.Start, calibration.Start+uint32(calibration.Size)
	var plan []conexantRecord
	for _, record := range records {
		if len(record.Data) == 0 || uint64(record.Address)+uint64(len(record.Data)) > 1<<16 {
			return nil, errors.New("invalid UC Voice patch range")
		}
		if !plus {
			if uint64(record.Address)+uint64(len(record.Data)) > 1<<15 {
				return nil, errors.New("legacy UC Voice patch exceeds the readable address range")
			}
			for i, value := range record.Data {
				address := record.Address + uint32(i)
				// The vendor's legacy exclusion compares twelve address
				// bits. This projection is not the packet's address encoding.
				projected := address & 0xfff
				if calibration.Size > 0 && projected >= start && projected < end {
					continue
				}
				plan = append(plan, conexantRecord{address, []byte{value}})
			}
			continue
		}
		for _, piece := range conexantWritablePieces(record, start, end) {
			for offset := 0; offset < len(piece.Data); {
				n := min(maxData, len(piece.Data)-offset)
				plan = append(plan, conexantRecord{piece.Address + uint32(offset), append([]byte(nil), piece.Data[offset:offset+n]...)})
				offset += n
			}
		}
	}
	if len(plan) == 0 {
		return nil, errors.New("UC Voice patch has no writable data")
	}
	return plan, nil
}

func transferConexant(ctx context.Context, client *conexantClient, records []conexantRecord, calibration conexantCalibration, checkpoint func() error, progress func(int, int)) error {
	if client == nil || checkpoint == nil {
		return errors.New("incomplete UC Voice transfer")
	}
	plan, err := conexantTransferPlan(records, client.plus, client.maxData, calibration)
	if err != nil {
		return err
	}
	if err := client.verifyCalibration(ctx, calibration); err != nil {
		return err
	}
	if err := checkpoint(); err != nil {
		return err
	}
	total := 0
	for _, record := range plan {
		total += len(record.Data)
	}
	done := 0
	for index, record := range plan {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Published patches end with an activation write. Check protected
		// data again before the final write, as well as after completion.
		if index == len(plan)-1 {
			if err := client.verifyCalibration(ctx, calibration); err != nil {
				return err
			}
		}
		if err := client.writeVerified(ctx, record.Address, record.Data); err != nil {
			return fmt.Errorf("UC Voice firmware transfer at 0x%04x: %w", record.Address, err)
		}
		done += len(record.Data)
		if progress != nil {
			progress(done, total)
		}
	}
	return client.verifyCalibration(ctx, calibration)
}
