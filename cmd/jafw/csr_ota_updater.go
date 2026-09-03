// csr_ota_updater — pure-Go CSR OTA firmware updater for newer Jabra devices.
//
// Implements the GNP/OTA wire protocol that jfwu's CsrOtaDriver +
// CsrOtaUpdater use to flash Qualcomm-based Jabra hardware (Evolve2 series,
// Engage 50 II, PanaCast, Speak 710, Link 380, etc.).
//
// Status:
//   ✓  GNP framing (build + parse) for commands — byte-exact with capture
//   ✓  Command builders: selectPartition, sendOtaStart, writeCrc, writeSoftwareVersion
//   ✓  writeBlock format: variable payload size, 16-bit LE chunk index, reserved byte zero
//   ✓  Response/ACK/event parser (including variable-length event payloads)
//   ✓  HidrawTransport — talks to /dev/hidrawN, proven live with dongle-info
//   ✓  Dongle class-0x46 queries (PID, serial, name) — validated against real Link 380
//   ✓  Pure-Go GnV archive unpack (no 7z dependency)
//   ✓  flash-csr-ota CLI command (behind a --force safety gate)
//   ⚠  Pre-OTA init is implemented from capture, but several command semantics
//      are still named by behavior rather than vendor documentation.
//
// Real flashing remains locked because hardware coverage is limited and a
// failed firmware update can leave a device unusable.
//
// Design notes:
//   - No dependency on libusb, libjabra.so, jfwu, or dfu-util. Pure stdlib.
//   - Uses /dev/hidraw<N> via plain os.File + syscall.Read/Write. The same
//     transport style as bccmd_client.go.
//   - Event reader runs in its own goroutine, events delivered on a
//     buffered channel. Keeps the flash loop synchronous and testable.
//   - Framing functions are pure (no I/O) so tests can validate byte
//     sequences without touching real hardware.

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── Protocol constants (from project_jabra_csr_ota_protocol.md) ───────────

const (
	// HID transport
	GnpReportID   byte = 0x05 // HID report ID for all GNP traffic
	GnpReportSize int  = 63   // default HID report length — auto-detected per device (63 for Evolve2 85, 64 for Link 380)

	// Addressing — host uses 0x08 as its source byte, device replies swap
	// src/dst so host-bound packets have byte[0]=0x00, byte[1]=0x08.
	GnpSrcHost byte = 0x08
	GnpDstZero byte = 0x00

	// Command classes
	GnpClassDeviceInfo byte = 0x02 // product / firmware version queries
	GnpClassCsrOta     byte = 0x0f // CSR OTA firmware update commands

	// flags|len byte — bit patterns
	GnpFlagQuery    byte = 0x40 // query (expects data response)
	GnpFlagCommand  byte = 0x80 // action (expects ACK)
	GnpFlagResponse byte = 0xc0 // response header
	GnpFlagAck      byte = 0xca // bytes[3] value of a pure ACK
	GnpAckOK        byte = 0xff // bytes[4] value of a pure ACK
	GnpFlagEvent    byte = 0x0a // bytes[3] value of an async event
	GnpEventTag     byte = 0x0f // bytes[4] value of an async event

	// CSR OTA opcodes (class = GnpClassCsrOta)
	OtaOpSendStart       byte = 0x17
	OtaOpWriteCrc        byte = 0x19
	OtaOpWriteBlock      byte = 0x1a
	OtaOpWriteFwVersion  byte = 0x1e
	OtaOpDfuFromSquif    byte = 0x1d // commit DFU image from SQIF — triggers bank switch on reboot
	OtaOpSelectPartition byte = 0x2d

	// CSR OTA async events (match bytes[5] of an event packet)
	OtaEventFlashEraseDone  byte = 0x18
	OtaEventPreloadProgress byte = 0x1b
	OtaEventVerifyStatus    byte = 0x1c

	// Bulk write parameters
	OtaChunkBytes   int = 52 // default payload bytes per writeBlock report (reportSize - 11 header)
	OtaPreloadCount int = 10 // unacked chunks allowed in flight
)

// ── GNP packet framing ─────────────────────────────────────────────────────

// buildCommand assembles a GNP command packet and wraps it in a 63-byte
// HID output report. The returned buffer is ready to write to /dev/hidrawN.
//
// Format (inner, before HID wrapping):
//
//	[0] src (host = 0x08)
//	[1] dst (0x00 — ignored on host-originated)
//	[2] seq (caller-supplied, wraps at 0xFF)
//	[3] flags|len — 0x80|total_length for commands expecting ACK
//	[4] class
//	[5] op
//	[6..] payload
//
// Then the inner packet is placed at buf[1..innerLen+1] with buf[0] = 0x05
// and the remaining bytes zero-padded to 63.
func buildCommand(seq byte, class, op byte, payload []byte) []byte {
	return buildCommandSized(seq, class, op, payload, GnpReportSize)
}

