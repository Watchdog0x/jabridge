package firmware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type sitelRawHID struct {
	file    *os.File
	in, out sitelHIDLayout
}

func (h *sitelRawHID) wait(ctx context.Context, events int16) error {
	_, err := (csrContextHID{transport: &HidrawTransport{f: h.file}}).wait(ctx, events)
	return err
}
func (h *sitelRawHID) Write(ctx context.Context, raw []byte) error {
	if len(raw) != h.out.ReportBytes || raw[0] != h.out.ReportID {
		return managementNotSent(errors.New("invalid firmware HID output layout"))
	}
	attempted := false
	for {
		if err := h.wait(ctx, unix.POLLOUT); err != nil {
			if !attempted {
				return managementNotSent(err)
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			if !attempted {
				return managementNotSent(err)
			}
			return err
		}
		attempted = true
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
func (h *sitelRawHID) Read(ctx context.Context) ([]byte, error) {
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

type sitelManagementIO struct {
	raw       *sitelRawHID
	assembler controlAssembler
}

func (h *sitelManagementIO) Write(ctx context.Context, report []byte) error {
	if len(report) < 6 || report[0] != 5 {
		return managementNotSent(errors.New("invalid Sitel management report"))
	}
	length := int(report[4] & 63)
	kind := report[4] & 0xc0
	if length < 5 || length+1 > len(report) || length == 5 && (kind != 0x80 || report[5] != 7) {
		return managementNotSent(errors.New("invalid Sitel management length"))
	}
	if kind == 0 {
		subscription := length == 8 && report[5] == 13 && (report[6] == 1 || report[6] == 2) && report[7] == 5 && report[8] == 0
		fileData := length >= 9 && report[5] == 3 && report[6] == 0 && report[7]&0xc0 == 0x40 && int(report[7]&0x3f) == length-7
		if !subscription && !fileData {
			return managementNotSent(errors.New("unknown firmware subscription event"))
		}
	} else if kind != 0x40 && kind != 0x80 {
		return managementNotSent(errors.New("invalid Sitel request flags"))
	}
	packet := report[1 : length+1]
	written := false
	for len(packet) > 0 {
		frame := make([]byte, h.raw.out.ReportBytes)
		frame[0] = h.raw.out.ReportID
		count := copy(frame[1:], packet)
		if err := h.raw.Write(ctx, frame); err != nil {
			var notSent *managementNotSentError
			if written && errors.As(err, &notSent) {
				return fmt.Errorf("partial management request: %w", notSent.cause)
			}
			return err
		}
		written = true
		packet = packet[count:]
	}
	return nil
}
func (h *sitelManagementIO) Read(ctx context.Context) ([]byte, error) {
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

func openSitelRuntime(device USBDevice) (*sitelRuntime, func() error, error) {
	transport, err := openBoundManagement(device)
	if err != nil {
		return nil, nil, err
	}
	layout := transport.Layout
	raw := &sitelRawHID{file: transport.file, in: sitelHIDLayout{ReportID: layout.InputID, ReportBytes: layout.InputBytes, MaxMessage: 1024}, out: sitelHIDLayout{ReportID: layout.OutputID, ReportBytes: layout.OutputBytes, MaxMessage: 1024}}
	io := &sitelManagementIO{raw: raw, assembler: controlAssembler{layout: layout}}
	return &sitelRuntime{io: io}, transport.Close, nil
}

// Firmware usages FF54/FF55 can label the collection or its byte-array fields.
// Evolve2 40 puts FF54 fields inside a generic FF00 collection.
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
			firmwareUsage := page == 0xff54 || page == 0xff55
			if page == 0xff00 {
				firmwareUsage = (field.UsagePage == 0xff54 || field.UsagePage == 0xff55) &&
					field.Flags&1 == 0 && len(field.Usages) == 1 && field.Usages[0] == 1
			}
			if field.OffsetBits != bits || field.SizeBits != 8 || !firmwareUsage {
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

func openSitelBoot(device USBDevice) (*sitelRawHID, error) {
	profile, ok := sitelProfileForPID(device.ProductID)
	if !ok || device.ProductID != profile.BootPID || device.VendorID != JabraVendorID || device.ViaDongle {
		return nil, errors.New("not a supported Sitel bootloader")
	}
	return openSitelFirmwareHID(device)
}

func openSitelFirmwareHID(device USBDevice) (*sitelRawHID, error) {
	return openSitelFirmwareHIDAt(device, linuxHidrawPaths())
}

func openSitelFirmwareHIDAt(device USBDevice, paths hidrawPaths) (*sitelRawHID, error) {
	if err := validateUSBDevice(device); err != nil {
		return nil, hidAccessFailure("hid-usb-binding", err)
	}
	nodes, err := os.ReadDir(paths.class)
	if err != nil {
		return nil, hidAccessFailure("hid-scan", err)
	}
	var selected *sitelRawHID
	var scanErrors []error
	for _, node := range nodes {
		parent, err := filepath.EvalSymlinks(filepath.Join(paths.class, node.Name(), "device"))
		if err != nil || !strings.HasPrefix(parent, device.attachment.realPath+string(filepath.Separator)) {
			continue
		}
		path := filepath.Join(paths.dev, node.Name())
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			scanErrors = append(scanErrors, hidAccessFailure("hid-open", err))
			continue
		}
		file := os.NewFile(uintptr(fd), path)
		if err := validateOpenedHIDAt(file, device, paths.char); err != nil {
			scanErrors = append(scanErrors, hidAccessFailure("hid-identity", err))
			_ = file.Close()
			continue
		}
		descriptor, err := readHidrawReportDescriptorFile(file)
		if err != nil {
			scanErrors = append(scanErrors, hidAccessFailure("hid-descriptor", err))
			_ = file.Close()
			continue
		}
		reports, err := parseHIDReports(descriptor)
		if err != nil {
			scanErrors = append(scanErrors, hidAccessFailure("hid-parse", err))
			_ = file.Close()
			continue
		}
		in, out, err := selectSitelLayouts(reports)
		if err != nil {
			scanErrors = append(scanErrors, hidAccessFailure("hid-layout", err))
			_ = file.Close()
			continue
		}
		if selected != nil {
			_ = selected.file.Close()
			_ = file.Close()
			return nil, hidAccessFailure("hid-ambiguous", errors.New("multiple Sitel bootloader interfaces"))
		}
		selected = &sitelRawHID{file: file, in: in, out: out}
	}
	if selected == nil {
		if len(scanErrors) == 0 {
			return nil, hidAccessFailure("hid-no-match", errors.New("no HID interface belongs to this USB attachment"))
		}
		return nil, errors.Join(scanErrors...)
	}
	if err := validateUSBDevice(device); err != nil {
		_ = selected.file.Close()
		return nil, hidAccessFailure("hid-usb-binding", err)
	}
	return selected, nil
}
