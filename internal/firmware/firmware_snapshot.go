package firmware

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Headset archives remain bounded by the expanded-archive limit. Only known
// bulk-camera manifests may use the larger streamed-archive budget.
const maxNativeArchive = MaxExpandedArchiveSize
const firmwareSeals = unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL

type firmwareSnapshot struct {
	file   *os.File
	path   string
	digest string
}

func (s *firmwareSnapshot) Close() error { return s.file.Close() }

// freezeFirmwareFile captures one input before validation or confirmation.
// Kernel seals make every later parser/hash/transfer read the same bytes,
// even if the original pathname is replaced or modified by another process.
func freezeFirmwareFile(path string) (*firmwareSnapshot, error) {
	input, info, err := openFirmwareRead(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if input != nil {
			_ = input.Close()
		}
	}()
	limit := maxNativeArchive
	if info.Size() > limit {
		manifest, err := parseFirmwareManifestAt(input, info.Size())
		if err != nil || !isBulkCameraManifest(manifest) || info.Size() > maxCameraArchive {
			return nil, fmt.Errorf("native firmware archive exceeds its supported size limit")
		}
		limit = maxCameraArchive
	}
	// Reopening an already sealed snapshot keeps the same immutable inode;
	// do not duplicate a multi-gigabyte camera archive for nested validation.
	if seals, err := unix.FcntlInt(input.Fd(), unix.F_GET_SEALS, 0); err == nil && seals&firmwareSeals == firmwareSeals {
		snapshot, err := describeFirmwareSnapshot(input)
		if err != nil {
			return nil, err
		}
		input = nil
		return snapshot, nil
	}
	fd, err := unix.MemfdCreate("jabridge-firmware", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, fmt.Errorf("create immutable firmware snapshot: %w", err)
	}
	file := os.NewFile(uintptr(fd), "jabridge-firmware")
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
		}
	}()
	n, err := io.Copy(file, io.LimitReader(input, limit+1))
	if err != nil {
		return nil, fmt.Errorf("copy firmware snapshot: %w", err)
	}
	after, err := input.Stat()
	if err != nil || n != info.Size() || after.Size() != info.Size() || n > limit {
		return nil, errors.New("firmware changed while creating snapshot")
	}
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, firmwareSeals); err != nil {
		return nil, fmt.Errorf("seal firmware snapshot: %w", err)
	}
	snapshot, err := describeFirmwareSnapshot(file)
	if err != nil {
		return nil, err
	}
	keep = true
	return snapshot, nil
}

// Hash only the sealed object, never the mutable source pathname.
func describeFirmwareSnapshot(file *os.File) (*firmwareSnapshot, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return nil, err
	}
	return &firmwareSnapshot{file: file, path: fmt.Sprintf("/proc/self/fd/%d", file.Fd()), digest: hex.EncodeToString(digest.Sum(nil))}, nil
}

// Ordinary input symlinks and special files are rejected. The sole symlink
// exception is a local descriptor whose contents are already kernel-sealed.
func openFirmwareRead(path string) (*os.File, os.FileInfo, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK | unix.O_NOFOLLOW
	fd, err := unix.Open(path, flags, 0)
	if errors.Is(err, unix.ELOOP) && strings.HasPrefix(path, "/proc/self/fd/") {
		number := strings.TrimPrefix(path, "/proc/self/fd/")
		if _, parseErr := strconv.ParseUint(number, 10, 32); parseErr != nil {
			return nil, nil, errors.New("invalid firmware snapshot descriptor")
		}
		fd, err = unix.Open(path, flags&^unix.O_NOFOLLOW, 0)
		if err == nil {
			seals, sealErr := unix.FcntlInt(uintptr(fd), unix.F_GET_SEALS, 0)
			if sealErr != nil || seals&firmwareSeals != firmwareSeals {
				_ = unix.Close(fd)
				return nil, nil, errors.New("firmware descriptor is not immutable")
			}
		}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open firmware: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > MaxFirmwareSize {
		_ = file.Close()
		return nil, nil, errors.New("firmware must be a nonempty, bounded regular file")
	}
	return file, info, nil
}
