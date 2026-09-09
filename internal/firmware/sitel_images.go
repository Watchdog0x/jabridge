package firmware

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

type sitelDeviceInfo struct {
	ID, ImageInfoOffset, SectorSize, WriteSize uint32
	Mode                                       byte
	Version                                    uint16
}
type sitelArea struct {
	Address, Size uint32
	Version       string
}
type sitelPreparedImage struct {
	Target   byte
	Area     sitelArea
	Segments []hexImageSegment
	Version  string
}

func decodeSitelInfo(data []byte) (sitelDeviceInfo, error) {
	if len(data) != 17 && len(data) != 19 {
		return sitelDeviceInfo{}, errors.New("invalid Sitel device-info reply")
	}
	info := sitelDeviceInfo{ID: binary.LittleEndian.Uint32(data), ImageInfoOffset: binary.LittleEndian.Uint32(data[4:]), SectorSize: binary.LittleEndian.Uint32(data[8:]), WriteSize: binary.LittleEndian.Uint32(data[12:]), Mode: data[16]}
	if len(data) == 19 {
		info.Version = uint16(data[17])<<8 | uint16(data[18])
	}
	if info.Version == 0 && info.SectorSize == 4096 {
		info.SectorSize = 65536
	}
	if info.ID == 0 || info.SectorSize == 0 || info.SectorSize > 1<<20 || info.SectorSize&(info.SectorSize-1) != 0 || info.WriteSize == 0 || info.WriteSize > 1014 || info.Mode > 3 {
		return sitelDeviceInfo{}, errors.New("unsupported Sitel flash geometry")
	}
	return info, nil
}

func decodeSitelArea(data []byte) (sitelArea, error) {
	if len(data) < 17 || len(data) != 17+int(data[15])+int(data[16]) {
		return sitelArea{}, errors.New("invalid Sitel area reply")
	}
	area := sitelArea{Address: binary.LittleEndian.Uint32(data), Size: binary.LittleEndian.Uint32(data[4:])}
	if area.Size == 0 || uint64(area.Address)+uint64(area.Size) > 1<<32 {
		return sitelArea{}, errors.New("invalid Sitel area range")
	}
	area.Version = string(bytesBeforeNUL(data[17+int(data[15]):]))
	return area, nil
}

func bytesBeforeNUL(value []byte) []byte {
	for i, b := range value {
		if b == 0 {
			return value[:i]
		}
	}
	return value
}

// Read only bytes actually present in the HEX file. Missing ranges are errors,
// not implicit zeroes/FF. Pointer rebasing is separate from upload addresses.
func readSitelImage(segments []hexImageSegment, address uint32, size int) ([]byte, error) {
	if size < 0 || size > 1<<20 || uint64(address)+uint64(size) > 1<<32 {
		return nil, errors.New("invalid Sitel image read range")
	}
	result := make([]byte, 0, size)
	cursor := uint64(address)
	i := sort.Search(len(segments), func(i int) bool { return uint64(segments[i].Address)+uint64(len(segments[i].Data)) > cursor })
	for ; i < len(segments) && len(result) < size; i++ {
		segment := segments[i]
		if uint64(segment.Address) > cursor {
			return nil, errors.New("missing Sitel image bytes")
		}
		offset := int(cursor - uint64(segment.Address))
		count := min(size-len(result), len(segment.Data)-offset)
		result = append(result, segment.Data[offset:offset+count]...)
		cursor += uint64(count)
	}
	if len(result) != size {
		return nil, errors.New("incomplete Sitel image metadata")
	}
	return result, nil
}

func readSitelCString(segments []hexImageSegment, address uint32) (string, error) {
	var result []byte
	for offset := uint64(0); offset < 256; offset++ {
		if uint64(address)+offset >= 1<<32 {
			return "", errors.New("sitel string address overflow")
		}
		value, err := readSitelImage(segments, address+uint32(offset), 1)
		if err != nil {
			return "", err
		}
		if value[0] == 0 {
			return string(result), nil
		}
		if value[0] < 32 || value[0] > 126 {
			return "", errors.New("invalid Sitel image text")
		}
		result = append(result, value[0])
	}
	return "", errors.New("unterminated Sitel image text")
}