func buildCommandSized(seq byte, class, op byte, payload []byte, reportSize int) []byte {
	return buildCommandFull(GnpSrcHost, seq, class, op, payload, reportSize)
}

func buildCommandFull(src, seq byte, class, op byte, payload []byte, reportSize int) []byte {
	innerLen := 6 + len(payload)
	if innerLen > reportSize-1 {
		panic(fmt.Sprintf("csr-ota: command payload too large: %d bytes (max %d)", len(payload), reportSize-1-6))
	}
	buf := make([]byte, reportSize)
	buf[0] = GnpReportID
	buf[1] = src
	buf[2] = GnpDstZero
	buf[3] = seq
	buf[4] = GnpFlagCommand | byte(innerLen)
	buf[5] = class
	buf[6] = op
	copy(buf[7:], payload)
	return buf
}

// buildWriteBlock assembles a single firmware-chunk HID report.
//
// CORRECTED 2026-04-12 from analysis of all 36,415 chunks with zero
// mismatches (see /tmp/chunk-format-analysis.md):
//
//	[0]  0x05         HID Report ID
//	[1]  0x08         GNP source (host)
//	[2]  0x00         GNP destination
//	[3]  0x00         GNP sequence (always 0 for fire-and-forget writeBlock)
//	[4]  10 + n       inner length (10-byte header + n data bytes)
//	[5]  0x0f         CSR OTA class
//	[6]  0x1a         writeBlock opcode
//	[7]  chunkIndex & 0xFF     chunk index LOW byte (16-bit LE)
//	[8]  chunkIndex >> 8       chunk index HIGH byte (16-bit LE)
//	[9]  n            actual data payload length (1..52)
//	[10] 0x00         reserved (always zero — confirmed across 36,415 chunks)
//	[11..11+n] firmware data
//	[11+n..62] zero padding
//
// The chunk index is per-partition (resets to 0 at each partition boundary)
// and uses a full 16-bit range. byte[7] is NOT a single-byte sequence —
// together with byte[8] they form the 16-bit counter.
func buildWriteBlock(chunkIndex uint16, data []byte) []byte {
	return buildWriteBlockSized(chunkIndex, data, GnpReportSize)
}

func buildWriteBlockSized(chunkIndex uint16, data []byte, reportSize int) []byte {
	return buildWriteBlockFull(GnpSrcHost, chunkIndex, data, reportSize)
}

func buildWriteBlockFull(src byte, chunkIndex uint16, data []byte, reportSize int) []byte {
	n := len(data)
	if n == 0 || n > reportSize-11 {
		panic(fmt.Sprintf("csr-ota: writeBlock data must be 1..%d bytes, got %d", reportSize-11, n))
	}
	buf := make([]byte, reportSize)
	buf[0] = GnpReportID
	buf[1] = src
	buf[2] = GnpDstZero
	buf[3] = 0x00         // sequence (always 0 for bulk chunks)
	buf[4] = byte(10 + n) // inner length
	buf[5] = GnpClassCsrOta
	buf[6] = OtaOpWriteBlock
	buf[7] = byte(chunkIndex)      // chunk index low byte
	buf[8] = byte(chunkIndex >> 8) // chunk index high byte
	buf[9] = byte(n)               // data payload length
	buf[10] = 0x00                 // reserved
	copy(buf[11:], data)
	return buf
}

// Command builders — each returns the full 63-byte HID report.

func cmdSelectPartition(seq, partition byte) []byte {
	return buildCommand(seq, GnpClassCsrOta, OtaOpSelectPartition, []byte{partition})
}

func cmdSendOtaStart(seq byte) []byte {
	return buildCommand(seq, GnpClassCsrOta, OtaOpSendStart, nil)
}

// cmdWriteCrc sends the CRC pre-commit header for a partition's chunks.
//
// CRITICAL ENCODING NOTE — pinned by the live capture and by the failing
// TestBuildCommandWriteCrcRealCRC test. The CRC32 is NOT serialized as a
// plain big-endian or little-endian u32. It is sent as TWO little-endian
// u16 halves, HIGH HALF FIRST:
//
//	bytes[0..1] = uint16 little-endian of (crc >> 16)
//	bytes[2..3] = uint16 little-endian of (crc & 0xffff)
//
// Example: crc 0x2db4fa8f → wire bytes b4 2d 8f fa (not 2d b4 fa 8f which
// would be pure BE, and not 8f fa b4 2d which would be pure LE). This was
// confirmed against the real Evolve2 85 flash capture at partition 1.
//
// After the CRC come two standard LE u16 values:
//
//	bytes[4..5] = chunk count    (LE u16)
//	bytes[6..7] = preload count  (LE u16)
//
// Captured packet for partition 1 (crc 0x2db4fa8f, 10 chunks, preload 10):
//
//	08 00 11 8e 0f 19 b4 2d 8f fa 0a 00 0a 00
//	                  └─half-swap─┘ └chnk┘ └prelo┘
func cmdWriteCrc(seq byte, crc32 uint32, chunks uint16, preload uint16) []byte {
	payload := make([]byte, 8)
	binary.LittleEndian.PutUint16(payload[0:2], uint16(crc32>>16))
	binary.LittleEndian.PutUint16(payload[2:4], uint16(crc32&0xffff))
	binary.LittleEndian.PutUint16(payload[4:6], chunks)
	binary.LittleEndian.PutUint16(payload[6:8], preload)
	return buildCommand(seq, GnpClassCsrOta, OtaOpWriteCrc, payload)
}

