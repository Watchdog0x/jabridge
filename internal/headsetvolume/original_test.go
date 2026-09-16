package headsetvolume

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestLocalOriginalFirmwareDescriptors(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_HEADSET_DESCRIPTOR_ORACLE")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_HEADSET_DESCRIPTOR_ORACLE for original firmware descriptor vectors")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		ImageSHA256 string `json:"imageSHA256"`
		Cases       []struct {
			PID         uint16
			Descriptors string
		}
	}
	if err := json.Unmarshal(data, &oracle); err != nil || oracle.ImageSHA256 != "2b7b47731439c31d0f5e212c569aca7db8f34fa674f6d9f9731f63a117bbea48" || len(oracle.Cases) != 4 {
		t.Fatal("incomplete original firmware descriptors", err)
	}
	seen := map[uint16]bool{}
	for _, v := range oracle.Cases {
		data, err := hex.DecodeString(v.Descriptors)
		if err != nil || len(data) < 18 || binary.LittleEndian.Uint16(data[10:]) != v.PID || seen[v.PID] {
			t.Fatal("invalid original descriptor vector", v.PID, err)
		}
		seen[v.PID] = true
		info, err := Inspect(data)
		if v.PID == ProductID {
			if err != nil || info.Channels != 2 || !bytes.Equal(data, originalDescriptorFixture(t)) {
				t.Fatal("original supported topology does not match regression fixture", info, err)
			}
		} else if err == nil {
			t.Fatalf("unsupported PID %04x accepted", v.PID)
		}
	}
	if !seen[ProductID] {
		t.Fatal("supported model missing from original descriptors")
	}
	t.Log("4 original firmware descriptor sets checked; supported topology matches the permanent fixture")
}

func TestLocalOriginalFirmwareVolumeVectors(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_HEADSET_VOLUME_ORACLE")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_HEADSET_VOLUME_ORACLE for original firmware execution vectors")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		ImageSHA256 string `json:"imageSHA256"`
		Vectors     []struct {
			Mode, Percent int
			SavedCode     int16
			SetCode       int16
			InternalLevel int
		}
	}
	if err := json.Unmarshal(data, &oracle); err != nil || oracle.ImageSHA256 != "2b7b47731439c31d0f5e212c569aca7db8f34fa674f6d9f9731f63a117bbea48" || len(oracle.Vectors) != 303 {
		t.Fatal("incomplete original firmware vectors", err)
	}
	for _, v := range oracle.Vectors {
		p := &peer{code: v.SavedCode}
		value, err := Set(context.Background(), p, v.Percent)
		if err != nil || len(p.requests) != 2 || value.Code != v.SetCode || v.InternalLevel < 0 || v.InternalLevel > 15 {
			t.Fatal("Go/native volume mismatch", v, value, err)
		}
		r, w := p.requests[0], p.requests[1]
		if r.kind != 0xa2 || r.op != 0x81 || r.value != 0x200 || r.index != 0x200 || w.kind != 0x22 || w.op != 1 || w.value != 0x200 || w.index != 0x200 || int16(binary.LittleEndian.Uint16(w.data)) != v.SetCode {
			t.Fatal("Go packet differs from original firmware execution", v)
		}
	}
	t.Log("303 original firmware execution vectors matched; firmware OS/USB boundaries are modeled")
}
