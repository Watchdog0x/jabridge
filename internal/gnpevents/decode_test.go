package gnpevents

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeDocumentedEventVectors(t *testing.T) {
	for _, tc := range []struct{ label, hex, name, value string }{
		{"battery", "050004000812020149", "battery", "73"},
		{"zero battery", "050004000812020200", "battery", "0"},
		{"left on", "05000400080d4c0200", "wear-left", "on"},
		{"left off", "05000400080d4c0201", "wear-left", "off"},
		{"right on", "05000400080d4c0202", "wear-right", "on"},
		{"right off", "05000400080d4c0203", "wear-right", "off"},
		{"boom mute", "05000400080d4c0600", "boom-position", "muted"},
		{"boom unmute", "05000400080d4c0601", "boom-position", "unmuted"},
		{"boom parked", "05000400080d4c0602", "boom-position", "parked"},
		{"ANC", "05000400090d4c090104", "noise-control", "anc"},
		{"mic mute", "050004000823010001", "microphone-muted", "true"},
		{"mic placement", "050004000823010100", "microphone-position-optimal", "false"},
		{"mic OK", "050004000823010301", "microphone-ok", "true"},
		{"agent speaking", "050004000823010901", "agent-speaking", "true"},
		{"customer quiet", "050004000823010800", "customer-speaking", "false"},
		{"noise", "050004000823020337", "background-noise", "55"},
		{"exposure", "050004000823020021", "audio-exposure", "33"},
		{"three dot tap", "05000400092404011204", "three-dot", "tap"},
		{"MMI long press", "05000400092404010720", "play", "long-press"},
		{"legacy tap", "05000400081a020100", "legacy-button", "tap"},
		{"legacy press", "05000400081a020101", "legacy-button", "press"},
		{"legacy double", "05000400081a020102", "legacy-button", "double-tap"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			packet, err := hex.DecodeString(tc.hex)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := Decode(packet)
			if !ok || got.Name != tc.name || got.Value != tc.value || got.Endpoint != 4 {
				t.Fatalf("decode=%+v accepted=%t", got, ok)
			}
			if tc.label == "battery" && (!got.Charging || got.BatteryLow) {
				t.Fatal(got)
			}
			if tc.label == "zero battery" && (got.Charging || !got.BatteryLow) {
				t.Fatal(got)
			}
			// Every proper prefix is incomplete, regardless of its contents.
			for end := 0; end < len(packet); end++ {
				if _, ok := Decode(packet[:end]); ok {
					t.Fatalf("truncated packet accepted at %d", end)
				}
			}
			for _, kind := range []byte{0x40, 0x80, 0xc0} {
				copy := append([]byte(nil), packet...)
				copy[4] |= kind
				if _, ok := Decode(copy); ok {
					t.Fatal("non-event packet accepted")
				}
			}
		})
	}
}

func TestDecodeRejectsUnknownOrInvalidSignals(t *testing.T) {
	for _, value := range []string{
		"050004000812020065",   // battery >100
		"05000400080d4c0204",   // unknown wear state
		"05000400080d4c0603",   // unknown boom state
		"05000400090d4c090180", // calibration is not an ordinary sound mode
		"0500040008230109ff",   // SDK known-bad speech payload
		"050004000823010302",   // invalid boolean
		"050004000823020155",   // unknown diagnostic
		"05000400092404011280", // unknown action bit
		"05000400092404011200", // no action
		"05000400092404017f04", // unknown button
		"05000400092405011204", // unsolicited format not established
		"05000400081a020103",   // unknown legacy action
		"050404000812020149",   // not host-bound
		"020004000812020149",   // not canonical report
		"05000400c5ff",         // ACK
	} {
		packet, err := hex.DecodeString(value)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := Decode(packet); ok {
			t.Fatalf("accepted %s: %+v", value, got)
		}
	}
	if _, ok := Decode([]byte("PRIVATE_DEVICE_NAME")); ok {
		t.Fatal("arbitrary text decoded")
	}
}

func FuzzPassiveEventDecoder(f *testing.F) {
	for _, value := range []string{"050004000812020149", "05000400092404011204", "05000400080d4c0200", "05000400c5ff"} {
		packet, _ := hex.DecodeString(value)
		f.Add(packet)
	}
	f.Fuzz(func(t *testing.T, packet []byte) {
		if len(packet) > 8193 {
			return
		}
		s, ok := Decode(packet)
		if !ok {
			return
		}
		if s.Kind != "state" && s.Kind != "interaction" {
			t.Fatal(s)
		}
		if len(s.Name) > 32 || len(s.Value) > 90 {
			t.Fatal("unbounded decoded text")
		}
		encoded, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "PRIVATE_DEVICE_NAME") {
			t.Fatal("raw payload disclosed")
		}
	})
}