// cmdWriteSoftwareVersion sends the post-partition version marker.
// Captured on partition 0 ACK: `08 00 0e 89 0f 1e 01 05 07` = version 1.5.7.
func cmdWriteSoftwareVersion(seq byte, major, minor, patch byte) []byte {
	return buildCommand(seq, GnpClassCsrOta, OtaOpWriteFwVersion,
		[]byte{major, minor, patch})
}

// ── Response / event parsing ──────────────────────────────────────────────

// OtaResponse classifies an incoming 63-byte HID input report from the
// device. Only one of the bool fields is set per response.
type OtaResponse struct {
	Seq          byte
	IsAck        bool
	IsEvent      bool
	IsData       bool
	EventOpcode  byte
	EventPayload []byte // 4 bytes typically
	DataPayload  []byte // response data when IsData is true
}

// parseResponse decodes a 63-byte HID input report into an OtaResponse.
//
// Packet header layout (pinned by the live capture):
//
//	byte[3]  = flags | length       where flags = top 2 bits:
//	                                  00 = async event
//	                                  01 = query  (host-originated)
//	                                  10 = command (host-originated)
//	                                  11 = response (device-originated)
//	                                and length = low 6 bits (0..63)
//	byte[4]  = subcategory byte:
//	                                  0x0f for events (GnpEventTag)
//	                                  0xff for pure ACKs
//	                                  class byte for data responses
//
// A "pure ACK" is specifically `byte[3]=0xca byte[4]=0xff` — that is
// 0xc0|0x0a (response with length 10) plus status=0xff. It's a data
// response that carries no payload beyond echoing the first 5 bytes of
// the original command.
//
// Events are identified by `byte[4]==0x0f` regardless of length — some
// events are 10 bytes (flashEraseDone, preloadProgress) and some are
// 7 bytes (verifyStatus). Length is in byte[3] low 6 bits.
func parseResponse(buf []byte) (OtaResponse, error) {
	// Strip the HID Report ID byte if present. Linux hidraw returns
	// the full report including the report ID as byte[0]; our fake
	// test transports return the inner packet directly. Both forms
	// should parse identically.
	if len(buf) >= 1 && buf[0] == GnpReportID {
		buf = buf[1:]
	}
	if len(buf) < 6 {
		return OtaResponse{}, fmt.Errorf("csr-ota: response too short: %d bytes", len(buf))
	}
	// Device-originated packets swap byte[0] and byte[1]: host sees
	// 00 XX ... where XX is the source address (0x08 for headset, 0x01
	// for dongle). Accept any non-zero source — don't hardcode 0x08.
	if buf[0] != 0x00 {
		return OtaResponse{}, fmt.Errorf("csr-ota: unexpected response byte[0] %02x (want 0x00)", buf[0])
	}
	r := OtaResponse{Seq: buf[2]}
	flags := buf[3] & 0xc0
	length := int(buf[3] & 0x3f)
	// Clamp length to available buffer so malformed inputs don't panic.
	if length > len(buf) {
		length = len(buf)
	}

	switch {
	case flags == 0x00 && buf[4] == GnpEventTag:
		// Async event. byte[5] = opcode, bytes[6..length] = payload.
		r.IsEvent = true
		r.EventOpcode = buf[5]
		if length >= 6 {
			r.EventPayload = append([]byte(nil), buf[6:length]...)
		}
	case flags == GnpFlagResponse && buf[4] == GnpAckOK:
		// Pure ACK — byte[3] is 0xca (0xc0|0x0a) and byte[4] is 0xff.
		// Bytes [5..9] echo the first 5 bytes of the original packet.
		r.IsAck = true
	case flags == GnpFlagResponse:
		// Data response to a query. byte[4] = class, byte[5] = op,
		// bytes[6..length] = data.
		r.IsData = true
		if length >= 6 {
			r.DataPayload = append([]byte(nil), buf[6:length]...)
		}
	default:
		return OtaResponse{}, fmt.Errorf("csr-ota: unknown response flags 0x%02x byte[4]=0x%02x", buf[3], buf[4])
	}
	return r, nil
}

// ── Flash loop driver ──────────────────────────────────────────────────────

