package firmware

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Wire-format helpers shared by the legacy and extended transfer engines.
// Official release evidence, not the image size, selects the protocol.
func otaChunkCount(dataBytes, reportBytes int, extended bool) (uint32, error) {
	if reportBytes != 63 && reportBytes != 64 {
		return 0, errors.New("unsupported CSR OTA report size")
	}
	return otaChunkCountForPayload(dataBytes, reportBytes-11, extended)
}

func otaChunkCountForPayload(dataBytes, payloadBytes int, extended bool) (uint32, error) {
	if payloadBytes < 1 || payloadBytes > 53 {
		return 0, errors.New("invalid CSR OTA payload size")
	}
	if dataBytes <= 0 {
		return 0, errors.New("empty CSR OTA image")
	}
	count := uint64((dataBytes-1)/payloadBytes) + 1
	if count > math.MaxUint32 {
		return 0, errors.New("CSR OTA chunk count exceeds 32-bit limit")
	}
	if !extended && count > math.MaxUint16 {
		return 0, fmt.Errorf("image needs %d chunks, exceeds legacy chunk limit of 65535; extended transfer is not enabled", count)
	}
	return uint32(count), nil
}

// CRC halves retain their historical order. Extended count mode replaces the
// legacy total with a zero marker and appends a 32-bit LE count after preload.
func otaCRCHeader(crc, count uint32, preload uint16, extended bool) ([]byte, error) {
	if count == 0 || preload == 0 {
		return nil, errors.New("CSR OTA count and preload must be nonzero")
	}
	if !extended && count > math.MaxUint16 {
		return nil, errors.New("CSR OTA total exceeds legacy chunk limit")
	}
	size := 8
	if extended {
		size = 12
	}
	payload := make([]byte, size)
	binary.LittleEndian.PutUint16(payload[0:2], uint16(crc>>16))
	binary.LittleEndian.PutUint16(payload[2:4], uint16(crc))
	binary.LittleEndian.PutUint16(payload[6:8], preload)
	if extended {
		binary.LittleEndian.PutUint32(payload[8:12], count)
	} else {
		binary.LittleEndian.PutUint16(payload[4:6], uint16(count))
	}
	return payload, nil
}

// The input is the unpadded event body, including opcode at byte zero, not a
// whole HID report. Return the observed width so a 16-bit value is never silently
// promoted to evidence of extended progress. Three/four bytes use legacy width;
// five or more use extended width. Malformed events are not progress zero.
func decodeOTAPreload(event []byte) (count uint32, bits int, err error) {
	if len(event) < 3 || event[0] != OtaEventPreloadProgress {
		return 0, 0, errors.New("invalid CSR OTA preload event")
	}
	if len(event) >= 5 {
		return binary.LittleEndian.Uint32(event[1:5]), 32, nil
	}
	return uint32(binary.LittleEndian.Uint16(event[1:3])), 16, nil
}
