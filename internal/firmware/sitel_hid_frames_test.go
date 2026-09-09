package firmware

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"testing"
)

func TestSitelHIDNativeFragmentVectors(t *testing.T) {
	for _, size := range []int{33, 64} {
		for _, length := range []int{0, 1, size - 5, size - 4, 100, 1050} {
			t.Run(fmt.Sprintf("%d/%d", size, length), func(t *testing.T) {
				payload := make([]byte, length)
				for i := range payload {
					payload[i] = byte(i)
				}
				layout := sitelHIDLayout{ReportID: 7, ReportBytes: size, MaxMessage: 4096}
				frames, next, err := encodeSitelHID(layout, payload, 15, 170)
				if err != nil {
					t.Fatal(err)
				}
				if next != byte(15+len(frames))&15 {
					t.Fatal("bad rollover")
				}
				// Pinned original-code vector: 100 bytes, 33-byte numbered reports.
				if size == 33 && length == 100 {
					want := "073f6400aa000102030405060708090a0b0c0d0e0f101112131415161718191a1b"
					if hex.EncodeToString(frames[0]) != want || len(frames) != 4 {
						t.Fatal("native vector mismatch")
					}
					if frames[1][1] != 0x40 || frames[1][2] != 28 || frames[2][1] != 0x41 || frames[2][2] != 59 || frames[3][2] != 90 {
						t.Fatal("native continuation offsets")
					}
				}
				assembler := sitelHIDAssembler{layout: layout, nextFragment: 15}
				for i, frame := range frames {
					result, err := assembler.Push(frame)
					if err != nil {
						t.Fatal(err)
					}
					if result.Complete != (i == len(frames)-1) {
						t.Fatal("premature or missing completion")
					}
					if result.Complete && (!bytes.Equal(result.Payload, payload) || result.Sequence != 170) {
						t.Fatal("wrong payload")
					}
					duplicate, err := assembler.Push(frame)
					if err != nil || !duplicate.Duplicate || duplicate.Complete {
						t.Fatal("duplicate delivered", err)
					}
				}
			})
		}
	}
}

func TestSitelHIDRejectsMalformedAndReorderedFragments(t *testing.T) {
	layout := sitelHIDLayout{ReportID: 7, ReportBytes: 33, MaxMessage: 128}
	frames, _, err := encodeSitelHID(layout, bytes.Repeat([]byte{42}, 100), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"size", "report", "length", "sequence", "continuation", "interrupted", "changed-duplicate"} {
		t.Run(name, func(t *testing.T) {
			assembler := sitelHIDAssembler{layout: layout}
			raw := append([]byte(nil), frames[0]...)
			switch name {
			case "size":
				raw = raw[:10]
			case "report":
				raw[0] = 8
			case "length":
				raw[2] = 255
			case "sequence":
				raw[1]++
			case "continuation":
				raw[1] = 0x40
			case "interrupted":
				_, err = assembler.Push(frames[0])
				if err != nil {
					t.Fatal(err)
				}
				raw[1]++
			case "changed-duplicate":
				_, err = assembler.Push(frames[0])
				if err != nil {
					t.Fatal(err)
				}
				raw[10]++
			}
			if _, err = assembler.Push(raw); err == nil {
				t.Fatal("invalid fragment accepted")
			}
			if assembler.active || len(assembler.pending) != 0 {
				t.Fatal("retained invalid partial message")
			}
		})
	}
}
