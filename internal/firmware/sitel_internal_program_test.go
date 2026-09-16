package firmware

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestXAPEncodingSignExtensionAndBranches(t *testing.T) {
	for _, vector := range []struct {
		opcode  byte
		operand uint16
		words   [2]uint16
	}{
		{0x18, 0x9000, [2]uint16{0x9000, 0x0018}}, // reference LD X,9000
		{0x27, 0xffff, [2]uint16{0, 0xff27}},      // ST AL,(-1,Y), NOP padded
		{0x14, 0x0080, [2]uint16{0x0100, 0x8014}},
		{0x14, 0x007f, [2]uint16{0, 0x7f14}},
		{0x14, 0xff80, [2]uint16{0, 0x8014}},
	} {
		if words := xapInstruction(vector.opcode, vector.operand); words != vector.words {
			t.Fatal("wrong XAP operand prefix", vector, words)
		}
	}
	var p xapProgramBuilder
	p.label("again")
	p.emit(0x14, 0)
	p.branch(xapAlways, "again")
	code, err := p.finish()
	if err != nil || !reflect.DeepEqual(code, []uint16{0, 0x14, 0, 0xfde0}) {
		t.Fatal("branch displacement must use the opcode address", code, err)
	}
	var missing xapProgramBuilder
	missing.branch(xapAlways, "missing")
	if _, err := missing.finish(); err == nil {
		t.Fatal("unknown branch label accepted")
	}
	var repeated xapProgramBuilder
	repeated.label("one")
	repeated.label("one")
	if _, err := repeated.finish(); err == nil {
		t.Fatal("duplicate label accepted")
	}
}

func TestLocalOwnInternalFlashProgramEncoding(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_XAP_INTERNAL_ORACLE")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_XAP_INTERNAL_ORACLE for the independent prototype/decoder comparison")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Words []uint16
		Cases []json.RawMessage
	}
	if err := json.Unmarshal(data, &oracle); err != nil || len(oracle.Cases) != 21 {
		t.Fatal("incomplete standalone controller prototype evidence", err)
	}
	code, err := buildSitelInternalFlashProgram()
	if err != nil || !reflect.DeepEqual(code, oracle.Words) {
		t.Fatal("Go program differs from the decoded and simulated independent prototype", err)
	}
}
