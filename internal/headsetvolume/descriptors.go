package headsetvolume

import (
	"encoding/binary"
	"fmt"
)

type Descriptor struct {
	Interface        byte
	Channels         byte
	AdvertisedVolume bool
}

// DescriptorError contains only parser reasons and numeric USB descriptor fields.
// It is safe to include in a debug report without exposing paths or device names.
type DescriptorError struct{ detail string }

func (e *DescriptorError) Error() string { return e.detail }

func descriptorError(format string, args ...any) error {
	return &DescriptorError{detail: fmt.Sprintf(format, args...)}
}

// Require the exact firmware's Audio 1 playback chain: USB input 1, mixer 8,
// feature unit 2, and headset output 3. Mixer 8 also takes microphone monitoring
// from feature 7. The volume bit may be absent; the exact firmware profile,
// rather than that descriptor bit, establishes the endpoint compatibility route.
func Inspect(data []byte) (Descriptor, error) {
	var result Descriptor
	if len(data) < 18 || data[0] != 18 || data[1] != 1 || binary.LittleEndian.Uint16(data[8:]) != VendorID || binary.LittleEndian.Uint16(data[12:]) != 0x0111 || data[17] != 1 {
		return result, descriptorError("not the validated Evolve2 30 SE USB firmware")
	}
	profile, ok := profileForPID(binary.LittleEndian.Uint16(data[10:]))
	if !ok {
		return result, descriptorError("no validated headset volume descriptor profile for this model")
	}
	if len(data) < 27 || data[18] != 9 || data[19] != 2 || int(binary.LittleEndian.Uint16(data[20:])) != len(data)-18 {
		return result, descriptorError("invalid or multiple USB configurations")
	}
	active, found, header := false, false, false
	controlLength, expectedControlLength := 0, 0
	units := map[byte][]byte{}
	for offset := 27; offset < len(data); {
		if offset+2 > len(data) || data[offset] < 2 || offset+int(data[offset]) > len(data) {
			return result, descriptorError("truncated USB descriptor")
		}
		item := data[offset : offset+int(data[offset])]
		offset += len(item)
		if item[1] == 4 {
			if len(item) != 9 {
				return result, descriptorError("invalid USB interface descriptor")
			}
			active = item[5] == 1 && item[6] == 1
			if active {
				if found || item[3] != 0 || item[7] != 0 {
					return result, descriptorError("unsupported or ambiguous USB audio control interface")
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
			return result, descriptorError("short audio control descriptor")
		}
		controlLength += len(item)
		if item[2] >= 2 && item[2] <= 8 {
			if len(item) < 4 || units[item[3]] != nil {
				return result, descriptorError("ambiguous audio unit identity")
			}
			units[item[3]] = item
		}
		if item[2] == 1 {
			if header || len(item) < 8 || len(item) != 8+int(item[7]) || binary.LittleEndian.Uint16(item[3:]) != 0x0100 {
				return result, descriptorError("headset volume requires USB Audio 1")
			}
			header = true
			expectedControlLength = int(binary.LittleEndian.Uint16(item[5:]))
		}
	}
	if !header || controlLength != expectedControlLength {
		return result, descriptorError("headset audio control descriptors are incomplete")
	}
	input := units[1]
	if len(input) != 12 || input[2] != 2 || binary.LittleEndian.Uint16(input[4:]) != 0x0101 || input[7] != profile.channels {
		return result, descriptorError("unexpected USB playback terminal 1; expected %d channels for this model", profile.channels)
	}
	result.Channels = input[7]
	feature := units[2]
	if len(feature) != 8+int(result.Channels) || feature[2] != 6 || feature[4] != 8 || feature[5] != 1 || feature[6]&1 == 0 {
		return result, descriptorError("unexpected headset playback feature unit 2; expected source mixer 8 and master mute")
	}
	mixer := units[8]
	if len(mixer) != 13 || mixer[2] != 4 || mixer[4] != 2 || mixer[5] != 1 || mixer[6] != 7 || mixer[7] != result.Channels {
		return result, descriptorError("unexpected headset mixer 8; expected USB input 1 and monitor feature 7")
	}
	monitor, microphone := units[7], units[10]
	if len(monitor) != 9 || monitor[2] != 6 || monitor[4] != 10 || monitor[5] != 1 || len(microphone) != 12 || microphone[2] != 2 || binary.LittleEndian.Uint16(microphone[4:]) != 0x0201 || microphone[7] != 2 {
		return result, descriptorError("unexpected headset monitor path; expected microphone 10 through feature 7")
	}
	output := units[3]
	if len(output) != 9 || output[2] != 3 || binary.LittleEndian.Uint16(output[4:]) != profile.outputTerminal || output[7] != 2 {
		return result, descriptorError("unexpected headset output terminal 3; expected type %04x from feature 2", profile.outputTerminal)
	}
	result.AdvertisedVolume = feature[6]&2 != 0
	return result, nil
}
