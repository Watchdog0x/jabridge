package firmware

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type hexImageSegment struct {
	Address uint32
	Data    []byte
}

// Validate Sitel-family image records while retaining sparse addresses. No
// guessed fill bytes, memory-map assumptions, or USB firmware commands are used.
func parseIntelHexImage(data []byte) ([]hexImageSegment, error) {
	if len(data) == 0 || int64(len(data)) > MaxExpandedArchiveSize {
		return nil, errors.New("invalid Intel HEX image size")
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), 1024)
	var base uint32
	var segments []hexImageSegment
	end := false
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if end || !strings.HasPrefix(text, ":") {
			return nil, fmt.Errorf("invalid Intel HEX record at line %d", line)
		}
		record, err := hex.DecodeString(text[1:])
		if err != nil || len(record) < 5 || len(record) != int(record[0])+5 {
			return nil, fmt.Errorf("invalid Intel HEX length at line %d", line)
		}
		var sum byte
		for _, value := range record {
			sum += value
		}
		if sum != 0 {
			return nil, fmt.Errorf("intel HEX checksum failed at line %d", line)
		}
		address := binary.BigEndian.Uint16(record[1:3])
		payload := record[4 : len(record)-1]
		switch record[3] {
		case 0:
			if len(payload) == 0 || uint64(base)+uint64(address)+uint64(len(payload)) > 1<<32 {
				return nil, errors.New("invalid Intel HEX data address")
			}
			segments = append(segments, hexImageSegment{Address: base + uint32(address), Data: append([]byte(nil), payload...)})
			if len(segments) > 1<<20 {
				return nil, errors.New("too many Intel HEX records")
			}
		case 1:
			if address != 0 || len(payload) != 0 {
				return nil, errors.New("invalid Intel HEX end record")
			}
			end = true
		case 2, 4:
			if address != 0 || len(payload) != 2 {
				return nil, errors.New("invalid Intel HEX base address")
			}
			base = uint32(binary.BigEndian.Uint16(payload))
			if record[3] == 2 {
				base <<= 4
			} else {
				base <<= 16
			}
		case 3, 5:
			if address != 0 || len(payload) != 4 {
				return nil, errors.New("invalid Intel HEX entry point")
			}
			// Entry-point metadata is not an upload address or a command to execute.
		default:
			return nil, errors.New("unsupported Intel HEX record type")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.New("intel HEX record exceeds size limit")
	}
	if !end || len(segments) == 0 {
		return nil, errors.New("incomplete Intel HEX image")
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].Address < segments[j].Address })
	for i := 1; i < len(segments); i++ {
		if uint64(segments[i-1].Address)+uint64(len(segments[i-1].Data)) > uint64(segments[i].Address) {
			return nil, errors.New("overlapping Intel HEX data")
		}
	}
	return segments, nil
}

type sitelPlannedImage struct {
	File     GnVFile
	Segments []hexImageSegment
}

// Preserve the raw Sitel target ID and GNP address. The transport must validate
// their protocol-specific interpretation; settings addresses are not FWU IDs.
func planSitelImages(manifest *BuildVector, files map[string][]byte) ([]sitelPlannedImage, error) {
	if manifest == nil || len(manifest.Files) == 0 {
		return nil, errors.New("missing Sitel images")
	}
	var result []sitelPlannedImage
	seen := map[string]bool{}
	for _, file := range manifest.Files {
		if file.SitelHidTargetID == "" || file.GNPAddress == "" || file.UpdateOrder < 0 || seen[file.SitelHidTargetID] {
			return nil, errors.New("missing or ambiguous Sitel target metadata")
		}
		seen[file.SitelHidTargetID] = true
		segments, err := parseIntelHexImage(files[file.Name])
		if err != nil {
			return nil, fmt.Errorf("sitel image %q: %w", file.Name, err)
		}
		result = append(result, sitelPlannedImage{File: file, Segments: segments})
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].File.UpdateOrder < result[j].File.UpdateOrder })
	return result, nil
}
