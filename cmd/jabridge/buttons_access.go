package main

import (
	"errors"
	"os"
	"syscall"
)

var errNoJabraInputNodes = errors.New("no Jabra Linux input nodes visible; check USB/HID detection in jabridge debug. The device may have no Linux button events; this is not proof of denied access")

// Only a real denied open justifies setup advice. An empty list, a disconnect
// and a busy node need different next steps, including in the standalone CLI.
func buttonInputUnavailable(openErrors []error) error {
	if len(openErrors) == 0 {
		return errNoJabraInputNodes
	}
	var denied, gone bool
	for _, err := range openErrors {
		denied = denied || errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM)
		gone = gone || errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENODEV)
	}
	switch {
	case denied:
		return errors.New("input access was denied; run jabridge setup on the host, reconnect USB if asked, then repeat debug as your normal user")
	case gone:
		return errors.New("input node disappeared or is not visible here; check the USB connection and host/container device visibility, then repeat debug")
	default:
		return errors.New("input nodes could not be opened; see the input access results above. No permission error was reported")
	}
}