// Engage's secondary controller image is staged in the headset's advertised
// secondary area. Its internal pointers refer to the controller's runtime base;
// do not upload at that runtime address or rewrite the original image bytes.
func prepareSitelImage(target byte, segments []hexImageSegment, area sitelArea, info sitelDeviceInfo, wanted string) (sitelPreparedImage, error) {
	if len(segments) == 0 || area.Size == 0 || info.SectorSize == 0 || uint64(area.Address)+uint64(area.Size) > 1<<32 || area.Address%info.SectorSize != 0 || area.Size%info.SectorSize != 0 {
		return sitelPreparedImage{}, errors.New("unaligned or missing Sitel target area")
	}
	for _, seg := range segments {
		if uint64(seg.Address) < uint64(area.Address) || uint64(seg.Address)+uint64(len(seg.Data)) > uint64(area.Address)+uint64(area.Size) {
			return sitelPreparedImage{}, errors.New("image extends outside the advertised Sitel area")
		}
	}
	version := ""
	switch target {
	case 27:
		header, err := readSitelImage(segments, area.Address, 34)
		if err != nil {
			return sitelPreparedImage{}, err
		}
		if binary.LittleEndian.Uint32(header[30:]) != info.ID {
			return sitelPreparedImage{}, errors.New("sitel tune image device ID mismatch")
		}
		version = string(bytesBeforeNUL(header[11:21]))
	case 3, 29:
		offset := info.ImageInfoOffset
		deviceID := info.ID
		if target == 29 {
			offset = 0x400
			deviceID = 0x0b0e4032
		}
		if uint64(area.Address)+uint64(offset)+16 > 1<<32 {
			return sitelPreparedImage{}, errors.New("sitel header address overflow")
		}
		headerAddress := area.Address + offset
		header, err := readSitelImage(segments, headerAddress, 16)
		if err != nil {
			return sitelPreparedImage{}, err
		}
		var virtualOffset uint32
		first := binary.LittleEndian.Uint32(header)
		if target == 3 && first > 0x10000000 && first < 0x60000000 {
			if uint64(headerAddress)+40 > 1<<32 {
				return sitelPreparedImage{}, errors.New("sitel virtual header address overflow")
			}
			extra, err := readSitelImage(segments, headerAddress+32, 8)
			if err != nil || binary.LittleEndian.Uint32(extra) != 0x0f0db01d {
				return sitelPreparedImage{}, errors.New("invalid Sitel virtual-address header")
			}
			virtualOffset = binary.LittleEndian.Uint32(extra[4:])
		}
		pointer := func(raw uint32) (uint32, error) {
			value := uint64(raw)
			if target == 29 {
				if raw < 0x08005000 {
					return 0, errors.New("invalid controller runtime pointer")
				}
				value = uint64(area.Address) + uint64(raw-0x08005000)
			} else if raw > 0x10000000 && raw < 0x60000000 {
				value = uint64(raw&0x03ffffff) + uint64(virtualOffset)
			}
			if value < uint64(area.Address) || value >= uint64(area.Address)+uint64(area.Size) {
				return 0, errors.New("sitel image pointer outside area")
			}
			return uint32(value), nil
		}
		idAddress, err := pointer(first)
		if err != nil {
			return sitelPreparedImage{}, err
		}
		id, err := readSitelImage(segments, idAddress, 4)
		if err != nil || binary.LittleEndian.Uint32(id) != deviceID {
			return sitelPreparedImage{}, errors.New("sitel image device ID mismatch")
		}
		versionAddress, err := pointer(binary.LittleEndian.Uint32(header[12:]))
		if err != nil {
			return sitelPreparedImage{}, err
		}
		version, err = readSitelCString(segments, versionAddress)
		if err != nil {
			return sitelPreparedImage{}, err
		}
	default:
		return sitelPreparedImage{}, errors.New("unimplemented Sitel image target")
	}
	if version != wanted {
		return sitelPreparedImage{}, fmt.Errorf("sitel image version %q does not match release %q", version, wanted)
	}
	return sitelPreparedImage{Target: target, Area: area, Segments: segments, Version: version}, nil
}

func sitelCRC(data []byte, legacy bool) uint16 {
	crc := uint16(0xffff)
	for _, b := range data {
		index := byte(crc>>8) ^ b
		if legacy {
			index = byte(crc) ^ b
		}
		value := uint16(index) << 8
		for bit := 0; bit < 8; bit++ {
			if value&0x8000 != 0 {
				value = value<<1 ^ 0x1021
			} else {
				value <<= 1
			}
		}
		if legacy {
			crc = crc>>8 ^ value
		} else {
			crc = crc<<8 ^ value
		}
	}
	return crc
}
