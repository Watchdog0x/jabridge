// Package buttons observes descriptor-described Jabra controls. It never
// writes HID reports, grabs an input device, or changes headset settings.
package buttons

import (
	"sort"

	"github.com/Watchdog0x/jabridge/internal/firmware"
)

type Control struct {
	Page          uint32 `json:"usagePage"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Report        byte   `json:"report"`
	Usage         uint32 `json:"usage"`
	Constant      bool   `json:"declaredConstant"`
	MusicEligible bool   `json:"musicEligible"`
}

type Edge struct {
	Control
	Pressed bool `json:"pressed"`
	Initial bool `json:"initial,omitempty"`
}

type field struct {
	Control
	bit uint64
}
type report struct {
	bytes    int
	fields   []field
	previous map[string]bool
}
type Decoder struct {
	reports  map[byte]*report
	numbered bool
	Controls []Control
}

func NewDecoder(pid, bus uint16, layouts []firmware.HIDReport) *Decoder {
	d := &Decoder{reports: map[byte]*report{}}
	for _, layout := range layouts {
		if layout.Kind != "input" || layout.Bytes < 1 || layout.Bytes > 8193 {
			continue
		}
		r := &report{bytes: layout.Bytes}
		for _, f := range layout.Fields {
			// Only explicit one-bit variable controls. Arrays, vendor payloads,
			// relative axes and padding are not guessed into physical buttons.
			if f.SizeBits != 1 || f.Count > 256 || f.Flags&2 == 0 || f.Flags&4 != 0 && f.Flags&1 == 0 || f.LogicalMin != 0 || f.LogicalMax != 1 {
				continue
			}
			for i := uint32(0); i < f.Count; i++ {
				usage, ok := fieldUsage(f, i)
				if !ok {
					continue
				}
				page := f.UsagePage
				if usage > 65535 {
					page, usage = usage>>16, usage&65535
				}
				name := ControlName(page, usage)
				kind := "button"
				if page == 0x0b && usage == 0x2f {
					name = "microphone-mute"
					kind = "switch"
				}
				if page == 0x0b && usage == 0x20 {
					name = "hook-switch"
					kind = "switch"
				}
				if name == "" {
					continue
				}
				constant := f.Flags&1 != 0
				eligible := bus == 3 && (pid == 0x24c7 || pid == 0x24c8) && f.Flags == 7 && (usage == 0xb0 || usage == 0xb1)
				bit := f.OffsetBits + uint64(i)
				payloadBytes := layout.Bytes
				if layout.ID != 0 {
					payloadBytes--
				}
				if bit >= uint64(payloadBytes)*8 {
					continue
				}
				control := Control{Page: page, Kind: kind, Name: name, Report: layout.ID, Usage: usage, Constant: constant, MusicEligible: eligible}
				r.fields = append(r.fields, field{Control: control, bit: bit})
				d.Controls = append(d.Controls, control)
			}
		}
		if len(r.fields) > 0 {
			d.reports[layout.ID] = r
			d.numbered = d.numbered || layout.ID != 0
		}
	}
	return d
}

func fieldUsage(f firmware.HIDField, index uint32) (uint32, bool) {
	if int(index) < len(f.Usages) {
		return f.Usages[index], true
	}
	if len(f.Usages) != 0 {
		return 0, false
	} // no repeated last usage for ambiguous padding
	if f.UsageMin != 0 && uint64(f.UsageMin)+uint64(index) <= uint64(f.UsageMax) {
		return f.UsageMin + index, true
	}
	return 0, false
}

func consumerName(page, usage uint32) string {
	if page != 0x0c {
		return ""
	}
	return map[uint32]string{0xb0: "play", 0xb1: "pause", 0xcd: "play-pause", 0xb7: "stop", 0xb5: "next", 0xb6: "previous", 0xe9: "volume-up", 0xea: "volume-down", 0xe2: "mute"}[usage]
}

func ControlName(page, usage uint32) string {
	if page == 0x0b {
		switch usage {
		case 0x2f:
			return "microphone-mute"
		case 0x20:
			return "hook-switch"
		}
	}
	return consumerName(page, usage)
}

// Decode establishes a baseline without generating a press. Repeated reports
// and held bits generate no new edge. Explicit Play and Pause remain distinct;
// a pause signal must never resume an already paused media player.
func (d *Decoder) Decode(packet []byte) []Edge {
	if len(packet) == 0 {
		return nil
	}
	id, prefix := byte(0), 0
	if d.numbered {
		id, prefix = packet[0], 1
	}
	r := d.reports[id]
	if r == nil || len(packet) != r.bytes {
		return nil
	}
	values, controls := map[string]bool{}, map[string]Control{}
	for _, f := range r.fields {
		key := f.Name
		if f.MusicEligible {
			key += ":quirk"
		} else if f.Constant {
			key += ":constant"
		}
		values[key] = values[key] || packet[prefix+int(f.bit/8)]&(1<<uint(f.bit%8)) != 0
		if _, ok := controls[key]; !ok {
			controls[key] = f.Control
		}
	}
	var edges []Edge
	for name, pressed := range values {
		if r.previous != nil && r.previous[name] != pressed || r.previous == nil && controls[name].Kind == "switch" {
			control := controls[name]
			// If a normal Linux media field also fires, the desktop owns it.
			if control.MusicEligible && (values["play-pause"] || values["play"] || values["pause"] || values["play:quirk"] && values["pause:quirk"]) {
				control.MusicEligible = false
			}
			edges = append(edges, Edge{Control: control, Pressed: pressed, Initial: r.previous == nil})
		}
	}
	r.previous = values
	sort.Slice(edges, func(i, j int) bool { return edges[i].Name < edges[j].Name })
	return edges
}
