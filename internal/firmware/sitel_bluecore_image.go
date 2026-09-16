package firmware

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	sitelBluecoreSectorWords = 0x1000
	sitelBluecoreImageWords  = 0x401 * sitelBluecoreSectorWords
	sitelBluecoreProgramBase = 0x10000
)

// XPV program words follow the 64K-word XDV data window in the flash image.
// Each present sector starts erased. Later records can patch earlier records;
// the original Pro 9470 images use these overlays. CHIP_VERSION sections must
// be selected first: the Engage 75 archive contains two different chip images.
type sitelBluecoreImage struct {
	Chip    string
	Sectors map[uint16][]uint16
}

func (im *sitelBluecoreImage) word(address uint32) uint16 {
	sector := im.Sectors[uint16(address/sitelBluecoreSectorWords)]
	if len(sector) == 0 {
		return 0xffff
	}
	return sector[address%sitelBluecoreSectorWords]
}

func (im *sitelBluecoreImage) put(address uint32, value uint16) {
	index := uint16(address / sitelBluecoreSectorWords)
	sector := im.Sectors[index]
	if sector == nil {
		sector = make([]uint16, sitelBluecoreSectorWords)
		for i := range sector {
			sector[i] = 0xffff
		}
		im.Sectors[index] = sector
	}
	sector[address%sitelBluecoreSectorWords] = value
}

func validSitelBluecoreChip(chip string) bool {
	return chip == "elvis" || chip == "gordon" || chip == "rick"
}

func sitelBluecoreChipMarker(line string) (string, bool, error) {
	if !strings.HasPrefix(line, "// GN ") {
		return "", false, nil
	}
	fields := strings.Fields(strings.TrimPrefix(line, "// GN "))
	if len(fields) == 0 || fields[0] != "CHIP_VERSION" {
		return "", false, nil
	}
	if len(fields) != 2 || fields[1] != "all" && !validSitelBluecoreChip(fields[1]) {
		return "", true, errors.New("unknown Bluecore chip section")
	}
	return fields[1], true, nil
}

func parseSitelBluecoreImages(xpv, xdv []byte, chip string) (*sitelBluecoreImage, error) {
	if !validSitelBluecoreChip(chip) {
		return nil, errors.New("bluecore image requires an identified chip")
	}
	im := &sitelBluecoreImage{Chip: chip, Sectors: make(map[uint16][]uint16)}
	if err := im.parseWords(xdv, 0, sitelBluecoreProgramBase); err != nil {
		return nil, fmt.Errorf("XDV: %w", err)
	}
	if err := im.parseWords(xpv, sitelBluecoreProgramBase, sitelBluecoreImageWords-sitelBluecoreProgramBase); err != nil {
		return nil, fmt.Errorf("XPV: %w", err)
	}
	// The reference reader removes a placeholder zero at data address zero
	// when every other word in that first 256-word block is erased.
	if im.word(0) == 0 {
		empty := true
		for address := uint32(1); address < 256; address++ {
			if im.word(address) != 0xffff {
				empty = false
				break
			}
		}
		if empty {
			im.put(0, 0xffff)
		}
	}
	return im, nil
}

func (im *sitelBluecoreImage) parseWords(data []byte, base, limit uint32) error {
	if len(data) == 0 || len(data) > 32<<20 {
		return errors.New("invalid Bluecore image size")
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 4096)
	active, count, lineNumber := true, 0, 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		chip, marker, err := sitelBluecoreChipMarker(line)
		if err != nil {
			return fmt.Errorf("line %d: %w", lineNumber, err)
		}
		if marker {
			active = chip == im.Chip || chip == "all"
			continue
		}
		line, _, _ = strings.Cut(line, "//")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 || !strings.HasPrefix(fields[0], "@") || len(fields[1]) != 4 {
			return fmt.Errorf("invalid Bluecore word at line %d", lineNumber)
		}
		address, err := parseSitelBluecoreHex(fields[0][1:], 32)
		if err != nil || address >= uint64(limit) {
			return fmt.Errorf("bluecore address outside image at line %d", lineNumber)
		}
		value, err := parseSitelBluecoreHex(fields[1], 16)
		if err != nil {
			return fmt.Errorf("invalid Bluecore value at line %d", lineNumber)
		}
		if active {
			im.put(base+uint32(address), uint16(value))
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if count == 0 {
		return errors.New("no image words for the identified Bluecore chip")
	}
	return nil
}

// PSR records are ordered operations, not a map: a key can be deleted and then
// written again in the same file. Chip labels are retained for the installer's
// component selection. Parsing itself does not read or change a device.
type sitelPSRRecord struct {
	Chip   string
	Key    uint16
	Delete bool
	Words  []uint16
}

func parseSitelPSR(data []byte) ([]sitelPSRRecord, error) {
	if len(data) == 0 || len(data) > 1<<20 {
		return nil, errors.New("invalid Bluecore settings size")
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 4096)
	var records []sitelPSRRecord
	chip := "all"
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		name, marker, err := sitelBluecoreChipMarker(line)
		if err != nil {
			return nil, fmt.Errorf("PSR line %d: %w", lineNumber, err)
		}
		if marker {
			chip = name
			continue
		}
		line, _, _ = strings.Cut(line, "//")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) < 6 || line[0] != '&' {
			return nil, fmt.Errorf("invalid PSR record at line %d", lineNumber)
		}
		key, err := parseSitelBluecoreHex(line[1:5], 16)
		if err != nil {
			return nil, fmt.Errorf("invalid PSR key at line %d", lineNumber)
		}
		record := sitelPSRRecord{Chip: chip, Key: uint16(key)}
		remainder := strings.TrimSpace(line[5:])
		if remainder == "-" {
			record.Delete = true
		} else {
			if !strings.HasPrefix(remainder, "=") {
				return nil, fmt.Errorf("missing PSR assignment at line %d", lineNumber)
			}
			words := strings.Fields(remainder[1:])
			// Empty assignments in official PSRs are no-ops. Deletion uses
			// the separate '-' syntax; treating '=' as deletion loses keys.
			if len(words) == 0 {
				continue
			}
			if len(words) > 100 {
				return nil, fmt.Errorf("invalid PSR word count at line %d", lineNumber)
			}
			for _, word := range words {
				value, err := parseSitelBluecoreHex(word, 16)
				if err != nil || len(word) != 4 {
					return nil, fmt.Errorf("invalid PSR value at line %d", lineNumber)
				}
				record.Words = append(record.Words, uint16(value))
			}
		}
		records = append(records, record)
		if len(records) > 1000 {
			return nil, errors.New("too many Bluecore settings records")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, errors.New("no Bluecore settings records")
	}
	return records, nil
}

func parseSitelBluecoreHex(text string, bits int) (uint64, error) {
	for _, c := range text {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return 0, errors.New("invalid hexadecimal word")
		}
	}
	return strconv.ParseUint(text, 16, bits)
}
