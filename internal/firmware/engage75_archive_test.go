package firmware

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"
)

func TestEngage75RadioMetadataMatchesSelectedChip(t *testing.T) {
	data := []byte("// GN CHIP_VERSION rick\n// GN FWU_ID 0x0b0e1111\n// GN VERSION 5.20.1\n// GN CHIP_VERSION gordon\n// GN FWU_ID 0x0b0e1111\n// GN VERSION 5.19.0\n")
	if err := validateEngage75RadioMetadata(data, "rick", "5.20.1"); err != nil {
		t.Fatal(err)
	}
	if err := validateEngage75RadioMetadata(data, "gordon", "5.20.1"); err == nil {
		t.Fatal("other chip's version substituted for the selected section")
	}
	for _, data := range []string{"// GN FWU_ID 0x0b0e1117\n// GN VERSION 5.20.1", "// GN VERSION 5.20.1", "// GN FWU_ID 0x0b0e1111", "// GN CHIP_VERSION unknown\n// GN FWU_ID 0x0b0e1111\n// GN VERSION 5.20.1"} {
		if err := validateEngage75RadioMetadata([]byte(data), "rick", "5.20.1"); err == nil {
			t.Fatal("invalid radio identity accepted")
		}
	}
}

func TestLocalEngage75OfficialReleaseVariants(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_ENGAGE75_ARCHIVE")
	if path == "" || os.Getenv("JABRIDGE_TEST_LIVE_RELEASES") != "1" {
		t.Skip("set Engage 75 archive and JABRIDGE_TEST_LIVE_RELEASES=1 for official release checks")
	}
	archive, err := loadSitelDECTArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range engage75RuntimePIDs {
		t.Run(fmt.Sprintf("%04x", pid), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := verifySitelDECTRelease(ctx, path, archive, pid); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestLocalEngage75CompleteArchive(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_ENGAGE75_ARCHIVE")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_ENGAGE75_ARCHIVE for the original complete package")
	}
	archive, err := loadEngage75Archive(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.HEX) != 7 || len(archive.Radio) != 2 || len(archive.Settings["gordon"]) != 243 || len(archive.Settings["rick"]) != 243 {
		t.Fatal("incomplete component set")
	}
	var order []string
	for _, image := range archive.HEX {
		order = append(order, image.File.SitelHidTargetID)
	}
	if !slices.Equal(order, []string{"1", "28", "22", "21", "5", "4", "27"}) {
		t.Fatal("HEX target order changed", order)
	}
	find := func(chip string) []uint16 {
		var value []uint16
		for _, record := range archive.Settings[chip] {
			if record.Key == 0xf002 {
				value = record.Words
			}
		}
		return value
	}
	if !slices.Equal(find("rick"), []uint16{0, 0, 0, 0x2000}) || !slices.Equal(find("gordon"), []uint16{0, 0, 0, 0x1000}) {
		t.Fatal("chip-specific settings were merged")
	}
}
