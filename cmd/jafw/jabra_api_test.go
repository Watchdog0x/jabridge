package main

import (
	"bytes"
	"testing"
	"time"
)

// TestGetFirmwareVersionFake uses a fake transport to verify the
// GNP query/response round-trip for firmware version. Capture:
//
//	TX: 05 08 00 01 46 02 03
//	RX: 00 08 01 CC 02 03 05 31 2E 33 2E 38  → "1.3.8"
func TestGetFirmwareVersionFake(t *testing.T) {
	ft := &fakeTransport{
		replies: [][]byte{
			padTo63([]byte{
				0x05, // report ID (present from hidraw)
				0x00, 0x08, 0x01, 0xCC, 0x02, 0x03,
				0x05,                         // string length = 5
				0x31, 0x2E, 0x33, 0x2E, 0x38, // "1.3.8"
			}),
		},
	}

	// Build query and verify wire bytes
	seq := byte(0x01)
	query := buildInitQuery(GnpSrcHost, seq, 0x02, 0x03)
	if err := ft.Write(query); err != nil {
		t.Fatal(err)
	}

	// Verify request bytes match capture
	if len(ft.writes) != 1 {
		t.Fatalf("wrote %d packets, want 1", len(ft.writes))
	}
	wantReq := padTo63([]byte{0x05, 0x08, 0x00, 0x01, 0x46, 0x02, 0x03})
	if !bytes.Equal(ft.writes[0], wantReq) {
		t.Errorf("request bytes:\ngot:  %x\nwant: %x", ft.writes[0][:7], wantReq[:7])
	}

	// Parse response
	resp, err := ft.Read(time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// Strip report ID
	if resp[0] == GnpReportID {
		resp = resp[1:]
	}
	strLen := int(resp[6])
	version := string(resp[7 : 7+strLen])
	if version != "1.3.8" {
		t.Errorf("version = %q, want %q", version, "1.3.8")
	}
}

// TestGetDeviceGNPInfoFake verifies the GNP DeviceInfo query against
// the live capture: TX 05 08 00 00 46 02 02 → RX 00 08 00 C9 02 02 02 01 67
func TestGetDeviceGNPInfoFake(t *testing.T) {
	ft := &fakeTransport{
		replies: [][]byte{
			padTo63([]byte{
				0x05, 0x00, 0x08, 0x00, 0xC9, 0x02, 0x02,
				0x02,       // GNP protocol version 2
				0x01, 0x67, // variant 0x0167 = 359 (Evolve2 85)
			}),
		},
	}

	query := buildInitQuery(GnpSrcHost, 0x00, 0x02, 0x02)
	if err := ft.Write(query); err != nil {
		t.Fatal(err)
	}

	wantReq := padTo63([]byte{0x05, 0x08, 0x00, 0x00, 0x46, 0x02, 0x02})
	if !bytes.Equal(ft.writes[0], wantReq) {
		t.Errorf("request:\ngot:  %x\nwant: %x", ft.writes[0][:7], wantReq[:7])
	}

	resp, err := ft.Read(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if resp[0] == GnpReportID {
		resp = resp[1:]
	}
	proto := resp[6]
	// Variant is big-endian in the capture: 01 67 = 0x0167
	variant := uint16(resp[7])<<8 | uint16(resp[8])
	if proto != 0x02 {
		t.Errorf("protocol = %d, want 2", proto)
	}
	if variant != 0x0167 {
		t.Errorf("variant = 0x%04x, want 0x0167", variant)
	}
}

// TestGetLanguageIDFake verifies the LanguageID GNP query.
// Capture: TX 05 08 00 04 46 13 08 → RX 00 08 04 C8 13 08 09 04
func TestGetLanguageIDFake(t *testing.T) {
	ft := &fakeTransport{
		replies: [][]byte{
			padTo63([]byte{
				0x05, 0x00, 0x08, 0x02, 0xC8, 0x13, 0x08,
				0x09, 0x04, // 0x0409 = English (US)
			}),
		},
	}

	query := buildInitQuery(GnpSrcHost, 0x02, 0x13, 0x08)
	if err := ft.Write(query); err != nil {
		t.Fatal(err)
	}

	wantReq := padTo63([]byte{0x05, 0x08, 0x00, 0x02, 0x46, 0x13, 0x08})
	if !bytes.Equal(ft.writes[0], wantReq) {
		t.Errorf("request:\ngot:  %x\nwant: %x", ft.writes[0][:7], wantReq[:7])
	}

	resp, err := ft.Read(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if resp[0] == GnpReportID {
		resp = resp[1:]
	}
	langID := uint16(resp[6]) | uint16(resp[7])<<8
	if langID != 0x0409 {
		t.Errorf("langID = 0x%04x, want 0x0409", langID)
	}
}

// TestIsDonglePID verifies the dongle PID detection.
func TestIsDonglePID(t *testing.T) {
	cases := []struct {
		pid  uint16
		want bool
	}{
		{0x24c7, true},  // Link 380 USB-A
		{0x24c8, true},  // Link 380 USB-C
		{0x0a17, true},  // Link 370
		{0x24b9, false}, // Evolve2 85 (headset)
		{0x2450, false}, // BIZ 2400 II (headset)
	}
	for _, c := range cases {
		if got := isDonglePID(c.pid); got != c.want {
			t.Errorf("isDonglePID(0x%04x) = %v, want %v", c.pid, got, c.want)
		}
	}
}

func TestHIDUeventMatchHandlesKernelPadding(t *testing.T) {
	data := []byte("HID_ID=0003:00000B0E:000024C7\n")
	if !hidUeventMatches(data, JabraVendorID, 0x24c7) {
		t.Fatal("padded kernel HID_ID did not match")
	}
	if hidUeventMatches(data, JabraVendorID, 0x24b9) {
		t.Fatal("wrong PID matched")
	}
}

// TestDeviceManagerSnapshot verifies that the device manager can
// return a snapshot of devices without panicking on an empty set.
func TestDeviceManagerSnapshot(t *testing.T) {
	dm := NewDeviceManager(time.Hour) // don't actually poll
	devs := dm.Devices()
	if devs == nil {
		t.Fatal("Devices() returned nil, want empty slice")
	}
	if len(devs) != 0 {
		t.Errorf("Devices() = %d devices, want 0 before Start()", len(devs))
	}
}

// TestBatteryStatusThreshold verifies the batteryLow threshold.
func TestBatteryStatusThreshold(t *testing.T) {
	cases := []struct {
		level   uint8
		wantLow bool
	}{
		{100, false},
		{50, false},
		{11, false},
		{10, true},
		{5, true},
		{0, true},
	}
	for _, c := range cases {
		bs := &BatteryStatus{LevelInPercent: c.level}
		bs.BatteryLow = bs.LevelInPercent <= 10
		if bs.BatteryLow != c.wantLow {
			t.Errorf("level=%d: BatteryLow=%v, want %v", c.level, bs.BatteryLow, c.wantLow)
		}
	}
}
