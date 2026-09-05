// bccmd_client — prototype BCCMD-over-HID client for Jabra/CSR devices.
//
// This prototype speaks the CSR BCCMD protocol in pure Go over /dev/hidraw.
//
// STATUS: READ-ONLY. Only implements PS_READ (opcode 0x7002) because that's
// the non-destructive verification path. Writes (0x7003) and firmware upload
// use the same wire format but are NOT wired up until the read path is
// validated against a real device.
//
// EXPERIMENTAL PACKET LAYOUT:
//
//   Top layer (from BcCmdWritePSKey @ 0x18be7f):
//     BCCMD message buffer:
//       [0x00..0x09]   filled by transport layer (seq/length header)
//       [0x0A..0x0B]   PS key ID       (uint16, LE)
//       [0x0C..0x0D]   payload length  (uint16, LE, in 16-bit words)
//       [0x0E..0x0F]   store ID        (uint16, LE)
//       [0x10..]       payload         (length*2 bytes)
//
//   BCCMD opcodes (standard CSR, same as bluez bccmd tool):
//     0x7002   PS_READ
//     0x7003   PS_WRITE
//     0x7006   COLD_RESET
//     0x7007   WARM_RESET
//
//   HID output report wrapping (from pttrans_block_write @ 0x18c827):
//     [0]     HID Report ID (vendor-specific, from HID descriptor at runtime)
//     [1..2]  0x00 0x00
//     [3]     0x80 (flags/direction = request)
//     [4]     0x0f
//     [5]     0x0c
//     [6..7]  sequence/id (LE u16)
//     [8..9]  length in bytes (LE u16)
//     [10..]  BCCMD message from the top layer
//
// UNKNOWNS (would need more RE to resolve):
//   - Exact algorithm that picks the outgoing HID Report ID from the HID
//     descriptor. jfwu reads the descriptor, identifies a vendor-page usage
//     (0xFFxx), and picks its Report ID. I make a best-guess fallback below
//     but the canonical fix is to parse HID descriptors via HIDIOCGRDESC.
//   - The sequence number field at offset 6. Currently guess a monotonic
//     counter; jfwu's pattern suggests that's correct but not confirmed.
//   - Response parsing: the read response format is not yet decoded.

package firmware

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// BCCMD variable IDs (varid). In CSR BCCMD the 16-bit "opcode" is a varid —
// it names a variable. Read/write is distinguished by msg_type, not by varid.
// Confirmed from disassembly:
//
//	BcCmdReadPSKey @ 0x18bd80 passes ecx=0x7003 (varid) and edi=0 (GetReq)
//	BcCmdWritePSKey @ 0x18be7f passes ecx=0x7003 (varid) and edi=2 (SetReq)
//
// so the PS variable is 0x7003 regardless of direction.
const (
	BCCMD_VARID_PS           uint16 = 0x7003
	BCCMD_VARID_COLD_RESET   uint16 = 0x7006
	BCCMD_VARID_WARM_RESET   uint16 = 0x7007
	BCCMD_VARID_CHIP_VERSION uint16 = 0x7010
)

// BCCMD message types — confirmed from BcCmdReadPSKey + BcCmdWritePSKey disassembly.
// These are the standard CSR BCCMD transaction types.
const (
	BCCMD_TYPE_GET_REQ  uint16 = 0 // read request        (BcCmdReadPSKey passes edi=0)
	BCCMD_TYPE_GET_RESP uint16 = 1 // read response
	BCCMD_TYPE_SET_REQ  uint16 = 2 // write request       (BcCmdWritePSKey passes edi=2)
	BCCMD_TYPE_SET_RESP uint16 = 3 // write response
	BCCMD_TYPE_EVENT    uint16 = 4 // async event
)

// Default store ID used for PS key reads/writes. Standard value for
// BlueCore BC01..BC07 chips.
const BCCMD_STORE_DEFAULT uint16 = 0

