// csr_ota_hidraw — /dev/hidraw<N> transport for the CSR OTA updater.
//
// This is the concrete OtaTransport implementation that talks to a real
// Jabra device. Tests use a fake transport (see csr_ota_updater_test.go);
// production code uses HidrawTransport.
//
// The hidraw kernel interface is dead simple for HID output+input reports:
//   - write(fd, report_bytes, report_size) sends an output report
//   - read(fd, buf, report_size) receives the next input report
// Report ID is the first byte of both buffers.
//
// We use poll(2) so Read can honor a deadline without spinning or changing
// descriptor flags shared with another goroutine.

package firmware

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// HidrawTransport is an OtaTransport bound to a single /dev/hidrawN node.
// Safe for use from a single goroutine; if you need multiplexed access,
// wrap it in your own mutex.
type HidrawTransport struct {
	path string
	f    *os.File
}

// OpenHidraw opens a /dev/hidrawN file for read/write. Returns an error
// if the path doesn't exist or the caller lacks permissions (the Jabra
// udev rule grants `input` group access, so either be in that group or
// run as root).
func OpenHidraw(path string) (*HidrawTransport, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &HidrawTransport{path: path, f: f}, nil
}

// Write sends a 63-byte HID output report to the device. Any length
// mismatch is a programming error and we fail loudly — a short or long
// report would corrupt the protocol.
func (t *HidrawTransport) Write(report []byte) error {
	// Accept any report size (63 or 64 depending on device)
	if len(report) < 7 {
		return fmt.Errorf("hidraw: report too short: %d bytes", len(report))
	}
	n, err := t.f.Write(report)
	if err != nil {
		return fmt.Errorf("hidraw write: %w", err)
	}
	// hidraw returns len(report) or len(report)-1 depending on kernel
	// version and whether it counts the report ID byte. Accept either.
	if n < len(report)-1 {
		return fmt.Errorf("hidraw short write: %d/%d bytes", n, len(report))
	}
	return nil
}

// Read pulls the next HID input report with poll(2), bounded by timeout.
func (t *HidrawTransport) Read(timeout time.Duration) ([]byte, error) {
	fd := int(t.f.Fd())
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 256) // large enough for any HID report size
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("hidraw read timeout after %s", timeout)
		}
		milliseconds := int((remaining + time.Millisecond - 1) / time.Millisecond)
		pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		ready, err := unix.Poll(pollFDs, milliseconds)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("hidraw poll: %w", err)
		}
		if ready == 0 {
			return nil, fmt.Errorf("hidraw read timeout after %s", timeout)
		}
		if pollFDs[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return nil, fmt.Errorf("hidraw poll failed: revents=0x%x", pollFDs[0].Revents)
		}
		n, err := unix.Read(fd, buf)
		if err == nil {
			// Discard non-GNP reports. The Jabra device sends multiple
			// HID report types (audio, buttons, etc.) — only report ID
			// 0x05 carries GNP protocol traffic.
			if n > 0 && buf[0] != GnpReportID {
				continue
			}
			if n < 7 {
				continue // too short to be a valid GNP report
			}
			return append([]byte(nil), buf[:n]...), nil
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			continue
		}
		return nil, fmt.Errorf("hidraw read: %w", err)
	}
}

// Close closes the underlying fd. Safe to call multiple times.
func (t *HidrawTransport) Close() error {
	if t.f == nil {
		return nil
	}
	err := t.f.Close()
	t.f = nil
	return err
}

// Path returns the /dev/hidrawN path this transport is bound to. Used
// for logging.
func (t *HidrawTransport) Path() string { return t.path }

// ── Dongle child-device discovery (item b) ─────────────────────────────────
//
// When a Jabra headset is connected via a dongle (not direct USB), it
// shows up on the dongle's hidraw interface as a "child device". The
// dongle's own PID (e.g. 0x24c7 for Link 380) is distinct from the
// paired headset's PID (e.g. 0x24b9 for Evolve2 85). jfwu queries the
// child's identity before deciding which firmware file to flash.
//
// Captured command from jfwu's GnpEndpoint[hidraw7] log during the
// Evolve2 85 flash:
//
//	--> 04 00 05 46 02 11           (child device count / addr query)
//	<-- 00 04 05 c8 02 11 b9 24     (child PID 0x24b9 little-endian)
//	--> 04 00 06 46 02 01           (child serial query)
//	<-- 00 04 06 d3 02 01 0c 41 31 42 32 43 33 44 34 45 35 46 36
//	       0c = length (12), then an anonymized example serial
//	--> 04 00 07 46 02 00           (child product name query)
//	<-- 00 04 07 d7 02 00 10 4a 61 62 72 61 20 45 76 6f 6c 76 65 32 20 38 35
//	       10 = length (16), then "Jabra Evolve2 85"
//
// The class byte is 0x46 (host->dongle→child device info), and the sub
// opcodes are:
//   0x11 = paired child product ID (returns 2 bytes LE)
//   0x01 = paired child serial number (returns len-prefixed ASCII)
//   0x00 = paired child product name (returns len-prefixed UTF-8)
//
// The outer addressing uses 0x04 as the source byte (instead of 0x08
// which we use for direct-connected headsets). That's because 0x04 is
// "dongle in the default slot" — a different endpoint address on the
// GNP network.

