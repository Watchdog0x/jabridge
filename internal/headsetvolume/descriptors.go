package headsetvolume

import (
	"encoding/binary"
	"errors"
)

type Descriptor struct {
	Interface        byte
	Channels         byte
	AdvertisedVolume bool
}

// Require the exact firmware's Audio 1 playback chain: USB streaming input 1,
// feature unit 2, and speaker output 3. This also works when the live descriptor
// advertises only mute, as reported in issue 44. The firmware-specific volume
// route is enabled by profile evidence, never by an absent descriptor bit.
func Inspect(data []byte) (Descriptor, error) {
	var result Descriptor
	if len(data) < 18 || data[0] != 18 || data[1] != 1 || binary.LittleEndian.Uint16(data[8:]) != VendorID || binary.LittleEndian.Uint16(data[10:]) != ProductID || binary.LittleEndian.Uint16(data[12:]) != 0x0111 || data[17] != 1 {
		return result, errors.New("not the validated Evolve2 30 SE USB firmware")
	}
	if len(data) < 27 || data[18] != 9 || data[19] != 2 || int(binary.LittleEndian.Uint16(data[20:])) != len(data)-18 {
		return result, errors.New("invalid or multiple USB configurations")
	}
	active, found, header, input, feature, output := false, false, false, false, false, false
	featureLength, controlLength, expectedControlLength := 0, 0, 0
	seen := map[byte]bool{}
	for offset := 27; offset < len(data); {
		if offset+2 > len(data) || data[offset] < 2 || offset+int(data[offset]) > len(data) {
			return result, errors.New("truncated USB descriptor")
		}
		item := data[offset : offset+int(data[offset])]
		offset += len(item)
		if item[1] == 4 {
			if len(item) != 9 {
				return result, errors.New("invalid USB interface descriptor")
			}
			active = item[5] == 1 && item[6] == 1
			if active {
				if found || item[3] != 0 || item[7] != 0 {
					return result, errors.New("unsupported or ambiguous USB audio control interface")
				}
				found = true
				result.Interface = item[2]
			}
			continue
		}
		if !active || item[1] != 0x24 {
			continue
		}
		if len(item) < 3 {
			return result, errors.New("short audio control descriptor")
		}
		controlLength += len(item)
		if item[2] >= 2 && item[2] <= 8 {
			if len(item) < 4 || seen[item[3]] {
				return result, errors.New("ambiguous audio unit identity")
			}
			seen[item[3]] = true
		}
		switch item[2] {
		case 1:
			if header || len(item) < 8 || len(item) != 8+int(item[7]) || binary.LittleEndian.Uint16(item[3:]) != 0x0100 {
				return result, errors.New("headset volume requires USB Audio 1")
			}
			header = true
			expectedControlLength = int(binary.LittleEndian.Uint16(item[5:]))
		case 2, 3, 6:
			if item[3] == 1 {
				if item[2] != 2 || len(item) != 12 || binary.LittleEndian.Uint16(item[4:]) != 0x0101 || item[7] < 1 || item[7] > 2 {
					return result, errors.New("unexpected USB playback terminal")
				}
				input = true
				result.Channels = item[7]
			}
			if item[3] == 2 {
				if item[2] != 6 || len(item) < 8 || item[4] != 1 || item[5] != 1 || item[6]&1 == 0 {
					return result, errors.New("unexpected headset playback feature unit")
				}
				feature = true
				featureLength = len(item)
				result.AdvertisedVolume = item[6]&2 != 0
			}
			if item[3] == 3 {
				if item[2] != 3 || len(item) != 9 || binary.LittleEndian.Uint16(item[4:]) != 0x0301 || item[7] != 2 {
					return result, errors.New("unexpected headset speaker terminal")
				}
				output = true
			}
		}
	}
	if !header || !input || !feature || !output || featureLength != 8+int(result.Channels) || controlLength != expectedControlLength {
		return result, errors.New("headset playback volume path is incomplete")
	}
	return result, nil
}
