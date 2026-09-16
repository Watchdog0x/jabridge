package headsetvolume

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"testing"
)

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
