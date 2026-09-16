package firmware

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type conexantRecord struct {
	Address uint32
	Data    []byte
}

type conexantImage struct {
	Manifest *BuildVector
	Records  []conexantRecord
}

func conexantPID(pid uint16) bool {
	switch pid {
	case 0x0342, 0x0343, 0x0344, 0x0345, 0x0346, 0x0347, 0x0348, 0x0349, 0x034a, 0x034b, 0x034c, 0x034d, 0x034e, 0x034f:
		return true
	default:
		return false
	}
}

func isConexantManifest(manifest *BuildVector) bool {
	if manifest == nil {
		return false
	}
	for _, file := range manifest.Files {
		if strings.EqualFold(filepath.Ext(file.Name), ".ptc") {
			return true
		}
	}
	return false
}

func loadConexantImage(path string) (*conexantImage, error) {
	manifest, files, err := parseGnVArchive(path)
	if err != nil {
		return nil, err
	}
	pids, err := parseTargetPIDs(manifest.TargetUSBPIDs)
	if err != nil || len(pids) != 1 || !conexantPID(pids[0]) || len(manifest.Files) != 1 {
		return nil, errors.New("unsupported UC Voice firmware target or image set")
	}
	file := manifest.Files[0]
	if !strings.EqualFold(filepath.Ext(file.Name), ".ptc") || file.Content != "firmware" || file.Target != "headset" || file.Version != manifest.Version {
		return nil, errors.New("invalid UC Voice firmware manifest")
	}
	if _, err := parseVersionTriplet(manifest.Version); err != nil {
		return nil, err
	}
	records, err := parseConexantRecords(files[file.Name])
	if err != nil {
		return nil, err
	}
	return &conexantImage{Manifest: manifest, Records: records}, nil
}

// S3 addresses are big-endian. Preserve record order, including repeated
// writes: the first/last records can disable and activate the patch.
func parseConexantRecords(data []byte) ([]conexantRecord, error) {
	if len(data) == 0 || len(data) > 2<<20 {
		return nil, errors.New("invalid UC Voice patch size")
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), 1024)
	var result []conexantRecord
	ended := false
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if ended {
			return nil, errors.New("UC Voice patch has records after its end marker")
		}
		if len(text) < 4 || text[0] != 'S' {
			return nil, fmt.Errorf("invalid S-record on line %d", line)
		}
		record, err := hex.DecodeString(text[2:])
		if err != nil || len(record) < 3 || int(record[0]) != len(record)-1 {
			return nil, fmt.Errorf("invalid S-record length on line %d", line)
		}
		var sum byte
		for _, b := range record {
			sum += b
		}
		if sum != 255 {
			return nil, fmt.Errorf("patch record checksum mismatch on line %d", line)
		}
		switch text[1] {
		case '3':
			if len(record) < 7 {
				return nil, errors.New("empty UC Voice patch record")
			}
			address := binary.BigEndian.Uint32(record[1:5])
			payload := append([]byte(nil), record[5:len(record)-1]...)
			if uint64(address)+uint64(len(payload)) > 1<<16 {
				return nil, errors.New("UC Voice patch address exceeds EEPROM")
			}
			result = append(result, conexantRecord{Address: address, Data: payload})
		case '0':
			if len(record) < 4 || len(result) != 0 {
				return nil, errors.New("invalid UC Voice patch header")
			}
		case '5':
			if len(record) != 4 || int(binary.BigEndian.Uint16(record[1:3])) != len(result) {
				return nil, errors.New("UC Voice patch record count mismatch")
			}
		case '7':
			if len(record) != 6 {
				return nil, errors.New("invalid UC Voice patch end marker")
			}
			ended = true
		default:
			return nil, fmt.Errorf("unsupported S-record type %c", text[1])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 || !ended {
		return nil, errors.New("UC Voice patch is empty or incomplete")
	}
	return result, nil
}

// Legacy packets have a checksum equal to the sum of each complemented
// header/data byte. This is not a device key and is not ~sum(bytes).
func conexantLegacyPacket(address uint32, value byte, write bool) ([]byte, error) {
	// The high address byte is ORed with the command flags. Preserve all
	// address bits here; the calibration comparison uses a separate mask.
	if address >= 1<<16 {
		return nil, errors.New("unsupported legacy UC Voice address")
	}
	flags := byte(0x20)
	if write {
		flags = 0xa0
	}
	packet := []byte{flags | byte(address>>8), byte(address), 0, value, 0}
	for _, b := range packet[:4] {
		packet[4] += ^b
	}
	return packet, nil
}

func conexantPlusPacket(address uint32, data []byte, count int, write bool) ([]byte, error) {
	if count < 1 || count > 255 || uint64(address)+uint64(count) > 1<<16 || write && len(data) != count {
		return nil, errors.New("invalid UC Voice block address or size")
	}
	packet := []byte{0x20, byte(count), byte(address >> 8), byte(address)}
	if write {
		packet[0] = 0x60
		packet = append(packet, data...)
	}
	return packet, nil
}

// Split, rather than drop or shift, data on both sides of protected device
// calibration. End is exclusive; the legacy caller converts its inclusive
// calibration length before calling this helper.
func conexantWritablePieces(record conexantRecord, start, end uint32) []conexantRecord {
	recordEnd := record.Address + uint32(len(record.Data))
	if start >= end || recordEnd <= start || record.Address >= end {
		return []conexantRecord{record}
	}
	var result []conexantRecord
	if record.Address < start {
		result = append(result, conexantRecord{Address: record.Address, Data: record.Data[:start-record.Address]})
	}
	if recordEnd > end {
		result = append(result, conexantRecord{Address: end, Data: record.Data[end-record.Address:]})
	}
	return result
}
