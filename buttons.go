package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Only media/call keys are printed. This never grabs the device or reads raw
// hidraw packets, so normal desktop shortcuts and the daemon keep working.
func runButtons(args []string) error {
	seconds := 20
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Println("Usage: jabridge buttons [--seconds 20]\nListen to Jabra media buttons and volume wheels. Press Ctrl+C to stop.\nOnly events exposed by Linux are shown; vendor-only buttons may need further support.")
		return nil
	}
	if len(args) != 0 {
		if len(args) != 2 || args[0] != "--seconds" {
			return errors.New("usage: jabridge buttons [--seconds 20]")
		}
		value, err := strconv.Atoi(args[1])
		if err != nil || value < 1 || value > 300 {
			return errors.New("seconds must be between 1 and 300")
		}
		seconds = value
	}
	paths := jabraInputPaths()
	var fds []unix.PollFd
	defer func() {
		for _, fd := range fds {
			_ = unix.Close(int(fd.Fd))
		}
	}()
	for _, path := range paths {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			fmt.Printf("%s: %s\n", filepath.Base(path), diagnosticError(err))
			continue
		}
		fds = append(fds, unix.PollFd{Fd: int32(fd), Events: unix.POLLIN})
	}
	if len(fds) == 0 {
		return errors.New("no accessible Jabra input events; run jabridge setup, reconnect USB, then jabridge debug")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	fmt.Printf("Listening for %d seconds. Press headset buttons or turn the wheel. Ctrl+C stops.\n", seconds)
	eventSize := 2*(strconv.IntSize/8) + 8
	buffer := make([]byte, eventSize*64)
	count := 0
	for ctx.Err() == nil {
		if _, err := unix.Poll(fds, 100); err != nil {
			if err == unix.EINTR {
				continue
			}
			return err
		}
		for _, fd := range fds {
			if fd.Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
				return errors.New("headset input disconnected; reconnect and run again")
			}
			if fd.Revents&unix.POLLIN == 0 {
				continue
			}
			n, err := unix.Read(int(fd.Fd), buffer)
			if err == unix.EAGAIN || err == unix.EINTR {
				continue
			}
			if err != nil {
				return fmt.Errorf("read Jabra input: %s", diagnosticError(err))
			}
			for offset := 0; offset+eventSize <= n; offset += eventSize {
				event := buffer[offset+eventSize-8 : offset+eventSize]
				if binary.NativeEndian.Uint16(event) == unix.EV_SYN && binary.NativeEndian.Uint16(event[2:]) == 3 {
					return errors.New("input event queue overflowed; run the button check again")
				}
				label := mediaInputEvent(binary.NativeEndian.Uint16(event), binary.NativeEndian.Uint16(event[2:]), int32(binary.NativeEndian.Uint32(event[4:])))
				if label != "" {
					fmt.Println(label)
					count++
				}
			}
		}
	}
	fmt.Printf("Finished: %d media/call events.\n", count)
	return nil
}

func jabraInputPaths() []string {
	entries, _ := os.ReadDir("/sys/class/input")
	var paths []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "event") {
			continue
		}
		vendor, err := os.ReadFile(filepath.Join("/sys/class/input", entry.Name(), "device/id/vendor"))
		if err == nil && strings.EqualFold(strings.TrimSpace(string(vendor)), "0b0e") {
			paths = append(paths, filepath.Join("/dev/input", entry.Name()))
		}
	}
	return paths
}

func mediaInputEvent(kind, code uint16, value int32) string {
	// Stable Linux UAPI values from linux/input-event-codes.h.
	if kind == unix.EV_REL && (code == 0x08 || code == 0x06 || code == 0x07) {
		return fmt.Sprintf("Volume wheel/dial: %+d", value)
	}
	if kind != unix.EV_KEY {
		return ""
	}
	name := map[uint16]string{
		113: "Mute", 248: "Microphone mute",
		115: "Volume up", 114: "Volume down",
		169: "Call", 0x1be: "Hang up",
		164: "Play/pause", 207: "Play", 119: "Pause",
		166: "Stop", 163: "Next track", 165: "Previous track",
	}[code]
	state := map[int32]string{0: "released", 1: "pressed", 2: "repeat"}[value]
	if name == "" || state == "" {
		return ""
	}
	return name + ": " + state
}
