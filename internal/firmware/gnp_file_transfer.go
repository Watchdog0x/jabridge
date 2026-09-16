package firmware

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

type gnpCameraFile struct {
	Name string
	Size uint32
	MD5  [16]byte
}
type gnpCameraFiles interface {
	Request(context.Context, byte, []byte) ([]byte, error)
}
type nativeCameraFiles struct {
	runtime *sitelRuntime
	address byte
	device  USBDevice
}

func (c nativeCameraFiles) Request(ctx context.Context, flags byte, body []byte) ([]byte, error) {
	if err := validateUSBDevice(c.device); err != nil {
		return nil, managementNotSent(err)
	}
	if flags != 0x40 {
		if err := requireHardwareWrites(); err != nil {
			return nil, managementNotSent(err)
		}
	}
	return c.runtime.exchangeTimeout(ctx, c.address, 3, flags, body, 10*time.Second)
}

func listCameraFiles(ctx context.Context, peer gnpCameraFiles) ([]gnpCameraFile, error) {
	data, err := peer.Request(ctx, 0x40, []byte{3, 0})
	if err != nil {
		return nil, err
	}
	if len(data) < 2 {
		return nil, errors.New("invalid camera file listing header")
	}
	count := int(data[1])
	files := make([]gnpCameraFile, 0, count)
	seen := map[string]bool{}
	for range count {
		data, err := peer.Request(ctx, 0x40, []byte{3, 0x40})
		if err != nil {
			return nil, err
		}
		if len(data) < 26 || int(data[0]&0x3f) < 25 || int(data[0]&0x3f) >= len(data) || len(data) > 58 {
			return nil, errors.New("invalid camera file entry length")
		}
		// The reference reader uses this embedded length and ignores report
		// padding after the name. Do not turn padding into part of a filename.
		name := string(data[25 : int(data[0]&0x3f)+1])
		if strings.ContainsAny(name, "\x00/\\") || name == "." || name == ".." || seen[name] {
			return nil, errors.New("invalid or duplicate camera file name")
		}
		for _, b := range []byte(name) {
			if b < 32 || b > 126 {
				return nil, errors.New("camera file name is not ASCII")
			}
		}
		seen[name] = true
		entry := gnpCameraFile{Name: name, Size: binary.BigEndian.Uint32(data[21:25])}
		copy(entry.MD5[:], data[5:21])
		files = append(files, entry)
	}
	return files, nil
}

// FILE.WRITE is the vendor's protocol-13 fallback for camera upgrade.zip.
// Acknowledged blocks bound each group of at most100 events. Device-reported
// size AND MD5 are required before activation; an ACK alone is insufficient.
func transferCameraGNPFile(ctx context.Context, peer gnpCameraFiles, source io.Reader, size int64, digest [16]byte, progress func(int64, int64)) (resultErr error) {
	if peer == nil || source == nil || size < 1 || size > MaxExpandedArchiveSize {
		return errors.New("invalid camera GNP file transfer")
	}
	matches := func() (bool, error) {
		files, err := listCameraFiles(ctx, peer)
		if err != nil {
			return false, err
		}
		for _, file := range files {
			if file.Name == "upgrade.zip" {
				return file.Size == uint32(size) && file.MD5 == digest, nil
			}
		}
		return false, nil
	}
	if same, err := matches(); err != nil {
		return err
	} else if same {
		if progress != nil {
			progress(size, size)
		}
		return nil
	}
	name := []byte("upgrade.zip")
	header := make([]byte, 6+len(name))
	header[0], header[1] = 0, byte(4+len(name))
	binary.BigEndian.PutUint32(header[2:6], uint32(size))
	copy(header[6:], name)
	// A failed or cancelled stream must release the device's file writer.
	// Cancellation uses a fresh bounded context, as the vendor transfer does.
	mayHaveStarted := false
	defer func() {
		if resultErr == nil || !mayHaveStarted {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := peer.Request(cleanup, 0x80, []byte{0, 0x80}); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("cancel camera file transfer: %w", err))
		}
	}()
	if _, err := peer.Request(ctx, 0x80, header); err != nil {
		mayHaveStarted = !errors.Is(err, errSitelRejected) && !errors.Is(err, errManagementNotSent)
		return err
	}
	mayHaveStarted = true
	buffer := make([]byte, 58)
	buffer[0] = 0
	written := int64(0)
	block := 0
	hash := md5.New()
	for written < size {
		if err := ctx.Err(); err != nil {
			return err
		}
		count := int(min(int64(55), size-written))
		if _, err := io.ReadFull(source, buffer[3:3+count]); err != nil {
			return fmt.Errorf("read sealed camera bundle: %w", err)
		}
		_, _ = hash.Write(buffer[3 : 3+count])
		buffer[1], buffer[2] = 0x40|byte(count+1), byte(block)
		flags := byte(0)
		if block%100 == 0 {
			flags = 0x80
		}
		if _, err := peer.Request(ctx, flags, buffer[:3+count]); err != nil {
			return err
		}
		written += int64(count)
		block++
		if progress != nil {
			progress(written, size)
		}
	}
	var extra [1]byte
	if n, err := source.Read(extra[:]); n != 0 || err != io.EOF {
		return errors.New("camera bundle size changed during transfer")
	}
	if actual := hash.Sum(nil); string(actual) != string(digest[:]) {
		return errors.New("camera bundle changed during transfer")
	}
	if same, err := matches(); err != nil {
		return err
	} else if !same {
		return errors.New("camera staged bundle size or checksum did not match")
	}
	return nil
}
