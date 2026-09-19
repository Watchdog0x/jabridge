package firmware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Keep paths injectable only inside this package so kernel-backed tests can
// supply a synthetic USB tree. Production always uses the normal Linux paths.
type hidrawPaths struct{ class, char, dev string }

func linuxHidrawPaths() hidrawPaths {
	return hidrawPaths{class: "/sys/class/hidraw", char: "/sys/dev/char", dev: "/dev"}
}

type hidAccessError struct {
	stage string
	cause error
}

func (e *hidAccessError) Error() string { return fmt.Sprintf("%s: %v", e.stage, e.cause) }
func (e *hidAccessError) Unwrap() error { return e.cause }

// HistoryCode contains no paths, serials, descriptors or arbitrary error text.
func (e *hidAccessError) HistoryCode() string {
	var inner *hidAccessError
	if errors.As(e.cause, &inner) {
		return inner.HistoryCode()
	}
	if e.stage == "hid-handshake" && (errors.Is(e.cause, context.DeadlineExceeded) || strings.Contains(e.cause.Error(), "timed out")) {
		return "hid-handshake-timeout"
	}
	if e.stage == "hid-open" {
		switch {
		case errors.Is(e.cause, os.ErrPermission), errors.Is(e.cause, unix.EPERM):
			return "hid-open-permission"
		case errors.Is(e.cause, os.ErrNotExist):
			return "hid-open-missing"
		case errors.Is(e.cause, unix.ELOOP):
			return "hid-open-symlink"
		case errors.Is(e.cause, unix.EBUSY):
			return "hid-open-busy"
		case errors.Is(e.cause, unix.ENODEV), errors.Is(e.cause, unix.ENXIO):
			return "hid-open-disconnected"
		}
	}
	switch e.stage {
	case "hid-usb-binding", "hid-scan", "hid-no-match", "hid-open", "hid-identity", "hid-handle-type", "hid-handle-stat", "hid-handle-parent", "hid-info-ioctl", "hid-info-mismatch", "hid-descriptor", "hid-parse", "hid-layout", "hid-ambiguous", "hid-handshake":
		return e.stage
	default:
		return "failed"
	}
}

func hidAccessFailure(stage string, err error) error {
	return &hidAccessError{stage: stage, cause: err}
}

func HIDAccessFailureCode(err error) string {
	if err == nil {
		return "ready"
	}
	var failure *hidAccessError
	if errors.As(err, &failure) {
		return failure.HistoryCode()
	}
	return "failed"
}

// CheckSitelBootAccess performs the same Linux open/identity/descriptor checks
// as the installer. It never starts the protocol or sends a device command.
func CheckSitelBootAccess(pid uint16) error {
	profile, ok := sitelProfileForPID(pid)
	if !ok || pid != profile.BootPID {
		return errors.New("not a supported Sitel bootloader")
	}
	devices, err := enumerateBoundUSB()
	if err != nil {
		return hidAccessFailure("hid-scan", err)
	}
	var selected []USBDevice
	for _, device := range devices {
		if device.ProductID == pid {
			selected = append(selected, device)
		}
	}
	if len(selected) > 1 {
		return hidAccessFailure("hid-ambiguous", errors.New("multiple matching USB attachments"))
	}
	if len(selected) == 0 {
		return hidAccessFailure("hid-no-match", errors.New("one matching USB attachment is required"))
	}
	raw, err := openSitelBoot(selected[0])
	if err != nil {
		return err
	}
	return raw.file.Close()
}
