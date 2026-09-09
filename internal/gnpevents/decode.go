// Package gnpevents decodes a bounded set of passive Jabra GNP events.
// It never sends commands, subscribes to streams, or takes button focus.
package gnpevents

import (
	"strconv"
	"strings"
)

// Signal describes observed protocol data, not a claim about sensor hardware.
// All fields are scalar, and unknown payloads or identifying strings are omitted.
type Signal struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Value      string `json:"value"`
	Endpoint   byte   `json:"endpoint"`
	Class      byte   `json:"class"`
	Opcode     byte   `json:"opcode"`
	Code       byte   `json:"code"`
	Charging   bool   `json:"charging,omitempty"`
	BatteryLow bool   `json:"batteryLow,omitempty"`
}

// Decode accepts canonical report 05 (after descriptor-driven reassembly).
// Layouts follow Jabra's public property definitions and Remote MMI SDK.
// Responses, ACKs, queries, commands and unknown/malformed events are ignored.
func Decode(packet []byte) (Signal, bool) {
	if len(packet) < 7 || packet[0] != 5 || packet[1] != 0 || packet[4]&0xc0 != 0 {
		return Signal{}, false
	}
	length := int(packet[4] & 0x3f)
	if length < 6 || length+1 > len(packet) {
		return Signal{}, false
	}
	data := packet[7 : length+1]
	s := Signal{Endpoint: packet[2], Class: packet[5], Opcode: packet[6]}
	switch {
	case s.Class == 0x12 && s.Opcode == 2:
		if len(data) < 2 || data[1] > 100 {
			return Signal{}, false
		}
		s.Kind, s.Name, s.Value = "state", "battery", strconv.Itoa(int(data[1]))
		s.Charging, s.BatteryLow = data[0]&1 != 0, data[0]&2 != 0
	case s.Class == 0x0d && s.Opcode == 0x4c:
		if len(data) < 2 {
			return Signal{}, false
		}
		s.Kind, s.Code = "state", data[0]
		switch data[0] {
		case 2:
			if len(data) != 2 || data[1] > 3 {
				return Signal{}, false
			}
			s.Name = "wear-left"
			if data[1] >= 2 {
				s.Name = "wear-right"
			}
			s.Value = "on"
			if data[1]&1 != 0 {
				s.Value = "off"
			}
		case 6:
			if len(data) != 2 || data[1] > 2 {
				return Signal{}, false
			}
			s.Name, s.Value = "boom-position", []string{"muted", "unmuted", "parked"}[data[1]]
		case 9:
			if len(data) != 3 || data[1] != 1 {
				return Signal{}, false
			}
			s.Name, s.Value = "noise-control", map[byte]string{1: "off", 2: "hearthrough", 4: "anc"}[data[2]]
			if s.Value == "" {
				return Signal{}, false
			}
		default:
			return Signal{}, false
		}
	case s.Class == 0x23 && (s.Opcode == 1 || s.Opcode == 2):
		if len(data) != 2 {
			return Signal{}, false
		}
		s.Kind, s.Code = "state", data[0]
		if s.Opcode == 1 {
			if data[1] > 1 {
				return Signal{}, false
			}
			s.Name = map[byte]string{0: "microphone-muted", 1: "microphone-position-optimal", 3: "microphone-ok", 8: "customer-speaking", 9: "agent-speaking"}[data[0]]
			s.Value = strconv.FormatBool(data[1] == 1)
		} else {
			s.Name = map[byte]string{0: "audio-exposure", 3: "background-noise"}[data[0]]
			s.Value = strconv.Itoa(int(data[1]))
		}
		if s.Name == "" {
			return Signal{}, false
		}
	case s.Class == 0x24 && s.Opcode == 4:
		if len(data) != 3 || data[1] > 20 || data[2] == 0 || data[2]&0x80 != 0 {
			return Signal{}, false
		}
		s.Kind, s.Code = "interaction", data[1]
		s.Name = []string{"multifunction", "volume-up", "volume-down", "voice-control", "app", "track-forward", "track-back", "play", "mute", "hook-off", "hook-on", "bluetooth", "jabra", "battery-button", "programmable", "link", "anc", "listen-in", "three-dot", "status-button", "media"}[data[1]]
		var actions []string
		for bit, name := range []string{"up", "down", "tap", "double-tap", "press", "long-press", "extra-long-press"} {
			if data[2]&(1<<uint(bit)) != 0 {
				actions = append(actions, name)
			}
		}
		s.Value = strings.Join(actions, "+")
	case s.Class == 0x1a && s.Opcode == 2:
		if len(data) != 2 || data[1] > 2 {
			return Signal{}, false
		}
		s.Kind, s.Name, s.Code = "interaction", "legacy-button", data[0]
		s.Value = []string{"tap", "press", "double-tap"}[data[1]]
	default:
		return Signal{}, false
	}
	return s, true
}