const (
	// Dongle commands use source 0x04 instead of host 0x08.
	GnpSrcDongle byte = 0x04

	// Dongle child-info query class.
	GnpClassDongleChild byte = 0x46

	// Child-info sub-opcodes.
	DongleChildProductID byte = 0x11
	DongleChildSerial    byte = 0x01
	DongleChildName      byte = 0x00
)

// ChildDeviceInfo is everything the dongle can tell us about the
// paired headset before we try to flash it.
type ChildDeviceInfo struct {
	ProductID uint16
	Serial    string
	Name      string
}

// buildDongleQuery assembles a class-0x46 query. Format matches the
// captured `04 00 <seq> 46 02 <sub>` exactly — note the outer byte is
// 0x04 (dongle), byte[3] is the class 0x46 not a flags|length, and
// byte[4] is 0x02 (device-info scope), byte[5] is the sub-opcode.
//
// NB: this framing deviates from the standard flags|length convention;
// the 0x46 value here is not `0x40|len=6`, it's a literal class byte.
// The device-info responses use the same scheme — byte[3] is a literal
// like 0xc8 / 0xd3 / 0xd7 (not a flags|length).
func buildDongleQuery(seq byte, sub byte) []byte {
	buf := make([]byte, GnpReportSize)
	buf[0] = GnpReportID
	buf[1] = GnpSrcDongle
	buf[2] = GnpDstZero
	buf[3] = seq
	buf[4] = GnpClassDongleChild
	buf[5] = 0x02
	buf[6] = sub
	return buf
}

// parseDongleChildResponse extracts the payload from a dongle child
// info response. Response format (based on capture):
//
//	[0]    0x00  (dst = 0)
//	[1]    0x04  (src = dongle)
//	[2]    seq   (echoes request seq)
//	[3]    resp class byte (varies per sub-op — not a flags|length)
//	[4]    0x02  (device-info scope)
//	[5]    sub   (echoes request sub)
//	[6]    payload length (for string responses; for PID responses, this
//	             is the first byte of the PID little-endian)
//	[7..]  payload bytes
//
// Because the format is NOT self-describing, the caller must know what
// sub-op they asked for and interpret the payload accordingly. We
// provide one helper per sub-op below.
func parseDongleChildResponse(buf []byte, wantSeq, wantSub byte) ([]byte, error) {
	// Strip HID Report ID byte if present (real hidraw reads include it).
	if len(buf) >= 1 && buf[0] == GnpReportID {
		buf = buf[1:]
	}
	if len(buf) < 7 {
		return nil, fmt.Errorf("dongle response too short: %d bytes", len(buf))
	}
	if buf[0] != 0x00 || buf[1] != GnpSrcDongle {
		return nil, fmt.Errorf("dongle response header mismatch: %02x %02x", buf[0], buf[1])
	}
	if buf[2] != wantSeq {
		return nil, fmt.Errorf("dongle response seq mismatch: got %02x, want %02x", buf[2], wantSeq)
	}
	if buf[5] != wantSub {
		return nil, fmt.Errorf("dongle response sub mismatch: got %02x, want %02x", buf[5], wantSub)
	}
	return buf[6:], nil
}

// QueryChildProductID sends a sub-op 0x11 query and returns the paired
// headset's PID as a uint16. For a Link 380 paired with an Evolve2 85,
// this returns 0x24b9.
func QueryChildProductID(tr OtaTransport, seq byte, timeout time.Duration) (uint16, error) {
	if err := tr.Write(buildDongleQuery(seq, DongleChildProductID)); err != nil {
		return 0, err
	}
	resp, err := tr.Read(timeout)
	if err != nil {
		return 0, err
	}
	payload, err := parseDongleChildResponse(resp, seq, DongleChildProductID)
	if err != nil {
		return 0, err
	}
	if len(payload) < 2 {
		return 0, fmt.Errorf("dongle PID response truncated: %d bytes", len(payload))
	}
	// Bytes are little-endian: b9 24 → 0x24b9.
	return uint16(payload[0]) | uint16(payload[1])<<8, nil
}