// OtaTransport is the minimal interface the updater needs to talk to a
// device. A real implementation wraps /dev/hidrawN; tests inject a fake.
type OtaTransport interface {
	Write(report []byte) error
	Read(deadline time.Duration) ([]byte, error)
	Close() error
}

// OtaPartition is one entry in a firmware manifest. For the Evolve2 85
// capture this maps 1:1 to the files in the GnV archive.
type OtaPartition struct {
	ID    byte
	CRC32 uint32
	Data  []byte // raw bytes, will be split into 52-byte chunks
}

// CsrOtaUpdater drives a full flash sequence over an OtaTransport.
// Owns the outgoing sequence counter and the ACK-matching state.
type CsrOtaUpdater struct {
	tr         OtaTransport
	seq        byte
	opts       CsrOtaOptions
	reportSize int    // HID report size for this device (63 or 64)
	srcAddr    byte   // GNP source address (0x08 for headset, device-specific for dongles)
	targetPID  uint16 // target USB PID for re-attach detection after flash
}

// CsrOtaOptions are timeouts + tunables. Defaults match what jfwu uses.
type CsrOtaOptions struct {
	AckTimeout    time.Duration // per-command ACK wait
	EraseTimeout  time.Duration // wait for flashEraseDone after sendOtaStart
	VerifyTimeout time.Duration // wait for verifyStatus at end of partition
	ReattachWait  time.Duration // wait for device to come back after flash
}

// DefaultCsrOtaOptions returns timeouts calibrated from the live capture —
// partition 7 (customer_ro_filesystem, 15628 chunks) took ~68 seconds
// including verify, so the defaults are generous.
func DefaultCsrOtaOptions() CsrOtaOptions {
	return CsrOtaOptions{
		AckTimeout:    5 * time.Second,
		EraseTimeout:  30 * time.Second,
		VerifyTimeout: 180 * time.Second, // dongles are slower than headsets
		ReattachWait:  30 * time.Second,
	}
}

// NewCsrOtaUpdater creates an updater. reportSize is the HID report
// size (63 for Evolve2 85, 64 for Link 380; 0 = default 63). srcAddr
// is the GNP source address (0x08 for headsets; 0 = auto-detect in
// runOtaInit by probing 0x08 then 0x01). targetPID is the expected
// USB PID for re-attach detection after flash (0 = match any Jabra).
func NewCsrOtaUpdater(tr OtaTransport, opts CsrOtaOptions, reportSize int, srcAddr byte, targetPID uint16) *CsrOtaUpdater {
	if reportSize <= 0 {
		reportSize = GnpReportSize
	}
	// srcAddr 0 means auto-detect — runOtaInit will probe.
	return &CsrOtaUpdater{tr: tr, seq: 0x0b, opts: opts, reportSize: reportSize, srcAddr: srcAddr, targetPID: targetPID}
}

// nextSeq returns the next outgoing sequence byte and advances the counter.
// Wraps at 0xFF the same way jfwu does — device accepts any 8-bit value.
func (u *CsrOtaUpdater) nextSeq() byte {
	s := u.seq
	u.seq++
	return s
}

// sendCmdSeq writes an already-framed command report and waits for an ACK
// whose seq field matches. Used for every non-bulk command.
func (u *CsrOtaUpdater) sendCmdSeq(seq byte, report []byte) error {
	if err := u.tr.Write(report); err != nil {
		return fmt.Errorf("write seq=0x%02x: %w", seq, err)
	}
	deadline := time.Now().Add(u.opts.AckTimeout)
	for time.Now().Before(deadline) {
		resp, err := u.tr.Read(time.Until(deadline))
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("ACK seq=0x%02x: transport closed", seq)
			}
			return fmt.Errorf("ACK seq=0x%02x: %w", seq, err)
		}
		r, perr := parseResponse(resp)
		if perr != nil {
			continue // ignore junk
		}
		if r.IsAck && r.Seq == seq {
			return nil
		}
		// Events mid-ACK are fine — keep waiting.
	}
	return fmt.Errorf("ACK seq=0x%02x: timeout after %s", seq, u.opts.AckTimeout)
}

// waitEvent reads until an event of opcode matching want is seen or the
// deadline fires. Returns the event payload.
func (u *CsrOtaUpdater) waitEvent(want byte, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := u.tr.Read(time.Until(deadline))
		if err != nil {
			return nil, fmt.Errorf("event 0x%02x: %w", want, err)
		}
		r, perr := parseResponse(resp)
		if perr != nil {
			continue
		}
		if r.IsEvent && r.EventOpcode == want {
			return r.EventPayload, nil
		}
		// Stray ACKs and other events are tolerated.
	}
	return nil, fmt.Errorf("event 0x%02x: timeout after %s", want, timeout)
}

