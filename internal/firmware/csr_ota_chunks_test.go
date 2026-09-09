package firmware

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"testing"
)

func TestOTACRCCountGoldenVectors(t *testing.T) {
	for _, test := range []struct {
		count    uint32
		extended bool
		want     string
	}{
		{10, false, "b42d8ffa0a000a00"},
		{65535, false, "b42d8ffaffff0a00"},
		{65535, true, "b42d8ffa00000a00ffff0000"},
		{65536, true, "b42d8ffa00000a0000000100"},
		{71156, true, "b42d8ffa00000a00f4150100"},
		{math.MaxUint32, true, "b42d8ffa00000a00ffffffff"},
	} {
		t.Run(fmt.Sprintf("%d/extended=%t", test.count, test.extended), func(t *testing.T) {
			got, err := otaCRCHeader(0x2db4fa8f, test.count, 10, test.extended)
			if err != nil || hex.EncodeToString(got) != test.want {
				t.Fatalf("header=%x err=%v want=%s", got, err, test.want)
			}
			if !test.extended && !bytes.Equal(got, cmdWriteCrc(0x11, 0x2db4fa8f, uint16(test.count), 10)[7:15]) {
				t.Fatal("legacy packet changed")
			}
		})
	}
	for _, extended := range []bool{false, true} {
		if _, err := otaCRCHeader(1, 0, 10, extended); err == nil {
			t.Fatal("zero count")
		}
		if _, err := otaCRCHeader(1, 1, 0, extended); err == nil {
			t.Fatal("zero preload")
		}
	}
	if _, err := otaCRCHeader(1, 65536, 10, false); err == nil {
		t.Fatal("legacy overflow accepted")
	}
	got, err := otaCRCHeader(0x12345678, 0x87654321, 0x1234, true)
	if err != nil || hex.EncodeToString(got) != "341278560000341221436587" {
		t.Fatal(got, err)
	}
}

func TestOTAChunkCountBoundaries(t *testing.T) {
	for _, test := range []struct {
		size, report int
		extended     bool
		want         uint32
	}{
		{1, 63, false, 1}, {52, 63, false, 1}, {53, 63, false, 2},
		{53, 64, false, 1}, {54, 64, false, 2},
		{65535 * 52, 63, false, 65535}, {65535*52 + 1, 63, false, 0},
		{65535*52 + 1, 63, true, 65536},
		{3700072, 63, true, 71156}, {3700072, 64, true, 69813},
		{0, 63, false, 0}, {-1, 63, true, 0}, {1, 11, false, 0}, {1, 65, true, 0},
	} {
		t.Run(fmt.Sprintf("%d/%d/%t", test.size, test.report, test.extended), func(t *testing.T) {
			got, err := otaChunkCount(test.size, test.report, test.extended)
			if (err != nil) != (test.want == 0) || got != test.want {
				t.Fatal(got, err)
			}
		})
	}
	if strconv.IntSize == 64 {
		tooLarge := uint64(math.MaxUint32)*52 + 1
		if _, err := otaChunkCount(int(tooLarge), 63, true); err == nil {
			t.Fatal("32-bit count overflow")
		}
		if _, err := otaChunkCount(int(^uint(0)>>1), 63, true); err == nil {
			t.Fatal("integer ceiling overflow")
		}
	}
}

func TestOTABlockIndexRolloverGoldenVectors(t *testing.T) {
	for _, test := range []struct {
		logical uint32
		want    string
	}{
		{0, "0000"}, {65534, "feff"}, {65535, "ffff"}, {65536, "0000"},
		{65537, "0100"}, {131072, "0000"}, {math.MaxUint32, "ffff"},
	} {
		t.Run(fmt.Sprint(test.logical), func(t *testing.T) {
			// The host's logical offset stays 32-bit. Only the wire index wraps.
			frame := buildWriteBlockFull(8, uint16(test.logical), []byte{0xaa, 0xbb, 0xcc}, 63)
			if hex.EncodeToString(frame[7:9]) != test.want || hex.EncodeToString(frame[9:14]) != "0300aabbcc" || frame[4] != 13 {
				t.Fatalf("wrong block header: %x", frame[:14])
			}
		})
	}
}

func TestOTAPreloadCountWidths(t *testing.T) {
	for _, test := range []struct {
		event []byte
		want  uint32
		bits  int
	}{
		{[]byte{0x1b, 0, 0}, 0, 16}, {[]byte{0x1b, 0xff, 0xff}, 65535, 16},
		{[]byte{0x1b, 0xff, 0xff, 0xaa}, 65535, 16}, // observed length fallback
		{[]byte{0x1b, 0, 0, 1, 0}, 65536, 32},
		{[]byte{0x1b, 0xf4, 0x15, 1, 0}, 71156, 32},
		{[]byte{0x1b, 0xff, 0xff, 0xff, 0xff}, math.MaxUint32, 32},
		{[]byte{0x1b, 0x78, 0x56, 0x34, 0x12, 0xee}, 0x12345678, 32},
		{nil, 0, 0}, {[]byte{0x1b}, 0, 0}, {[]byte{0x1b, 0}, 0, 0},
		{[]byte{0x1c, 0, 0}, 0, 0},
	} {
		t.Run(hex.EncodeToString(test.event), func(t *testing.T) {
			got, bits, err := decodeOTAPreload(test.event)
			if got != test.want || bits != test.bits || (err != nil) != (test.bits == 0) {
				t.Fatal(got, bits, err)
			}
		})
	}
}

func TestOTAInvalidTransferRejectedBeforeInit(t *testing.T) {
	for _, test := range []struct {
		parts  []OtaPartition
		report int
	}{
		{nil, 63}, {[]OtaPartition{{ID: 1}}, 63},
		{[]OtaPartition{{ID: 1, Data: []byte{1}}}, 11},
	} {
		tr := &otaNoIOTransport{}
		err := NewCsrOtaUpdater(tr, DefaultCsrOtaOptions(), test.report, 8, 0).FlashAll(test.parts, [3]byte{1, 0, 0})
		if err == nil || tr.writes != 0 || tr.reads != 0 {
			t.Fatal(err, tr)
		}
	}
}
