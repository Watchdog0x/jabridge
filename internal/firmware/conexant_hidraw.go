package firmware

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type conexantHIDField struct {
	ID                  byte
	Bytes, Offset, Size int
}
type conexantHIDLayout struct {
	Input, Output conexantHIDField
	Plus          bool
}

func selectConexantLayout(reports []HIDReport) (conexantHIDLayout, error) {
	fields := func(kind string) []conexantHIDField {
		var result []conexantHIDField
		for _, report := range reports {
			if report.Kind != kind || report.ID == 0 || report.Bytes < 3 || report.Bytes > 65 {
				continue
			}
			var selected *HIDField
			valid := true
			for _, field := range report.Fields {
				if field.Flags&1 != 0 {
					continue
				} // Constant padding is zeroed.
				if selected != nil || field.collectionPage != 0x0c || field.UsagePage != 0x0c || len(field.Usages) != 1 || field.Usages[0] != 0 || field.SizeBits != 8 || field.OffsetBits%8 != 0 || field.Count == 0 {
					valid = false
					break
				}
				copyField := field
				selected = &copyField
			}
			if !valid || selected == nil || selected.OffsetBits/8+uint64(selected.Count)+1 > uint64(report.Bytes) {
				continue
			}
			result = append(result, conexantHIDField{report.ID, report.Bytes, 1 + int(selected.OffsetBits/8), int(selected.Count)})
		}
		return result
	}
	inputs, outputs := fields("input"), fields("output")
	for _, id := range []byte{4, 8} {
		var out []conexantHIDField
		for _, field := range outputs {
			if field.ID == id && field.Size >= 5 {
				out = append(out, field)
			}
		}
		if len(out) == 0 {
			continue
		}
		if len(out) != 1 {
			return conexantHIDLayout{}, errors.New("ambiguous UC Voice output reports")
		}
		var in []conexantHIDField
		for _, field := range inputs {
			if field.ID == id && field.Size >= 2 {
				in = append(in, field)
			}
		}
		if len(in) == 0 {
			for _, field := range inputs {
				if field.Size >= 2 {
					in = append(in, field)
				}
			}
		}
		if len(in) != 1 {
			return conexantHIDLayout{}, errors.New("ambiguous or missing UC Voice input report")
		}
		return conexantHIDLayout{Input: in[0], Output: out[0], Plus: id == 4}, nil
	}
	return conexantHIDLayout{}, errors.New("no supported UC Voice firmware reports on Consumer usage 0")
}

type conexantHID struct {
	raw           *sitelRawHID
	layout        conexantHIDLayout
	device        USBDevice
	descriptorSHA string
}

func (h *conexantHID) Write(ctx context.Context, packet []byte) error {
	if len(packet) == 0 || len(packet) > h.layout.Output.Size {
		return errors.New("UC Voice packet exceeds its HID field")
	}
	write := packet[0]&0x80 != 0
	if h.layout.Plus {
		if packet[0] != 0x20 && packet[0] != 0x60 {
			return errors.New("invalid UC Voice block command")
		}
		write = packet[0] == 0x60
	}
	if write {
		if err := requireHardwareWrites(); err != nil {
			return err
		}
	}
	if err := validateUSBDevice(h.device); err != nil {
		return err
	}
	frame := make([]byte, h.layout.Output.Bytes)
	frame[0] = h.layout.Output.ID
	copy(frame[h.layout.Output.Offset:], packet)
	return h.raw.Write(ctx, frame)
}

func (h *conexantHID) Read(ctx context.Context) ([]byte, error) {
	frame, err := h.raw.Read(ctx)
	if err != nil {
		return nil, err
	}
	field := h.layout.Input
	if len(frame) != field.Bytes || frame[0] != field.ID {
		return nil, errors.New("invalid UC Voice input frame")
	}
	return append([]byte(nil), frame[field.Offset:field.Offset+field.Size]...), nil
}

func (h *conexantHID) client() *conexantClient {
	size := 1
	if h.layout.Plus {
		size = min(h.layout.Input.Size, h.layout.Output.Size-4, 255)
	}
	return &conexantClient{io: h, plus: h.layout.Plus, maxData: size, wait: waitDFU}
}

func openConexantHID(device USBDevice) (*conexantHID, error) {
	if !conexantPID(device.ProductID) || device.VendorID != JabraVendorID || device.ViaDongle {
		return nil, errors.New("not a directly attached UC Voice firmware target")
	}
	if err := validateUSBDevice(device); err != nil {
		return nil, err
	}
	nodes, err := os.ReadDir("/sys/class/hidraw")
	if err != nil {
		return nil, err
	}
	var selected *conexantHID
	var failures []error
	for _, node := range nodes {
		parent, err := filepath.EvalSymlinks(filepath.Join("/sys/class/hidraw", node.Name(), "device"))
		if err != nil || !strings.HasPrefix(parent, device.attachment.realPath+string(filepath.Separator)) {
			continue
		}
		path := filepath.Join("/dev", node.Name())
		candidate, err := openConexantNode(path, device)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if selected != nil {
			_ = selected.raw.file.Close()
			_ = candidate.raw.file.Close()
			return nil, errors.New("multiple UC Voice firmware interfaces on the selected device")
		}
		selected = candidate
	}
	if selected == nil {
		return nil, errors.Join(append([]error{errors.New("UC Voice firmware HID interface is unavailable")}, failures...)...)
	}
	if err := validateUSBDevice(device); err != nil {
		_ = selected.raw.file.Close()
		return nil, err
	}
	return selected, nil
}

func openConexantNode(path string, device USBDevice) (result *conexantHID, resultErr error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() {
		if resultErr != nil {
			_ = file.Close()
		}
	}()
	if err := validateOpenedHID(file, device); err != nil {
		return nil, err
	}
	descriptor, err := readHidrawReportDescriptorFile(file)
	if err != nil {
		return nil, err
	}
	reports, err := parseHIDReports(descriptor)
	if err != nil {
		return nil, err
	}
	layout, err := selectConexantLayout(reports)
	if err != nil {
		return nil, err
	}
	raw := &sitelRawHID{file: file, in: sitelHIDLayout{ReportID: layout.Input.ID, ReportBytes: layout.Input.Bytes}, out: sitelHIDLayout{ReportID: layout.Output.ID, ReportBytes: layout.Output.Bytes}}
	return &conexantHID{raw: raw, layout: layout, device: device, descriptorSHA: fmt.Sprintf("%x", sha256.Sum256(descriptor))}, nil
}
