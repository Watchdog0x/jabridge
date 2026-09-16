package firmware

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"testing"
)

func TestSitelResourceImageIdentityAndVersion(t *testing.T) {
	for _, target := range []byte{4, 5, 27, 28} {
		data := make([]byte, 256)
		copy(data[11:21], "5.17.1")
		binary.LittleEndian.PutUint32(data[30:34], 0x0b0e1111)
		parts := []hexImageSegment{{Address: 0x160000, Data: data}}
		area := sitelArea{Address: 0x160000, Size: 256}
		info := sitelDeviceInfo{ID: 0x0b0e1111, SectorSize: 256}
		if _, err := prepareSitelImage(target, parts, area, info, "5.17.1"); err != nil {
			t.Fatal(target, err)
		}
		if _, err := prepareSitelImage(target, parts, area, info, "5.20.1"); err == nil {
			t.Fatal("resource version was replaced with the main firmware version")
		}
		info.ID = 0x0b0e1116
		if _, err := prepareSitelImage(target, parts, area, info, "5.17.1"); err == nil {
			t.Fatal("base resource was accepted for a headset")
		}
	}
}

func TestLocalEngage75OriginalHEXMetadata(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_ENGAGE75_IMAGE_AUDIT")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_ENGAGE75_IMAGE_AUDIT for original HEX metadata checks")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Path   string
		Images []struct {
			File, Version string
			Target        byte
			Base          uint32
			Metadata      []struct{ ID, Offset uint32 }
		}
	}
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != 1 || len(rows[0].Images) != 7 {
		t.Fatal("incomplete Engage 75 metadata evidence", err)
	}
	manifest, files, err := parseGnVArchive(rows[0].Path)
	if err != nil || manifest.Version != "5.20.1" {
		t.Fatal("could not read original Engage 75 archive", err)
	}
	for _, image := range rows[0].Images {
		parts, err := parseIntelHexImage(files[image.File])
		if err != nil || len(image.Metadata) != 1 {
			t.Fatal("invalid image metadata fixture", image.File, err)
		}
		end := parts[len(parts)-1].Address + uint32(len(parts[len(parts)-1].Data))
		// This is explicit simulated area geometry. The installed updater
		// must use the actual bootloader's reported geometry before erasing.
		area := sitelArea{Address: image.Base, Size: (end - image.Base + 4095) &^ 4095}
		info := sitelDeviceInfo{ID: image.Metadata[0].ID, ImageInfoOffset: image.Metadata[0].Offset, SectorSize: 4096}
		prepared, err := prepareSitelImage(image.Target, parts, area, info, image.Version)
		if err != nil || prepared.Version != image.Version {
			t.Fatal("original native/Go metadata mismatch", image.File, err)
		}
		if image.Target == 22 && info.ID != 0x0b0e1117 {
			t.Fatal("MMI controller image identity was confused with the base")
		}
	}
}
