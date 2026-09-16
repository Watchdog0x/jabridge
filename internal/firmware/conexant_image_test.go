package firmware

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func conexantTestS3(address uint32, data ...byte) string {
	record := []byte{byte(len(data) + 5), byte(address >> 24), byte(address >> 16), byte(address >> 8), byte(address)}
	record = append(record, data...)
	var sum byte
	for _, b := range record {
		sum += b
	}
	return fmt.Sprintf("S3%X%02X\n", record, ^sum)
}

func TestConexantRecordsPreserveOrderedWrites(t *testing.T) {
	source := conexantTestS3(0x14, 0) + conexantTestS3(0x1038, 0x11, 0x29) + conexantTestS3(0x2649, 0x43, 0x58, 0x33) + conexantTestS3(0x14, 0x50) + "S70500000000FA\n"
	got, err := parseConexantRecords([]byte(source))
	want := []conexantRecord{{0x14, []byte{0}}, {0x1038, []byte{0x11, 0x29}}, {0x2649, []byte{0x43, 0x58, 0x33}}, {0x14, []byte{0x50}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered patch: %v %v", got, err)
	}
	for _, bad := range []string{
		strings.TrimSuffix(source, "S70500000000FA\n"),
		source + conexantTestS3(0x14, 0),
		strings.Replace(source, "S306", "S307", 1),
		strings.Replace(source, "0000001400E5", "0000001401E5", 1),
		conexantTestS3(0xffff, 1, 2) + "S70500000000FA\n",
		"S70500000000FA\n",
	} {
		if _, err := parseConexantRecords([]byte(bad)); err == nil {
			t.Fatal("accepted incomplete or corrupt patch")
		}
	}
}

func TestConexantLegacyPacketAddressBands(t *testing.T) {
	// Independent vectors retain the high address byte, including bit 12.
	for _, tc := range []struct {
		address uint32
		value   byte
		write   bool
		want    []byte
	}{
		{0x0038, 0, false, []byte{0x20, 0x38, 0, 0, 0xa4}},
		{0x1038, 0x11, true, []byte{0xb0, 0x38, 0, 0x11, 0x03}},
		{0x2649, 0x43, true, []byte{0xa6, 0x49, 0, 0x43, 0xca}},
	} {
		got, err := conexantLegacyPacket(tc.address, tc.value, tc.write)
		if err != nil || !bytes.Equal(got, tc.want) {
			t.Fatalf("%x: %x %v", tc.address, got, err)
		}
	}
	if _, err := conexantLegacyPacket(0x10000, 0, true); err == nil {
		t.Fatal("truncated an address")
	}
}

func TestConexantCalibrationOverlap(t *testing.T) {
	record := conexantRecord{Address: 100, Data: []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}}
	for _, tc := range []struct {
		start, end uint32
		want       []conexantRecord
	}{
		{103, 107, []conexantRecord{{100, []byte{0, 1, 2}}, {107, []byte{7, 8, 9}}}},
		{98, 104, []conexantRecord{{104, []byte{4, 5, 6, 7, 8, 9}}}},
		{108, 112, []conexantRecord{{100, []byte{0, 1, 2, 3, 4, 5, 6, 7}}}},
		{98, 112, nil},
		{110, 112, []conexantRecord{record}},
	} {
		got := conexantWritablePieces(record, tc.start, tc.end)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%d..%d: %v", tc.start, tc.end, got)
		}
	}
}

func TestLocalConexantOriginalPackets(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_CONEXANT_ORACLE")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_CONEXANT_ORACLE for original managed-parser comparison")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		Rows []struct {
			Path, MD5 string
			Streams   map[string][]byte
		}
	}
	if err := json.Unmarshal(data, &audit); err != nil || len(audit.Rows) != 14 {
		t.Fatalf("incomplete archive evidence: %v", err)
	}
	seen := map[uint16]bool{}
	for _, row := range audit.Rows {
		image, err := loadConexantImage(row.Path)
		if err != nil {
			t.Fatal(err)
		}
		pids, err := parseTargetPIDs(image.Manifest.TargetUSBPIDs)
		if err != nil || len(pids) != 1 || seen[pids[0]] {
			t.Fatal("duplicate or invalid target", err)
		}
		seen[pids[0]] = true
		if !NativeFirmwareProtocolSupported(pids[0], 5) {
			t.Fatal("UC Voice installer is not advertised for its exact PID")
		}
		if err := ValidateInstallInput([]string{row.Path}); err != nil {
			t.Fatal(err)
		}
		device := interactiveDFUTestDevice(t, pids[0])
		if _, err := interactiveInstallBindingForDevices(row.Path, pids[0], []USBDevice{device}); err != nil {
			t.Fatal("TUI could not prepare UC Voice firmware", err)
		}
		checksum, err := firmwareFileMD5(row.Path)
		if err != nil || checksum != row.MD5 {
			t.Fatal("archive changed", err)
		}
		if os.Getenv("JABRIDGE_TEST_LIVE_RELEASES") == "1" {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := verifyConexantRelease(ctx, row.Path, image, pids[0])
			cancel()
			if err != nil {
				t.Fatalf("%04x official release validation: %v", pids[0], err)
			}
		}
		var legacy, plus []byte
		for _, record := range image.Records {
			for i, value := range record.Data {
				packet, err := conexantLegacyPacket(record.Address+uint32(i), value, true)
				if err != nil {
					t.Fatal(err)
				}
				legacy = append(legacy, packet...)
			}
			packet, err := conexantPlusPacket(record.Address, record.Data, len(record.Data), true)
			if err != nil {
				t.Fatal(err)
			}
			plus = append(plus, packet...)
		}
		if !bytes.Equal(legacy, row.Streams["legacy"]) || !bytes.Equal(plus, row.Streams["plus"]) {
			t.Fatalf("native packets differ from original updater for %04x", pids[0])
		}
		for _, mode := range []bool{false, true} {
			client, peer := newConexantTestClient(mode)
			calibration, err := client.captureCalibration(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			saved := false
			if err := transferConexant(context.Background(), client, image.Records, calibration, func() error { saved = true; return nil }, nil); err != nil {
				t.Fatalf("%04x plus=%t simulated transfer: %v", pids[0], mode, err)
			}
			if !saved || len(peer.writes) == 0 {
				t.Fatal("original archive never reached the transfer")
			}
		}
		t.Logf("%04x: %d records; all legacy and plus packets match original managed code", pids[0], len(image.Records))
	}
}
