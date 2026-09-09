package firmware

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// Management uses FF00:0001; HID report numbers and sizes come from the
// descriptor. Firmware flashing retains its separate protocol-specific path.
type ControlLayout struct {
	InputID, OutputID       byte
	InputBytes, OutputBytes int
}

func SelectControlLayout(reports []HIDReport) (ControlLayout, error) {
	var layout ControlLayout
	for _, report := range reports {
		if report.Kind != "input" && report.Kind != "output" {
			continue
		}
		if report.ID == 0 || report.Bytes < 7 || report.Bytes > 65 || len(report.Fields) == 0 {
			continue
		}
		bits := uint64(0)
		valid := true
		for _, field := range report.Fields {
			if field.collectionPage == 0xff54 || field.collectionPage == 0xff55 {
				valid = false
				break
			}
			if field.OffsetBits != bits || field.SizeBits != 8 || field.UsagePage != 0xff00 || field.Flags&1 != 0 || len(field.Usages) != 1 || field.Usages[0] != 1 {
				valid = false
				break
			}
			bits += uint64(field.SizeBits) * uint64(field.Count)
		}
		if !valid || int(bits/8)+1 != report.Bytes {
			continue
		}
		if report.Kind == "input" {
			if layout.InputID != 0 {
				return ControlLayout{}, errors.New("ambiguous management input reports")
			}
			layout.InputID, layout.InputBytes = report.ID, report.Bytes
		} else {
			if layout.OutputID != 0 {
				return ControlLayout{}, errors.New("ambiguous management output reports")
			}
			layout.OutputID, layout.OutputBytes = report.ID, report.Bytes
		}
	}
	if layout.InputID == 0 || layout.OutputID == 0 {
		return ControlLayout{}, errors.New("no byte-array management usage FF00:0001 in both directions")
	}
	return layout, nil
}

func InspectControlLayout(path string) (ControlLayout, error) {
	reports, err := InspectHIDReports(path)
	if err != nil {
		return ControlLayout{}, err
	}
	return SelectControlLayout(reports)
}

func HasControlLayout(path string) bool { _, err := InspectControlLayout(path); return err == nil }

func (l ControlLayout) Encode(report []byte) ([][]byte, error) {
	if len(report) < 7 || report[0] != GnpReportID {
		return nil, errors.New("invalid canonical management report")
	}
	length := int(report[4] & 0x3f)
	if length < 6 || length+1 > len(report) {
		return nil, errors.New("invalid management packet length")
	}
	if l.OutputBytes < 7 || l.OutputBytes > 65 || l.OutputID == 0 {
		return nil, errors.New("invalid management output layout")
	}
	if kind := report[4] & 0xc0; kind != 0x40 && kind != 0x80 {
		return nil, errors.New("management packet must be a query or command")
	}
	packet := report[1 : length+1]
	var frames [][]byte
	for len(packet) > 0 {
		count := min(l.OutputBytes-1, len(packet))
		frame := make([]byte, l.OutputBytes)
		frame[0] = l.OutputID
		copy(frame[1:], packet[:count])
		frames = append(frames, frame)
		packet = packet[count:]
	}
	return frames, nil
}

type controlAssembler struct {
	layout ControlLayout
	packet []byte
	length int
}

// ControlPacketAssembler exposes the same bounded framing to passive debug
// observers. It never opens a device or sends a packet.
type ControlPacketAssembler struct{ state controlAssembler }

func NewControlPacketAssembler(layout ControlLayout) *ControlPacketAssembler {
	return &ControlPacketAssembler{state: controlAssembler{layout: layout}}
}

func (a *ControlPacketAssembler) Push(frame []byte) ([]byte, error) { return a.state.push(frame) }

// Reset discards an incomplete packet, for example after a reconnect.
func (a *ControlPacketAssembler) Reset() { a.state.reset() }

func (a *controlAssembler) reset() {
	a.packet = nil
	a.length = 0
}

func (a *controlAssembler) push(frame []byte) (packet []byte, err error) {
	defer func() {
		if err != nil {
			a.reset()
		}
	}()
	if a.layout.InputID == 0 || a.layout.InputBytes < 7 || a.layout.InputBytes > 65 {
		return nil, errors.New("invalid management input layout")
	}
	if len(frame) == 0 || frame[0] != a.layout.InputID {
		return nil, nil
	}
	if len(frame) > a.layout.InputBytes {
		return nil, errors.New("management input exceeds descriptor size")
	}
	if len(frame) < 2 {
		return nil, errors.New("empty management input report")
	}
	chunk := frame[1:min(len(frame), a.layout.InputBytes)]
	if len(a.packet) == 0 {
		if len(chunk) < 5 {
			return nil, errors.New("short management header")
		}
		a.length = int(chunk[3] & 0x3f)
		if a.length < 5 || (chunk[4] != 0xff && a.length < 6) {
			return nil, errors.New("invalid management reply length")
		}
	}
	count := min(a.length-len(a.packet), len(chunk))
	a.packet = append(a.packet, chunk[:count]...)
	if len(a.packet) < a.length {
		return nil, nil
	}
	result := append([]byte{GnpReportID}, a.packet...)
	a.reset()
	return result, nil
}

type ControlHidraw struct {
	file   *os.File
	Layout ControlLayout
}

func OpenControlHidraw(path string) (*ControlHidraw, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	descriptor, err := readHidrawReportDescriptorFile(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	reports, err := parseHIDReports(descriptor)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	layout, err := SelectControlLayout(reports)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &ControlHidraw{file: file, Layout: layout}, nil
}

func (t *ControlHidraw) Close() error { return t.file.Close() }

func (t *ControlHidraw) Write(report []byte) error {
	frames, err := t.Layout.Encode(report)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		n, err := t.file.Write(frame)
		if err != nil {
			return err
		}
		if n != len(frame) {
			return fmt.Errorf("short management write: %d/%d", n, len(frame))
		}
	}
	return nil
}

func (t *ControlHidraw) Read(timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	assembler := controlAssembler{layout: t.Layout}
	buffer := make([]byte, 256)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, errors.New("hidraw read timeout")
		}
		fds := []unix.PollFd{{Fd: int32(t.file.Fd()), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, int((remaining+time.Millisecond-1)/time.Millisecond))
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, errors.New("hidraw read timeout")
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return nil, errors.New("management device disconnected")
		}
		n, err = unix.Read(int(t.file.Fd()), buffer)
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, errors.New("management input ended")
		}
		packet, err := assembler.push(buffer[:n])
		if err != nil {
			return nil, err
		}
		if packet != nil {
			return packet, nil
		}
	}
}
