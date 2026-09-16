package firmware

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

const bulkPacketSize = 512
const bulkDataSize = bulkPacketSize - 6

var bulkCRCTable = func() [256]uint16 {
	var table [256]uint16
	for i := range table {
		value := uint16(i) << 8
		for bit := 0; bit < 8; bit++ {
			if value&0x8000 != 0 {
				value = value<<1 ^ 0x1021
			} else {
				value <<= 1
			}
		}
		table[i] = value
	}
	return table
}()

func bulkCRC(crc uint16, data []byte) uint16 {
	for _, value := range data {
		crc = crc<<8 ^ bulkCRCTable[byte(crc>>8)^value]
	}
	return crc
}

func encodeBulkCommand(packet []byte, command byte, data []byte) error {
	if len(packet) != bulkPacketSize || len(data) > bulkDataSize {
		return errors.New("invalid camera bulk packet size")
	}
	switch command {
	case 1:
		if len(data) != 4 {
			return errors.New("camera start command needs a file size")
		}
	case 2:
	case 3, 4:
		if len(data) != 0 {
			return errors.New("camera control command has unexpected data")
		}
	default:
		return errors.New("unknown camera bulk command")
	}
	clear(packet)
	packet[0], packet[1] = 0xaa, command
	binary.LittleEndian.PutUint16(packet[2:4], uint16(len(data)))
	copy(packet[6:], data)
	checksum := bulkCRC(bulkCRC(0xffff, packet[1:4]), data)
	binary.LittleEndian.PutUint16(packet[4:6], checksum)
	return nil
}

type bulkMessage struct {
	ID  byte
	MD5 [16]byte
}

func decodeBulkMessage(packet []byte) (bulkMessage, error) {
	if len(packet) < 6 || len(packet) > bulkPacketSize || packet[0] != 0xaa {
		return bulkMessage{}, errors.New("invalid camera bulk reply")
	}
	message := bulkMessage{ID: packet[1]}
	if message.ID < 16 || message.ID > 25 {
		return message, errors.New("unknown camera bulk reply")
	}
	// The reference updater's replies do not promise command-style CRC fields.
	// MD5 data starts at byte six; reject a truncated checksum before comparing.
	if message.ID == 22 {
		if len(packet) < 22 {
			return message, errors.New("short staged-camera checksum")
		}
		copy(message.MD5[:], packet[6:22])
	}
	return message, nil
}
func bulkMessageError(message bulkMessage) error {
	labels := map[byte]string{16: "wrong packet marker", 17: "packet checksum failed", 18: "could not open staging file", 19: "could not save staging file", 20: "missing transfer start", 21: "staged file size mismatch", 25: "camera is already updating"}
	if label, ok := labels[message.ID]; ok {
		return fmt.Errorf("camera bulk transfer: %s", label)
	}
	return fmt.Errorf("unexpected camera bulk reply %d", message.ID)
}

type bulkCameraIO interface {
	Write(context.Context, []byte) error
	Read(context.Context) ([]byte, error)
}
type bulkReadResult struct {
	message bulkMessage
	err     error
}

// Transfer the original archive as a stream. No per-data-packet ACK exists:
// one reader watches for device errors while the writer sends the file.
// Staging is complete only after the device returns the same whole-file MD5.
func transferBulkCamera(ctx context.Context, transport bulkCameraIO, source io.Reader, size int64, digest [16]byte, progress func(int64, int64)) error {
	if transport == nil || source == nil || size < 1 || size > 1<<32-1 {
		return errors.New("invalid camera file transfer")
	}
	// Drain replies left by an interrupted session, but never ignore a live
	// update-in-progress response or an unbounded stream of stale packets.
	drain, cancelDrain := context.WithTimeout(ctx, 250*time.Millisecond)
	for count := 0; ; count++ {
		if count >= 64 {
			cancelDrain()
			return errors.New("camera bulk reply queue did not become idle")
		}
		raw, err := transport.Read(drain)
		if err != nil {
			cancelDrain()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) {
				break
			}
			return err
		}
		message, err := decodeBulkMessage(raw)
		if err != nil {
			cancelDrain()
			return err
		}
		if message.ID == 25 {
			cancelDrain()
			return bulkMessageError(message)
		}
	}
	readContext, cancelRead := context.WithCancel(ctx)
	results := make(chan bulkReadResult, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			raw, err := transport.Read(readContext)
			message := bulkMessage{}
			if err == nil {
				message, err = decodeBulkMessage(raw)
			}
			select {
			case results <- bulkReadResult{message: message, err: err}:
			case <-readContext.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { cancelRead(); <-done }()
	waitFor := func(wanted byte, timeout time.Duration) (bulkMessage, error) {
		wait, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		select {
		case <-wait.Done():
			return bulkMessage{}, wait.Err()
		case result := <-results:
			if result.err != nil {
				return bulkMessage{}, result.err
			}
			if result.message.ID != wanted {
				return result.message, bulkMessageError(result.message)
			}
			return result.message, nil
		}
	}
	packet := make([]byte, bulkPacketSize)
	send := func(command byte, data []byte) error {
		if err := encodeBulkCommand(packet, command, data); err != nil {
			return err
		}
		write, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return transport.Write(write, packet)
	}
	if err := send(4, nil); err != nil {
		return err
	}
	staged, err := waitFor(22, 20*time.Second)
	if err != nil {
		return err
	}
	if staged.MD5 == digest {
		if progress != nil {
			progress(size, size)
		}
		return nil
	}
	var fileSize [4]byte
	binary.LittleEndian.PutUint32(fileSize[:], uint32(size))
	if err := send(1, fileSize[:]); err != nil {
		return err
	}
	if _, err := waitFor(23, 5*time.Second); err != nil {
		return err
	}
	data := make([]byte, bulkDataSize)
	written := int64(0)
	for written < size {
		select {
		case result := <-results:
			if result.err != nil {
				return result.err
			}
			return bulkMessageError(result.message)
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		count := int(min(int64(len(data)), size-written))
		if _, err := io.ReadFull(source, data[:count]); err != nil {
			return fmt.Errorf("read immutable camera file: %w", err)
		}
		if err := send(2, data[:count]); err != nil {
			return err
		}
		written += int64(count)
		if progress != nil {
			progress(written, size)
		}
	}
	var extra [1]byte
	if n, err := source.Read(extra[:]); n != 0 || err != io.EOF {
		return errors.New("camera source size changed during transfer")
	}
	// Any error queued before End belongs to the data transfer, not a new
	// request. The single reader preserves order through the final replies.
	select {
	case result := <-results:
		if result.err != nil {
			return result.err
		}
		return bulkMessageError(result.message)
	default:
	}
	if err := send(3, nil); err != nil {
		return err
	}
	if _, err := waitFor(24, 20*time.Second); err != nil {
		return err
	}
	if err := send(4, nil); err != nil {
		return err
	}
	staged, err = waitFor(22, 20*time.Second)
	if err != nil {
		return err
	}
	if staged.MD5 != digest {
		return errors.New("camera staged archive checksum did not match")
	}
	return nil
}
