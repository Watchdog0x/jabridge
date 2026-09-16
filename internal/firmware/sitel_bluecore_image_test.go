package firmware

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestSitelBluecoreChipSectionsAndOverlays(t *testing.T) {
	xpv := []byte("// GN CHIP_VERSION rick\n@0000 1234\n@0001 ABCD\n@0000 4567\n// GN CHIP_VERSION gordon\n@0000 9999\n// GN CHIP_VERSION all\n@0002 5555\n")
	xdv := []byte("@0000 0000\n@0080 D397\n")
	for chip, want := range map[string]uint16{"rick": 0x4567, "gordon": 0x9999, "elvis": 0xffff} {
		im, err := parseSitelBluecoreImages(xpv, xdv, chip)
		if err != nil || im.word(sitelBluecoreProgramBase) != want || im.word(sitelBluecoreProgramBase+2) != 0x5555 || im.word(0x80) != 0xd397 || im.word(0) != 0 {
			t.Fatalf("chip %s: %v", chip, err)
		}
		if chip == "gordon" && im.word(sitelBluecoreProgramBase+1) != 0xffff {
			t.Fatal("mixed words from different chips")
		}
	}
	im, err := parseSitelBluecoreImages([]byte("@0000 1000"), []byte("@0000 0000"), "elvis")
	if err != nil || im.word(0) != 0xffff {
		t.Fatal("placeholder zero was not removed", err)
	}
}

func TestSitelBluecoreImagesRejectMalformedInput(t *testing.T) {
	for _, input := range []string{"", "@0 0000 extra", "@0 12345", "@0 12", "@+1 0000", "@0 -001", "@400000 0000", "// GN CHIP_VERSION future\n@0 0001", "// GN CHIP_VERSION gordon\n@0 0001", "@0 0000\n" + strings.Repeat("x", 4097)} {
		if _, err := parseSitelBluecoreImages([]byte(input), []byte("@0 1234"), "rick"); err == nil {
			t.Fatalf("accepted invalid XPV %q", input[:min(len(input), 70)])
		}
	}
	if _, err := parseSitelBluecoreImages([]byte("@0 0001"), []byte("@10000 0001"), "rick"); err == nil {
		t.Fatal("XDV address escaped into program memory")
	}
	if _, err := parseSitelBluecoreImages([]byte("@0 0001"), []byte("@0 0001"), ""); err == nil {
		t.Fatal("chip identity was guessed")
	}
}

func TestSitelPSRPreservesOrderAndDeletes(t *testing.T) {
	records, err := parseSitelPSR([]byte("// GN CHIP_VERSION rick\r\n&0123 = 1234 abcd // comment\n&0123 =\n&0123 -\n&0123 = FFFF\n// GN CHIP_VERSION gordon\n&0123 = 0000\n"))
	if err != nil || len(records) != 4 || records[0].Chip != "rick" || records[0].Key != 0x123 || !slices.Equal(records[0].Words, []uint16{0x1234, 0xabcd}) || !records[1].Delete || records[2].Words[0] != 0xffff || records[3].Chip != "gordon" {
		t.Fatal("PSR operations changed", records, err)
	}
	for _, input := range []string{"", "&0001", "&0001 =", "&0001 = 12345", "&0001 = +001", "&0001 - 1234", "&0001 = -", "&0001 1234", "&0001 = 00gg", "// GN CHIP_VERSION unknown\n&0001 = 1234", "&0001 = " + strings.Repeat("0000 ", 101), strings.Repeat("&0001 -\n", 1001)} {
		if _, err := parseSitelPSR([]byte(input)); err == nil {
			t.Fatalf("accepted invalid PSR %q", input[:min(len(input), 70)])
		}
	}
}

func TestLocalSitelBluecoreOriginalImages(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_BLUECORE_IMAGE_AUDIT")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_BLUECORE_IMAGE_AUDIT for original XPV/XDV/PSR checks")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var audit []struct {
		Path, XPV, XDV, PSR, Chip, ImageSHA256, PSRSHA256 string
		Sectors, Settings                                 int
	}
	if err := json.Unmarshal(data, &audit); err != nil || len(audit) != 5 {
		t.Fatal("incomplete Bluecore archive audit", err)
	}
	for _, row := range audit {
		t.Run(row.XPV+"/"+row.Chip, func(t *testing.T) {
			archive, err := zip.OpenReader(row.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = archive.Close() }()
			read := func(name string) []byte {
				r, err := archive.Open(name)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = r.Close() }()
				data, err := io.ReadAll(io.LimitReader(r, (32<<20)+1))
				if err != nil || len(data) > 32<<20 {
					t.Fatal("invalid fixture size", err)
				}
				return data
			}
			im, err := parseSitelBluecoreImages(read(row.XPV), read(row.XDV), row.Chip)
			if err != nil || len(im.Sectors) != row.Sectors {
				t.Fatal("original image failed", err)
			}
			var indices []uint16
			for index := range im.Sectors {
				indices = append(indices, index)
			}
			slices.Sort(indices)
			hash := sha256.New()
			for _, index := range indices {
				// Independent audit format: sector index, then 4096 LE words.
				if err := binary.Write(hash, binary.LittleEndian, index); err != nil {
					t.Fatal(err)
				}
				if err := binary.Write(hash, binary.LittleEndian, im.Sectors[index]); err != nil {
					t.Fatal(err)
				}
			}
			if hex.EncodeToString(hash.Sum(nil)) != row.ImageSHA256 {
				t.Fatal("original flash words do not match independent audit")
			}
			if row.PSR != "" {
				records, err := parseSitelPSR(read(row.PSR))
				if err != nil || len(records) != row.Settings {
					t.Fatal("original settings failed", err)
				}
				var buf bytes.Buffer
				for _, r := range records {
					buf.WriteString(r.Chip)
					buf.WriteByte(0)
					count := uint16(len(r.Words))
					if r.Delete {
						count = 0xffff
					}
					for _, v := range append([]uint16{r.Key, count}, r.Words...) {
						buf.WriteByte(byte(v))
						buf.WriteByte(byte(v >> 8))
					}
				}
				digest := sha256.Sum256(buf.Bytes())
				if hex.EncodeToString(digest[:]) != row.PSRSHA256 {
					t.Fatal("original settings operations do not match independent audit")
				}
			}
		})
	}
}
