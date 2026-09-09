package firmware

import (
	"bytes"
	"testing"
)

func managementReports(id byte, size int) []HIDReport {
	field := HIDField{SizeBits: 8, Count: uint32(size - 1), UsagePage: 0xff00, Usages: []uint32{1}, Flags: 0x102}
	return []HIDReport{{ID: id, Kind: "input", Bytes: size, Fields: []HIDField{field}}, {ID: id, Kind: "output", Bytes: size, Fields: []HIDField{field}}}
}

func TestControlLayoutUsesUsageAndPreservesReportNumber(t *testing.T) {
	for _, test := range []struct {
		id   byte
		size int
	}{{2, 33}, {5, 63}, {5, 64}, {7, 33}} {
		layout, err := SelectControlLayout(managementReports(test.id, test.size))
		if err != nil {
			t.Fatal(err)
		}
		packet := make([]byte, 63)
		copy(packet, []byte{5, 8, 0, 0x31, 0x46, 2, 3})
		frames, err := layout.Encode(packet)
		if err != nil || len(frames) != 1 || len(frames[0]) != test.size || frames[0][0] != test.id || !bytes.Equal(frames[0][1:7], packet[1:7]) {
			t.Fatal(frames, err)
		}
	}
	invalid := managementReports(2, 33)
	invalid[0].Fields[0].UsagePage = 0xff20
	if _, err := SelectControlLayout(invalid); err == nil {
		t.Fatal("non-management page accepted")
	}
	ambiguous := append(managementReports(2, 33), managementReports(5, 64)...)
	if _, err := SelectControlLayout(ambiguous); err == nil {
		t.Fatal("ambiguous interface accepted")
	}
}

func TestControlWriteFragmentsAndReplyReassembly(t *testing.T) {
	layout, _ := SelectControlLayout(managementReports(2, 33))
	packet := make([]byte, 63)
	copy(packet, []byte{5, 8, 0, 0x31, 0xbe, 0x13, 0x2a})
	for i := 7; i < len(packet); i++ {
		packet[i] = byte(i)
	}
	frames, err := layout.Encode(packet)
	if err != nil || len(frames) != 2 || frames[0][0] != 2 || frames[1][0] != 2 {
		t.Fatal(frames, err)
	}
	if !bytes.Equal(append(frames[0][1:], frames[1][1:31]...), packet[1:]) {
		t.Fatal("fragment content changed")
	}
	// A matching response, interleaved with an unrelated consumer report.
	frames[0][1], frames[0][2], frames[0][4] = 0, 8, 0xfe
	assembler := controlAssembler{layout: layout}
	if got, err := assembler.push(frames[0]); got != nil || err != nil {
		t.Fatal(got, err)
	}
	if got, err := assembler.push([]byte{1, 0}); got != nil || err != nil {
		t.Fatal(got, err)
	}
	got, err := assembler.push(frames[1])
	if err != nil || len(got) != 63 || got[0] != 5 || got[2] != 8 || got[4] != 0xfe || !bytes.Equal(got[7:], packet[7:]) {
		t.Fatal(got, err)
	}
	ack := make([]byte, 33)
	copy(ack, []byte{2, 0, 8, 0x32, 0xc5, 0xff})
	got, err = assembler.push(ack)
	if err != nil || !bytes.Equal(got, []byte{5, 0, 8, 0x32, 0xc5, 0xff}) {
		t.Fatal(got, err)
	}
}