// HIDIOCGRDESCSIZE / HIDIOCGRDESC ioctl numbers for Linux hidraw.
// /usr/include/linux/hidraw.h:
//
//	#define HIDIOCGRDESCSIZE  _IOR('H', 0x01, int)
//	#define HIDIOCGRDESC      _IOR('H', 0x02, struct hidraw_report_descriptor)
//
// Where _IOR('H', n, t) = (2 << 30) | (sizeof(t) << 16) | ('H' << 8) | n.
const (
	HID_MAX_DESCRIPTOR_SIZE = 4096
	HIDIOCGRDESCSIZE        = 0x80044801
	HIDIOCGRDESC            = 0x90044802
)

// BCCMDClient wraps a /dev/hidraw file descriptor and speaks BCCMD to the
// device on the other end.
type BCCMDClient struct {
	fd       *os.File
	reportID byte // output report ID (picked from HID descriptor)
	seq      atomic.Uint32
}

// OpenBCCMD opens a hidraw device and prepares it for BCCMD traffic.
// The caller must have read/write access to the hidraw node (usually root
// or a user in a group matched by a udev rule).
func OpenBCCMD(hidrawPath string) (*BCCMDClient, error) {
	f, err := os.OpenFile(hidrawPath, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", hidrawPath, err)
	}

	c := &BCCMDClient{fd: f}

	// Query HID descriptor to find the vendor-specific output report ID.
	// jfwu does the same thing, and the Report ID is not fixed across
	// Jabra products (varies by chip).
	rptID, err := c.pickVendorReportID()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("pick report id: %w", err)
	}
	c.reportID = rptID
	return c, nil
}

// Close closes the underlying file descriptor.
func (c *BCCMDClient) Close() error { return c.fd.Close() }

// ReportID returns the chosen HID output report ID.
func (c *BCCMDClient) ReportID() byte { return c.reportID }

// pickVendorReportID fetches the HID report descriptor via ioctl and scans
// for vendor-page usages. The report ID that appears on a top-level
// vendor-defined collection is our target.
//
// This is a minimal descriptor parser — it handles enough of the HID spec
// to find Report ID items inside vendor collections. Full HID parsers are
// much larger; we don't need all of that here.
func (c *BCCMDClient) pickVendorReportID() (byte, error) {
	// Step 1: get descriptor size via HIDIOCGRDESCSIZE
	var size int32
	if _, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		c.fd.Fd(),
		uintptr(HIDIOCGRDESCSIZE),
		uintptr(unsafe.Pointer(&size)),
	); errno != 0 {
		return 0, fmt.Errorf("HIDIOCGRDESCSIZE: %v", errno)
	}
	if size <= 0 || size > HID_MAX_DESCRIPTOR_SIZE {
		return 0, fmt.Errorf("invalid descriptor size: %d", size)
	}

	// Step 2: fetch the descriptor itself
	// struct hidraw_report_descriptor { __u32 size; __u8 value[HID_MAX_DESCRIPTOR_SIZE]; }
	type hidrawReportDescriptor struct {
		Size  uint32
		Value [HID_MAX_DESCRIPTOR_SIZE]byte
	}
	var desc hidrawReportDescriptor
	desc.Size = uint32(size)
	if _, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		c.fd.Fd(),
		uintptr(HIDIOCGRDESC),
		uintptr(unsafe.Pointer(&desc)),
	); errno != 0 {
		return 0, fmt.Errorf("HIDIOCGRDESC: %v", errno)
	}

	// Step 3: walk the descriptor looking for Report ID (0x85) inside a
	// vendor-defined usage page (0x06 0xXX 0xFF).
	rptID, err := findVendorReportID(desc.Value[:size])
	if err != nil {
		return 0, err
	}
	if rptID == 0 {
		return 0, errors.New("no vendor-defined Report ID found in HID descriptor")
	}
	return rptID, nil
}

