package firmware

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func TestFirmwareTransportUsesDeclaredReportSize(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	transport := &HidrawTransport{f: writer, reportSize: 64}
	if err := transport.Write(make([]byte, 63)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)
	n, err := reader.Read(buffer)
	if err != nil || n != 64 {
		t.Fatalf("framing: n=%d err=%v", n, err)
	}
	if err := transport.Write(make([]byte, 65)); err == nil {
		t.Fatal("oversized report accepted")
	}
}

func TestParseGnpOutputReportSizeFound(t *testing.T) {
	descriptor := []byte{
		0x75, 0x08, // Report Size: 8 bits
		0x85, 0x05, // Report ID: 5 (GNP)
		0x95, 0x3e, // Report Count: 62
		0x91, 0x02, // Output
	}
	size, found := parseGnpOutputReportSizeFound(descriptor)
	if !found || size != 63 {
		t.Fatalf("GNP output report = size %d found=%v", size, found)
	}
	descriptor[3] = 0x04
	if _, found := parseGnpOutputReportSizeFound(descriptor); found {
		t.Fatal("non-GNP report ID was accepted")
	}
	if _, found := parseGnpOutputReportSizeFound([]byte{0x76, 0x01}); found {
		t.Fatal("truncated HID descriptor was accepted")
	}
}

// TestBuildDongleQueryChildProductID matches capture line:
//
//	GnpEndpoint[hidraw7]: --> 04 00 05 46 02 11
//
// Dongle child PID query, seq 0x05, sub 0x11.
func TestBuildDongleQueryChildProductID(t *testing.T) {
	got := buildDongleQuery(0x05, DongleChildProductID)
	want := padTo63([]byte{0x05, 0x04, 0x00, 0x05, 0x46, 0x02, 0x11})
	if !bytes.Equal(got, want) {
		t.Errorf("dongle child PID query bytes:\ngot:  %x\nwant: %x", got[:7], want[:7])
	}
}

// TestBuildDongleQueryChildName matches capture line:
//
//	GnpEndpoint[hidraw7]: --> 04 00 07 46 02 00
func TestBuildDongleQueryChildName(t *testing.T) {
	got := buildDongleQuery(0x07, DongleChildName)
	want := padTo63([]byte{0x05, 0x04, 0x00, 0x07, 0x46, 0x02, 0x00})
	if !bytes.Equal(got, want) {
		t.Errorf("dongle child name query bytes:\ngot:  %x\nwant: %x", got[:7], want[:7])
	}
}

// TestBuildDongleQueryChildSerial matches capture line:
//
//	GnpEndpoint[hidraw7]: --> 04 00 06 46 02 01
func TestBuildDongleQueryChildSerial(t *testing.T) {
	got := buildDongleQuery(0x06, DongleChildSerial)
	want := padTo63([]byte{0x05, 0x04, 0x00, 0x06, 0x46, 0x02, 0x01})
	if !bytes.Equal(got, want) {
		t.Errorf("dongle child serial query bytes:\ngot:  %x\nwant: %x", got[:7], want[:7])
	}
}

// TestQueryChildProductIDWithFakeTransport uses a scripted fake
// transport to validate the full send/parse path. Response bytes
// taken verbatim from the capture line:
//
//	GnpEndpoint[hidraw7]: <-- 00 04 05 c8 02 11 b9 24
func TestQueryChildProductIDWithFakeTransport(t *testing.T) {
	ft := &fakeTransport{
		replies: [][]byte{
			padTo63([]byte{0x00, 0x04, 0x05, 0xc8, 0x02, 0x11, 0xb9, 0x24}),
		},
	}
	pid, err := QueryChildProductID(ft, 0x05, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 0x24b9 {
		t.Errorf("PID = 0x%04x, want 0x24b9", pid)
	}
	// Verify the wire bytes we sent match the captured request exactly.
	if len(ft.writes) != 1 {
		t.Fatalf("wrote %d packets, want 1", len(ft.writes))
	}
	wantReq := padTo63([]byte{0x05, 0x04, 0x00, 0x05, 0x46, 0x02, 0x11})
	if !bytes.Equal(ft.writes[0], wantReq) {
		t.Errorf("request bytes:\ngot:  %x\nwant: %x", ft.writes[0], wantReq)
	}
}

// TestQueryChildNameWithFakeTransport matches the capture:
//
//	<-- 00 04 07 d7 02 00 10 4a 61 62 72 61 20 45 76 6f 6c 76 65 32 20 38 35
//	       ^d7 = resp class         ^10 = len=16  ^"Jabra Evolve2 85"
func TestQueryChildNameWithFakeTransport(t *testing.T) {
	resp := []byte{
		0x00, 0x04, 0x07, 0xd7, 0x02, 0x00, 0x10,
		0x4a, 0x61, 0x62, 0x72, 0x61, 0x20, // "Jabra "
		0x45, 0x76, 0x6f, 0x6c, 0x76, 0x65, 0x32, 0x20, // "Evolve2 "
		0x38, 0x35, // "85"
	}
	ft := &fakeTransport{replies: [][]byte{padTo63(resp)}}
	name, err := QueryChildName(ft, 0x07, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Jabra Evolve2 85" {
		t.Errorf("name = %q, want %q", name, "Jabra Evolve2 85")
	}
}

// TestQueryChildSerialWithFakeTransport matches the capture:
//
//	<-- 00 04 06 d3 02 01 0c 41 31 42 32 43 33 44 34 45 35 46 36
//	       ^d3                    ^0c=12  ^"A1B2C3D4E5F6"
func TestQueryChildSerialWithFakeTransport(t *testing.T) {
	resp := []byte{
		0x00, 0x04, 0x06, 0xd3, 0x02, 0x01, 0x0c,
		0x41, 0x31, 0x42, 0x32, 0x43, 0x33, 0x44, 0x34, 0x45, 0x35, 0x46, 0x36,
	}
	ft := &fakeTransport{replies: [][]byte{padTo63(resp)}}
	serial, err := QueryChildSerial(ft, 0x06, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if serial != "A1B2C3D4E5F6" {
		t.Errorf("serial = %q, want %q", serial, "A1B2C3D4E5F6")
	}
}

// TestParseDongleChildResponseSeqMismatch verifies that a stale response
// (wrong seq byte) is rejected rather than silently mis-parsed.
func TestParseDongleChildResponseSeqMismatch(t *testing.T) {
	resp := padTo63([]byte{0x00, 0x04, 0x05, 0xc8, 0x02, 0x11, 0xb9, 0x24})
	_, err := parseDongleChildResponse(resp, 0x06, DongleChildProductID)
	if err == nil {
		t.Fatal("expected error for seq mismatch")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("seq mismatch")) {
		t.Errorf("error = %q, want seq-mismatch message", err)
	}
}

// TestHidrawTransportWriteLengthCheck — the transport rejects any
// write that isn't exactly 63 bytes BEFORE touching the fd, so we can
// verify it with a zero-value struct (no real file needed). A short
// or long write to real hardware would corrupt the protocol.
func TestHidrawTransportWriteLengthCheck(t *testing.T) {
	tr := &HidrawTransport{path: "test"} // f nil — never reached
	if err := tr.Write(make([]byte, 50)); err == nil {
		t.Fatal("expected error for short (50-byte) write")
	}
	if err := tr.Write(make([]byte, 70)); err == nil {
		t.Fatal("expected error for long (70-byte) write")
	}
	// Nil Close should also not panic.
	if err := (&HidrawTransport{}).Close(); err != nil {
		t.Errorf("Close on zero-value returned %v, want nil", err)
	}
}
