package firmware

import (
	"fmt"
	"sort"
)

// HIDReport describes sizes only; it contains no device input or identity.
type HIDReport struct {
	ID    byte
	Kind  string
	Bytes int
}

func InspectHIDReports(path string) ([]HIDReport, error) {
	descriptor, err := readHidrawReportDescriptor(path)
	if err != nil {
		return nil, err
	}
	return parseHIDReports(descriptor)
}

func parseHIDReports(descriptor []byte) ([]HIDReport, error) {
	type globals struct {
		id          byte
		size, count uint32
	}
	type key struct {
		id   byte
		kind string
	}
	state := globals{}
	var stack []globals
	bits := map[key]uint64{}
	for offset := 0; offset < len(descriptor); {
		prefix := descriptor[offset]
		offset++
		if prefix == 0xfe {
			if offset+2 > len(descriptor) {
				return nil, fmt.Errorf("truncated long HID item")
			}
			length := int(descriptor[offset])
			offset += 2
			if offset+length > len(descriptor) {
				return nil, fmt.Errorf("truncated long HID payload")
			}
			offset += length
			continue
		}
		length := int(prefix & 3)
		if length == 3 {
			length = 4
		}
		if offset+length > len(descriptor) {
			return nil, fmt.Errorf("truncated HID item")
		}
		var value uint32
		for i := 0; i < length; i++ {
			value |= uint32(descriptor[offset+i]) << (8 * i)
		}
		offset += length
		kind, tag := (prefix>>2)&3, prefix>>4
		if kind == 1 {
			switch tag {
			case 7:
				state.size = value
			case 8:
				if value == 0 || value > 255 {
					return nil, fmt.Errorf("invalid HID report ID")
				}
				state.id = byte(value)
			case 9:
				state.count = value
			case 10:
				stack = append(stack, state)
			case 11:
				if len(stack) == 0 {
					return nil, fmt.Errorf("unbalanced HID global pop")
				}
				state = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			}
		} else if kind == 0 {
			name := map[byte]string{8: "input", 9: "output", 11: "feature"}[tag]
			if name == "" {
				continue
			}
			k := key{state.id, name}
			count := uint64(state.size) * uint64(state.count)
			if count > 65536 || bits[k]+count > 65536 {
				return nil, fmt.Errorf("HID report exceeds supported size")
			}
			bits[k] += count
		}
	}
	var reports []HIDReport
	for k, count := range bits {
		size := int((count + 7) / 8)
		if k.id != 0 {
			size++
		}
		reports = append(reports, HIDReport{ID: k.id, Kind: k.kind, Bytes: size})
	}
	sort.Slice(reports, func(i, j int) bool {
		if reports[i].ID != reports[j].ID {
			return reports[i].ID < reports[j].ID
		}
		return reports[i].Kind < reports[j].Kind
	})
	return reports, nil
}