// findVendorReportID walks an HID report descriptor byte stream and returns
// the Report ID that appears inside a vendor-defined usage page (FF00–FFFF).
// Handles short items per the HID 1.11 spec §6.2.2.
func findVendorReportID(desc []byte) (byte, error) {
	var (
		currentUsagePage uint16
		currentReportID  byte
		inVendorPage     bool
		lastSeenID       byte
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
			return 0, fmt.Errorf("truncated HID descriptor at offset %d", i-1)
		}
		var data uint32
		for j := 0; j < bSize; j++ {
			data |= uint32(desc[i+j]) << (8 * j)
		}
		i += bSize

		// Global items (bType==1): Usage Page=0, Report ID=8
		if bType == 1 {
			switch bTag {
			case 0: // Usage Page
				currentUsagePage = uint16(data)
				inVendorPage = currentUsagePage >= 0xFF00
			case 8: // Report ID
				currentReportID = byte(data)
				if inVendorPage && currentReportID != 0 {
					// Found it — a Report ID declared while inside vendor page.
					return currentReportID, nil
				}
				lastSeenID = currentReportID
			}
		}
	}
	// Fallback: if we saw a Report ID but never a vendor page, return it anyway.
	return lastSeenID, nil
}

// buildBCCMDPacket constructs a complete HID output report for a BCCMD
// message. Uses the STANDARD CSR BCCMD wire format (10-byte header +
// varid-specific payload), which is what the device actually parses.
//
// jfwu's BcCmdWritePSKey allocates 18 bytes for PS_WRITE because that
// specific command embeds its parameters at offsets 0xA/0xC/0xE before
// the data. For PS_READ (and most other BCCMD ops), the payload starts
// right after the 10-byte header at offset 0xA.
func (c *BCCMDClient) buildBCCMDPacket(msgType, opcode uint16, payload []byte) []byte {
	payloadWords := uint16(len(payload) / 2)
	if len(payload)%2 != 0 {
		// Pad odd-length payloads to word boundary — BCCMD is 16-bit addressed.
		payload = append(payload, 0x00)
		payloadWords++
	}

	// Standard CSR BCCMD header (10 bytes = 5 words):
	//   [0..1]  type  (u16)
	//   [2..3]  length (u16, in words, including header)
	//   [4..5]  seq no (u16)
	//   [6..7]  varid (u16)  (== opcode for PS ops)
	//   [8..9]  status (u16)
	//   [10..]  payload
	const stdHeaderBytes = 10
	bccmdMsg := make([]byte, stdHeaderBytes+len(payload))
	copy(bccmdMsg[stdHeaderBytes:], payload)

	seq := uint16(c.seq.Add(1))
	totalWords := uint16(stdHeaderBytes/2) + payloadWords      // 5 + payloadWords
	binary.LittleEndian.PutUint16(bccmdMsg[0x00:], msgType)    // type
	binary.LittleEndian.PutUint16(bccmdMsg[0x02:], totalWords) // length in words
	binary.LittleEndian.PutUint16(bccmdMsg[0x04:], seq)        // seq
	binary.LittleEndian.PutUint16(bccmdMsg[0x06:], opcode)     // varid (PS_READ/PS_WRITE/etc)
	binary.LittleEndian.PutUint16(bccmdMsg[0x08:], 0)          // status (0 in request)

	// HID output report wrapper (10 bytes + BCCMD message)
	// Layout from pttrans_block_write @ 0x18c827:
	//   [0]    Report ID
	//   [1..2] 0x00 0x00
	//   [3]    0x80
	//   [4]    0x0f
	//   [5]    0x0c
	//   [6..7] seq/id (LE)
	//   [8..9] length in bytes (LE)
	//   [10..] payload
	report := make([]byte, 10+len(bccmdMsg))
	report[0] = c.reportID
	report[1] = 0x00
	report[2] = 0x00
	report[3] = 0x80
	report[4] = 0x0f
	report[5] = 0x0c
	binary.LittleEndian.PutUint16(report[6:], seq)
	binary.LittleEndian.PutUint16(report[8:], uint16(len(bccmdMsg)))
	copy(report[10:], bccmdMsg)
	return report
}