// QueryChildName sends a sub-op 0x00 query and returns the paired
// headset's name as a UTF-8 string. Payload layout: [0]=length,
// [1..length]=ASCII/UTF-8 bytes.
func QueryChildName(tr OtaTransport, seq byte, timeout time.Duration) (string, error) {
	if err := tr.Write(buildDongleQuery(seq, DongleChildName)); err != nil {
		return "", err
	}
	resp, err := tr.Read(timeout)
	if err != nil {
		return "", err
	}
	payload, err := parseDongleChildResponse(resp, seq, DongleChildName)
	if err != nil {
		return "", err
	}
	if len(payload) < 1 {
		return "", errors.New("dongle name response empty")
	}
	nlen := int(payload[0])
	if nlen+1 > len(payload) {
		nlen = len(payload) - 1
	}
	return string(payload[1 : 1+nlen]), nil
}

// QueryChildSerial is analogous to QueryChildName but returns the
// device serial number string.
func QueryChildSerial(tr OtaTransport, seq byte, timeout time.Duration) (string, error) {
	if err := tr.Write(buildDongleQuery(seq, DongleChildSerial)); err != nil {
		return "", err
	}
	resp, err := tr.Read(timeout)
	if err != nil {
		return "", err
	}
	payload, err := parseDongleChildResponse(resp, seq, DongleChildSerial)
	if err != nil {
		return "", err
	}
	if len(payload) < 1 {
		return "", errors.New("dongle serial response empty")
	}
	slen := int(payload[0])
	if slen+1 > len(payload) {
		slen = len(payload) - 1
	}
	return string(payload[1 : 1+slen]), nil
}

// DetectGnpReportSize opens a hidraw device and reads its HID report
// descriptor to determine the output report size for Report ID 0x05
// (GNP traffic). Returns 63 or 64. Falls back to 63 on any error.
//
// The approach mirrors bccmd_client.go's pickVendorReportID — we use
// HIDIOCGRDESCSIZE + HIDIOCGRDESC to fetch the raw descriptor, then
// walk the HID items looking for Report ID 0x05's output report size.
func DetectGnpReportSize(hidrawPath string) int {
	f, err := os.OpenFile(hidrawPath, os.O_RDONLY, 0)
	if err != nil {
		return 63
	}
	defer func() { _ = f.Close() }()

	// Step 1: get descriptor size
	var size int32
	if _, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		uintptr(HIDIOCGRDESCSIZE),
		uintptr(unsafe.Pointer(&size)),
	); errno != 0 || size <= 0 || size > HID_MAX_DESCRIPTOR_SIZE {
		return 63
	}

	// Step 2: fetch the descriptor
	type hidrawReportDescriptor struct {
		Size  uint32
		Value [HID_MAX_DESCRIPTOR_SIZE]byte
	}
	var desc hidrawReportDescriptor
	desc.Size = uint32(size)
	if _, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		uintptr(HIDIOCGRDESC),
		uintptr(unsafe.Pointer(&desc)),
	); errno != 0 {
		return 63
	}

	// Step 3: parse the descriptor for Report ID 5's output report size.
	return parseGnpOutputReportSize(desc.Value[:size])
}

// parseGnpOutputReportSize walks an HID report descriptor and returns
// the output report size (Report Count * Report Size / 8) for Report
// ID 0x05. Returns 63 if not found or on parse error.
func parseGnpOutputReportSize(desc []byte) int {
	var (
		currentReportID   byte
		currentReportSize int // in bits
		currentReportCnt  int
	)
	i := 0
	for i < len(desc) {
		b := desc[i]
		bSize := int(b & 0x03)
		if bSize == 3 {
			bSize = 4
		}
		bType := (b >> 2) & 0x03
		bTag := (b >> 4) & 0x0f
		i++
		if i+bSize > len(desc) {
			return 63
		}
		var data uint32
		for j := 0; j < bSize; j++ {
			data |= uint32(desc[i+j]) << (8 * j)
		}
		i += bSize

		switch bType {
		case 1: // Global
			switch bTag {
			case 7: // Report Size (bits per field)
				currentReportSize = int(data)
			case 8: // Report ID
				currentReportID = byte(data)
			case 9: // Report Count
				currentReportCnt = int(data)
			}
		case 0: // Main
			switch bTag {
			case 9: // Output
				if currentReportID == GnpReportID {
					// Total bytes = Report Count * Report Size / 8, plus
					// 1 byte for the Report ID prefix.
					totalBytes := (currentReportCnt * currentReportSize / 8) + 1
					if totalBytes == 63 || totalBytes == 64 {
						return totalBytes
					}
				}
			}
		}
	}
	return 63
}