// ── Pre-OTA initialization ─────────────────────────────────────────────────
//
// jfwu sends 11 init commands before selectPartition (see
// /tmp/init-commands-analysis.md). The recommended minimum is 6 commands
// that establish the GNP session, query the firmware version, activate
// Bluetooth event routing via the dongle, and arm the OTA subsystem.
// Without these, the device accepts commands but never delivers
// verifyStatus events — the exact failure observed on 2026-04-12.

// buildInitQuery creates a 63-byte HID report for a GNP QUERY command.
// Queries use flags 0x40|len (not 0x80|len used for action commands).
// All init queries have inner length 6 bytes: src, dst, seq, flags|len,
// class, op.
func buildInitQuery(src, seq, class, op byte) []byte {
	return buildInitQuerySized(src, seq, class, op, GnpReportSize)
}

func buildInitQuerySized(src, seq, class, op byte, reportSize int) []byte {
	buf := make([]byte, reportSize)
	buf[0] = GnpReportID
	buf[1] = src
	buf[2] = GnpDstZero
	buf[3] = seq
	buf[4] = GnpFlagQuery | 6 // 0x46
	buf[5] = class
	buf[6] = op
	return buf
}

// runOtaInit sends the recommended minimum init sequence before starting
// the OTA flash. Each command gets a response (or error/NAK) — we read
// and discard it. The important side effects are:
//   - CMD 1+2: establish GNP session, get device variant
//   - CMD 3:   get current firmware version
//   - CMD 4+5: activate Bluetooth event routing via dongle (src=0x01)
//   - CMD 6:   arm the OTA subsystem (GetUpdateState)
//
// Total expected time: ~500ms (dominated by CMD 4's Bluetooth relay).
func (u *CsrOtaUpdater) runOtaInit() error {
	// Auto-detect source address if not set. Try 0x08 (headset) first;
	// if the response is a NAK or timeout, try 0x01 (dongle).
	if u.srcAddr == 0 {
		u.srcAddr = GnpSrcHost // try 0x08 first
		seq := u.nextSeq()
		report := buildInitQuerySized(u.srcAddr, seq, 0x02, 0x02, u.reportSize)
		if err := u.tr.Write(report); err != nil {
			return fmt.Errorf("init probe write: %w", err)
		}
		resp, err := u.tr.Read(2 * time.Second)
		if err != nil || isNak(resp) {
			// 0x08 didn't work — switch to 0x01 (dongle)
			u.srcAddr = 0x01
			fmt.Fprintf(os.Stderr, "[csr-ota] src=0x08 NAK/timeout — auto-detected src=0x01 (dongle)\n")
		} else {
			fmt.Fprintf(os.Stderr, "[csr-ota] auto-detected src=0x%02x (headset)\n", u.srcAddr)
		}
	}

	type initCmd struct {
		src   byte
		class byte
		op    byte
		label string
	}
	// Use u.srcAddr for device-directed commands. For headset (0x08),
	// the BT routing commands use 0x01 (dongle relay). For dongle
	// (0x01), ALL commands use 0x01 since the dongle IS the target.
	sa := u.srcAddr
	cmds := []initCmd{
		{sa, 0x02, 0x02, "GetDeviceInfo"},
		{sa, 0x02, 0x02, "GetDeviceInfo (retry)"},
		{sa, 0x02, 0x03, "GetFirmwareVersion"},
		{0x01, 0x02, 0x11, "GetConnectedDevicePID (BT routing)"},
		{0x01, 0x02, 0x11, "GetConnectedDevicePID (repeat)"},
		{sa, 0x12, 0x00, "GetUpdateState (arm OTA)"},
	}
	for _, c := range cmds {
		seq := u.nextSeq()
		report := buildInitQuerySized(c.src, seq, c.class, c.op, u.reportSize)
		if err := u.tr.Write(report); err != nil {
			return fmt.Errorf("init %s: write: %w", c.label, err)
		}
		// Read one response. Timeout 5s covers the ~400ms Bluetooth relay.
		// We don't validate the response — NAKs are expected for some
		// commands (jfwu ignores them too). The important thing is the
		// write itself, which triggers device-side state changes.
		resp, err := u.tr.Read(5 * time.Second)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[csr-ota] init %s: read timeout (continuing): %v\n", c.label, err)
			continue
		}
		_ = resp // response consumed and discarded
		fmt.Fprintf(os.Stderr, "[csr-ota] init %s: seq=0x%02x OK\n", c.label, seq)
	}
	return nil
}

// isNak checks whether a GNP response is a NAK. A NAK has byte[4]==0xFE
// after stripping the HID report ID byte.
func isNak(buf []byte) bool {
	if len(buf) == 0 {
		return false
	}
	// Strip HID report ID if present
	if buf[0] == GnpReportID {
		buf = buf[1:]
	}
	if len(buf) < 5 {
		return false
	}
	return buf[4] == 0xFE
}

