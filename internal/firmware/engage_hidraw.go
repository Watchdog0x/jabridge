package firmware

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type engageRawHID struct {
	file    *os.File
	in, out sitelHIDLayout
}

func (h *engageRawHID) wait(ctx context.Context, events int16) error {
	_, err := (csrContextHID{transport: &HidrawTransport{f: h.file}}).wait(ctx, events)
	return err
}
func (h *engageRawHID) Write(ctx context.Context, raw []byte) error {
	if len(raw) != h.out.ReportBytes || raw[0] != h.out.ReportID {
		return errors.New("invalid firmware HID output layout")
	}
	for {
		if err := h.wait(ctx, unix.POLLOUT); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := unix.Write(int(h.file.Fd()), raw)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n != len(raw) {
			return errors.New("short firmware HID write")
		}
		return nil
	}
}
func (h *engageRawHID) Read(ctx context.Context) ([]byte, error) {
	buffer := make([]byte, 256)
	for {
		if err := h.wait(ctx, unix.POLLIN); err != nil {
			return nil, err
		}
		n, err := unix.Read(int(h.file.Fd()), buffer)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, unix.ENODEV
		}
		if h.in.ReportID == 0 {
			if n+1 != h.in.ReportBytes {
				return nil, errors.New("invalid unnumbered firmware HID input")
			}
			return append([]byte{0}, buffer[:n]...), nil
		}
		if buffer[0] != h.in.ReportID {
			continue
		}
		if n != h.in.ReportBytes {
			return nil, errors.New("invalid firmware HID input size")
		}
		return append([]byte(nil), buffer[:n]...), nil
	}
}

type engageManagementIO struct {
	raw       *engageRawHID
	assembler controlAssembler
}

func (h *engageManagementIO) Write(ctx context.Context, report []byte) error {
	if len(report) < 6 || report[0] != 5 {
		return errors.New("invalid Engage management report")
	}
	length := int(report[4] & 63)
	kind := report[4] & 0xc0
	if length < 5 || length+1 > len(report) || length == 5 && (kind != 0x80 || report[5] != 7) {
		return errors.New("invalid Engage management length")
	}
	if kind == 0 {
		if length != 8 || report[5] != 13 || (report[6] != 1 && report[6] != 2) || report[7] != 5 || report[8] != 0 {
			return errors.New("unknown firmware subscription event")
		}
	} else if kind != 0x40 && kind != 0x80 {
		return errors.New("invalid Engage request flags")
	}
	packet := report[1 : length+1]
	for len(packet) > 0 {
		frame := make([]byte, h.raw.out.ReportBytes)
		frame[0] = h.raw.out.ReportID
		count := copy(frame[1:], packet)
		if err := h.raw.Write(ctx, frame); err != nil {
			return err
		}
		packet = packet[count:]
	}
	return nil
}
func (h *engageManagementIO) Read(ctx context.Context) ([]byte, error) {
	for {
		raw, err := h.raw.Read(ctx)
		if err != nil {
			h.assembler.reset()
			return nil, err
		}
		packet, err := h.assembler.push(raw)
		if err != nil {
			return nil, err
		}
		if packet != nil {
			return packet, nil
		}
	}
}

func openEngageRuntime(device USBDevice) (*engageRuntime, func() error, error) {
	transport, err := openBoundManagement(device)
	if err != nil {
		return nil, nil, err
	}
	layout := transport.Layout
	raw := &engageRawHID{file: transport.file, in: sitelHIDLayout{ReportID: layout.InputID, ReportBytes: layout.InputBytes, MaxMessage: 1024}, out: sitelHIDLayout{ReportID: layout.OutputID, ReportBytes: layout.OutputBytes, MaxMessage: 1024}}
	io := &engageManagementIO{raw: raw, assembler: controlAssembler{layout: layout}}
	return &engageRuntime{io: io}, transport.Close, nil
}

// The Sitel updater selects FF54/FF55, not the FF00 runtime GNP interface.
// Report IDs and lengths are descriptor-derived, including unnumbered reports.
func selectSitelLayouts(reports []HIDReport) (sitelHIDLayout, sitelHIDLayout, error) {
	var in, out sitelHIDLayout
	for _, report := range reports {
		if report.Kind != "input" && report.Kind != "output" || len(report.Fields) == 0 {
			continue
		}
		bits := uint64(0)
		valid := true
		for _, field := range report.Fields {
			page := field.collectionPage
			if page == 0 {
				page = field.UsagePage
			}
			if field.OffsetBits != bits || field.SizeBits != 8 || (page != 0xff54 && page != 0xff55) {
				valid = false
				break
			}
			bits += uint64(field.Count) * 8
		}
		count := int(bits/8) + 1
		if !valid || count < 8 || count > 65 || (report.ID != 0 && count != report.Bytes) || (report.ID == 0 && count != report.Bytes+1) {
			continue
		}
		layout := sitelHIDLayout{ReportID: report.ID, ReportBytes: count, MaxMessage: 1024}
		if report.Kind == "input" {
			if in.ReportBytes != 0 {
				return in, out, errors.New("ambiguous Sitel input reports")
			}
			in = layout
		} else {
			if out.ReportBytes != 0 {
				return in, out, errors.New("ambiguous Sitel output reports")
			}
			out = layout
		}
	}
	if in.ReportBytes == 0 || out.ReportBytes == 0 {
		return in, out, errors.New("no matching Sitel bootloader HID reports")
	}
	return in, out, nil
}

func openEngageBoot(device USBDevice) (*engageRawHID, error) {
	if device.ProductID != 0x4050 {
		return nil, errors.New("not an Engage bootloader PID")
	}
	if err := validateUSBDevice(device); err != nil {
		return nil, err
	}
	nodes, err := os.ReadDir("/sys/class/hidraw")
	if err != nil {
		return nil, err
	}
	var selected *engageRawHID
	var scanErrors []error
	for _, node := range nodes {
		parent, err := filepath.EvalSymlinks(filepath.Join("/sys/class/hidraw", node.Name(), "device"))
		if err != nil || !strings.HasPrefix(parent, device.attachment.realPath+string(filepath.Separator)) {
			continue
		}
		path := filepath.Join("/dev", node.Name())
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			scanErrors = append(scanErrors, err)
			continue
		}
		file := os.NewFile(uintptr(fd), path)
		if err := validateOpenedHID(file, device); err != nil {
			scanErrors = append(scanErrors, err)
			_ = file.Close()
			continue
		}
		descriptor, err := readHidrawReportDescriptorFile(file)
		if err != nil {
			scanErrors = append(scanErrors, err)
			_ = file.Close()
			continue
		}
		reports, err := parseHIDReports(descriptor)
		if err != nil {
			scanErrors = append(scanErrors, err)
			_ = file.Close()
			continue
		}
		in, out, err := selectSitelLayouts(reports)
		if err != nil {
			scanErrors = append(scanErrors, err)
			_ = file.Close()
			continue
		}
		if selected != nil {
			_ = selected.file.Close()
			_ = file.Close()
			return nil, errors.New("multiple Engage bootloader interfaces")
		}
		selected = &engageRawHID{file: file, in: in, out: out}
	}
	if selected == nil {
		return nil, errors.Join(append([]error{errors.New("engage bootloader HID interface is unavailable")}, scanErrors...)...)
	}
	if err := validateUSBDevice(device); err != nil {
		_ = selected.file.Close()
		return nil, err
	}
	return selected, nil
}