// readWithTimeout performs a poll-based read on the hidraw fd with a deadline.
// Returns the bytes read, or an error on timeout / I/O failure.
func (c *BCCMDClient) readWithTimeout(timeout time.Duration) ([]byte, error) {
	fd := int(c.fd.Fd())
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 128)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("read timeout after %s — device did not respond to BCCMD request", timeout)
		}
		milliseconds := int((remaining + time.Millisecond - 1) / time.Millisecond)
		pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		ready, err := unix.Poll(pollFDs, milliseconds)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("hid poll: %w", err)
		}
		if ready == 0 {
			return nil, fmt.Errorf("read timeout after %s — device did not respond to BCCMD request", timeout)
		}
		if pollFDs[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return nil, fmt.Errorf("hid poll failed: revents=0x%x", pollFDs[0].Revents)
		}
		n, err := unix.Read(fd, buf)
		if err == nil {
			return buf[:n], nil
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			continue
		}
		return nil, fmt.Errorf("hid read: %w", err)
	}
}

// PSRead sends a BCCMD Get request for a PS key (varid 0x7003, msg_type 0).
// Non-destructive — reads configuration data from the device.
//
// Matches BcCmdReadPSKey @ 0x18bd80:
//   - allocates maxWords*2 + 18 bytes
//   - writes pskeyID at offset 0xA, maxWords at 0xC, storeID at 0xE
//   - calls BcCmdRequest(type=0, length_words=maxWords+8, flags=0,
//     varid=0x7003, buffer)
func (c *BCCMDClient) PSRead(pskeyID uint16, maxWords uint16) ([]byte, error) {
	// PS_READ request payload follows jfwu's buffer layout exactly:
	//   [0..1]  pskey_id      (jfwu offset 0xA, our payload offset 0)
	//   [2..3]  maxWords      (jfwu offset 0xC, our payload offset 2)
	//   [4..5]  storeID       (jfwu offset 0xE, our payload offset 4)
	//   [6..]   maxWords*2 bytes of zeros — response fill space
	//
	// jfwu's BcCmdRequest gets length_words = maxWords + 8, meaning the
	// wire carries (8 + maxWords) words = 16 + maxWords*2 bytes. The 16
	// covers the 10-byte standard BCCMD header + the 6-byte varid params.
	// The maxWords*2 extra bytes are the reserved response area — the chip
	// fills them in on its reply. My payload (6 params + maxWords*2) is
	// what comes after buildBCCMDPacket's 10-byte std header.
	payload := make([]byte, 6+int(maxWords)*2)
	binary.LittleEndian.PutUint16(payload[0:], pskeyID)
	binary.LittleEndian.PutUint16(payload[2:], maxWords)
	binary.LittleEndian.PutUint16(payload[4:], BCCMD_STORE_DEFAULT)
	// payload[6..] left zero — matches the memset'd tail of jfwu's buffer

	packet := c.buildBCCMDPacket(BCCMD_TYPE_GET_REQ, BCCMD_VARID_PS, payload)
	fmt.Printf("[bccmd] TX %d bytes: % x\n", len(packet), packet)

	if _, err := c.fd.Write(packet); err != nil {
		return nil, fmt.Errorf("hid write: %w", err)
	}

	// Read the response with a 2-second deadline so bad wire format doesn't
	// hang the test forever. jfwu has a background reader thread
	// (`HidIoInputReport` @ 0x187065) that posts responses to queued waiters;
	// our prototype uses a simple synchronous poll.
	resp, err := c.readWithTimeout(2 * time.Second)
	if err != nil {
		return nil, err
	}
	fmt.Printf("[bccmd] RX %d bytes: % x\n", len(resp), resp)
	if len(resp) < 10 {
		return nil, fmt.Errorf("response too short: %d bytes", len(resp))
	}
	return resp, nil
}