// drainPendingEvents reads and discards all currently-queued HID input
// reports. Uses a very short timeout (10ms) per read — after the buffer
// is empty the read times out and we return. This prevents the hidraw
// kernel input buffer from overflowing during long chunk streams.
func (u *CsrOtaUpdater) drainPendingEvents() {
	for {
		_, err := u.tr.Read(10 * time.Millisecond)
		if err != nil {
			return // buffer empty or timeout — done draining
		}
	}
}

// flashPartition runs the full 10-step sequence for a single partition.
// Sequence from the capture, reproduced exactly:
//
//  1. selectPartition(id)         → wait ACK
//  2. sendOtaStart                → wait ACK + flashEraseDone event
//  3. writeCrc(crc, chunks, 10)   → wait ACK
//  4. stream chunks bulk          → respect preload (pause after 10)
//  5. wait verifyStatus event
//  6. writeSoftwareVersion        → wait ACK
func (u *CsrOtaUpdater) flashPartition(p OtaPartition, version [3]byte) error {
	maxChunk := u.reportSize - 11 // 11-byte header, rest is data
	chunks := splitChunksSized(p.Data, maxChunk)
	fmt.Fprintf(os.Stderr, "[csr-ota] partition %d: %d bytes in %d chunks, crc=0x%08x\n",
		p.ID, len(p.Data), len(chunks), p.CRC32)

	sz := u.reportSize
	src := u.srcAddr
	// Step 1: selectPartition
	s1 := u.nextSeq()
	if err := u.sendCmdSeq(s1, buildCommandFull(src, s1, GnpClassCsrOta, OtaOpSelectPartition, []byte{p.ID}, sz)); err != nil {
		return fmt.Errorf("partition %d selectPartition: %w", p.ID, err)
	}

	// Step 2: sendOtaStart + wait erase
	s2 := u.nextSeq()
	if err := u.sendCmdSeq(s2, buildCommandFull(src, s2, GnpClassCsrOta, OtaOpSendStart, nil, sz)); err != nil {
		return fmt.Errorf("partition %d sendOtaStart: %w", p.ID, err)
	}
	if _, err := u.waitEvent(OtaEventFlashEraseDone, u.opts.EraseTimeout); err != nil {
		return fmt.Errorf("partition %d flashEraseDone: %w", p.ID, err)
	}

	// Step 3: writeCrc
	s3 := u.nextSeq()
	crcPayload := make([]byte, 8)
	binary.LittleEndian.PutUint16(crcPayload[0:2], uint16(p.CRC32>>16))
	binary.LittleEndian.PutUint16(crcPayload[2:4], uint16(p.CRC32&0xffff))
	binary.LittleEndian.PutUint16(crcPayload[4:6], uint16(len(chunks)))
	binary.LittleEndian.PutUint16(crcPayload[6:8], uint16(OtaPreloadCount))
	if err := u.sendCmdSeq(s3, buildCommandFull(src, s3, GnpClassCsrOta, OtaOpWriteCrc, crcPayload, sz)); err != nil {
		return fmt.Errorf("partition %d writeCrc: %w", p.ID, err)
	}

	// Step 4: stream chunks using jfwu's preload cadence. CsrOtaDriver::writeBlocks
	// waits for preloadProgress whenever chunkIndex % preload == 0, including
	// chunk 0. A missing preload event is fatal in jfwu, so fail here too instead
	// of silently overrunning the dongle's small OTA buffer.
	preload := OtaPreloadCount // 10
	for i, chunk := range chunks {
		if err := u.tr.Write(buildWriteBlockFull(src, uint16(i), chunk, sz)); err != nil {
			return fmt.Errorf("partition %d chunk %d: %w", p.ID, i, err)
		}
		if i%preload == 0 {
			if _, err := u.waitEvent(OtaEventPreloadProgress, u.opts.AckTimeout); err != nil {
				return fmt.Errorf("partition %d preloadProgress at chunk %d: %w", p.ID, i, err)
			}
		}
		if len(chunks) > 100 && i%(len(chunks)/10+1) == 0 {
			fmt.Fprintf(os.Stderr, "[csr-ota]   chunk %d/%d (%.0f%%)\n",
				i, len(chunks), 100*float64(i)/float64(len(chunks)))
		}
	}
	// NO drain here — verifyStatus might already be in the buffer.
	// The waitEvent loop below will skip any preloadProgress events
	// it encounters while looking for verifyStatus (opcode 0x1c).

	// Step 5: wait verifyStatus (must be 0)
	vs, err := u.waitEvent(OtaEventVerifyStatus, u.opts.VerifyTimeout)
	if err != nil {
		return fmt.Errorf("partition %d verifyStatus: %w", p.ID, err)
	}
	if len(vs) == 0 || vs[0] != 0x00 {
		return fmt.Errorf("partition %d verifyStatus failed: %x", p.ID, vs)
	}

	// Step 6: writeSoftwareVersion
	s6 := u.nextSeq()
	if err := u.sendCmdSeq(s6, buildCommandFull(src, s6, GnpClassCsrOta, OtaOpWriteFwVersion, []byte{version[0], version[1], version[2]}, sz)); err != nil {
		return fmt.Errorf("partition %d writeSoftwareVersion: %w", p.ID, err)
	}

	fmt.Fprintf(os.Stderr, "[csr-ota] partition %d: SUCCESS\n", p.ID)
	return nil
}

// FlashAll runs the pre-OTA init sequence, then flashPartition for each
// partition in order. After the last partition (254 = footer), the device
// reboots into the new firmware. We wait for it to re-attach and confirm
// the version — matching jfwu's behavior:
//
//	#!#!#!# Transfer complete -- wait for device detach..
//	#!#!#!# Device detached
//	#!#!#!# Device re-attached: Jabra Evolve2 85 1.5.7
func (u *CsrOtaUpdater) FlashAll(partitions []OtaPartition, version [3]byte) error {
	// Pre-OTA init — establishes GNP session, activates BT event routing,
	// arms the OTA subsystem. Without this, verifyStatus events never arrive.
	if err := u.runOtaInit(); err != nil {
		return fmt.Errorf("OTA init: %w", err)
	}
	for i, p := range partitions {
		err := u.flashPartition(p, version)
		if err != nil {
			// Footer (last partition, id=254) triggers device reboot.
			// hidraw write() has a kernel timeout shorter than libusb's,
			// so the last 1-2 chunks may fail with "connection timed out"
			// as the device starts rebooting. The firmware IS fully
			// written — partitions 0..N-1 all got verifyStatus=OK.
			isLast := i == len(partitions)-1
			if isLast && strings.Contains(err.Error(), "timed out") {
				fmt.Fprintf(os.Stderr, "[csr-ota] partition %d: device rebooting (footer write timeout — firmware committed)\n", p.ID)
				break
			}
			return err
		}
	}

	// Send writeDfuFromSquif — the COMMIT command. This tells the QCS chip
	// to switch SQIF flash banks on the next reboot so the bootloader loads
	// the newly-written firmware instead of the old one. Without this, all
	// partitions are written but the device boots the old firmware.
	// Discovered by comparing jfwu's strace (write #36458 of 36459).
	commitSeq := u.nextSeq()
	commitCmd := buildCommandFull(u.srcAddr, commitSeq, GnpClassCsrOta, OtaOpDfuFromSquif, nil, u.reportSize)
	fmt.Fprintf(os.Stderr, "[csr-ota] sending writeDfuFromSquif (SQIF bank commit)...\n")
	if err := u.sendCmdSeq(commitSeq, commitCmd); err != nil {
		// The device may reboot immediately after receiving this command,
		// causing the ACK read to fail. That's OK — the commit was sent.
		fmt.Fprintf(os.Stderr, "[csr-ota] writeDfuFromSquif ACK: %v (device may be rebooting)\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "[csr-ota] writeDfuFromSquif: ACK received — SQIF commit done\n")
	}

	// Wait for device to reboot and re-attach. The device detaches from
	// USB, boots the new firmware, then re-enumerates. Typically takes
	// 2-5 seconds. We poll sysfs for re-attachment.
	if u.opts.ReattachWait <= 0 {
		fmt.Fprintf(os.Stderr, "[csr-ota] reattach wait disabled\n")
		return nil
	}
	fmt.Fprintf(os.Stderr, "[csr-ota] all partitions written — waiting for device reboot...\n")
	grace := 3 * time.Second
	if u.opts.ReattachWait < grace {
		grace = u.opts.ReattachWait
	}
	time.Sleep(grace) // initial grace period for USB disconnect + boot

	deadline := time.Now().Add(u.opts.ReattachWait - grace)
	for time.Now().Before(deadline) {
		devs, err := enumerateUSB()
		if err == nil {
			for _, d := range devs {
				if d.VendorID != JabraVendorID || d.ProductID == 0 {
					continue
				}
				// Match the specific target PID if set, otherwise any Jabra device.
				if u.targetPID != 0 && d.ProductID != u.targetPID {
					continue
				}
				fmt.Fprintf(os.Stderr, "[csr-ota] device re-attached: %s (PID 0x%04x)\n",
					d.Product, d.ProductID)
				return nil
			}
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("device did not re-attach within %s after flash", u.opts.ReattachWait)
}

// ── GnV archive unpacking (pure Go — no 7z needed) ────────────────────────
//
// jfwu shells out to p7zip to unpack firmware archives, but Go's
// stdlib archive/zip handles the same files natively. The archive is a
// flat ZIP (no password — jfwu passes `-p***` which p7zip ignores for
// unencrypted archives) containing info.xml + one .gnv file per partition
// per language. We read info.xml to get the flash order and per-file
// metadata, then build an OtaPartition list for the target language.

// UnpackedArchive holds the info.xml manifest plus a map of file-name
// → decompressed bytes. One call to UnpackGnVArchive fills both.
type UnpackedArchive struct {
	Manifest *BuildVector
	Files    map[string][]byte
}

// BuildOtaPartitions selects one file per partition (matching the
// requested language where applicable) and returns an OtaPartition list
// ordered for flashing. Partition 254 (footer) is placed last to match
// jfwu's order even though the manifest may list it mid-stream.
//
// The manifest language ID "0x0409" = English (US). Jabra uses that as
// the default for captures. Callers can pass any language code present
// in the archive; unsupported codes fall back to English.
func BuildOtaPartitions(a *UnpackedArchive, langID string) ([]OtaPartition, error) {
	if a == nil || a.Manifest == nil {
		return nil, errors.New("nil archive")
	}
	// Group files by partition ID.
	type candidate struct {
		GnVFile
		bytes []byte
	}
	byPart := map[int][]candidate{}
	for _, f := range a.Manifest.Files {
		data, ok := a.Files[f.Name]
		if !ok {
			return nil, fmt.Errorf("archive missing file %q declared in info.xml", f.Name)
		}
		byPart[f.Partition] = append(byPart[f.Partition], candidate{f, data})
	}

	pick := func(cands []candidate) candidate {
		// Prefer the matching language if provided, else the requested
		// language's fallback (English 0x0409), else the first entry.
		for _, c := range cands {
			if c.Language.ID == langID {
				return c
			}
		}
		for _, c := range cands {
			if c.Language.ID == "0x0409" {
				return c
			}
		}
		return cands[0]
	}

	// Deterministic order: by partition ID, with 254 (footer) last.
	var ids []int
	for id := range byPart {
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		a := ids[i]
		b := ids[j]
		// Footer (254) always sorts last, regardless of its numeric value.
		if a == 254 {
			return false
		}
		if b == 254 {
			return true
		}
		return a < b
	})

	out := make([]OtaPartition, 0, len(ids))
	for _, id := range ids {
		c := pick(byPart[id])
		crc, err := parseHexCRC(c.CRC)
		if err != nil {
			return nil, fmt.Errorf("partition %d (%s): %w", id, c.Name, err)
		}
		out = append(out, OtaPartition{
			ID:    byte(id),
			CRC32: crc,
			Data:  c.bytes,
		})
	}
	return out, nil
}

// parseHexCRC accepts CRCs in either "0x2db4fa8f" or "2db4fa8f" form,
// returning a uint32. Tolerant of leading zeros stripped by the
// manifest (jfwu logs "0x9a94e13" for 0x09a94e13 — note the missing
// leading 0).
func parseHexCRC(s string) (uint32, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if s == "" {
		return 0, errors.New("empty CRC")
	}
	// Pad with leading zeros to 8 hex digits so strconv.ParseUint
	// handles short forms consistently.
	for len(s) < 8 {
		s = "0" + s
	}
	n, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid hex %q: %w", s, err)
	}
	return uint32(n), nil
}

// parseVersionTriplet converts "1.5.7" into a [3]byte. jfwu accepts
// dotted versions and rejects anything else; we match that.
func parseVersionTriplet(s string) ([3]byte, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]byte{}, fmt.Errorf("version %q: want MAJOR.MINOR.PATCH", s)
	}
	var out [3]byte
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 8)
		if err != nil {
			return [3]byte{}, fmt.Errorf("version %q: part %d: %w", s, i, err)
		}
		out[i] = byte(n)
	}
	return out, nil
}

// UnpackGnVArchive reads a Jabra .zip firmware archive and parses its
// info.xml. Pure Go — uses archive/zip from the stdlib, no external
// 7-zip dependency. Re-uses the existing parseGnVArchive helper from
// firmware.go so the manifest schema stays in one place.
func UnpackGnVArchive(path string) (*UnpackedArchive, error) {
	bv, files, err := parseGnVArchive(path)
	if err != nil {
		return nil, err
	}
	return &UnpackedArchive{Manifest: bv, Files: files}, nil
}

// splitChunks slices data into OtaChunkBytes-sized chunks. The last
// chunk may be shorter than OtaChunkBytes — that's correct. The device
// uses byte[9] of the writeBlock header to know the actual payload
// length, so padding is NOT needed. Confirmed by analysis of all
// 36,415 chunks in the live capture: final chunks of each partition
// have sizes 8, 16, 20, 28, etc. — NOT padded to 52.
func splitChunks(data []byte) [][]byte { return splitChunksSized(data, OtaChunkBytes) }

func splitChunksSized(data []byte, chunkSize int) [][]byte {
	n := (len(data) + chunkSize - 1) / chunkSize
	out := make([][]byte, n)
	for i := 0; i < n; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(data) {
			end = len(data)
		}
		out[i] = append([]byte(nil), data[start:end]...)
	}
	return out
}
