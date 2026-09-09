package buttons

import (
	"testing"

	"github.com/Watchdog0x/jabridge/internal/firmware"
)

func mediaLayout(id byte, offset uint64) []firmware.HIDReport {
	return []firmware.HIDReport{{ID: id, Kind: "input", Bytes: 1 + int((offset+2+7)/8), Fields: []firmware.HIDField{{OffsetBits: offset, SizeBits: 1, Count: 2, UsagePage: 0x0c, Usages: []uint32{0xb1, 0xb0}, Flags: 7, LogicalMax: 1}}}}
}

func TestConstantMediaQuirkUsesDescriptorNotFixedReportOrBit(t *testing.T) {
	for _, offset := range []uint64{0, 9, 21} {
		layouts := mediaLayout(9, offset)
		d := NewDecoder(0x24c7, 3, layouts)
		packet := make([]byte, layouts[0].Bytes)
		packet[0] = 9
		if edges := d.Decode(packet); len(edges) != 0 {
			t.Fatal("baseline emitted", edges)
		}
		packet[1+offset/8] |= 1 << uint(offset%8)
		edges := d.Decode(packet)
		if len(edges) != 1 || !edges[0].Pressed || !edges[0].MusicEligible || edges[0].Name != "pause" {
			t.Fatal(edges)
		}
		if len(d.Decode(packet)) != 0 {
			t.Fatal("held press repeated")
		}
		packet[1+(offset+1)/8] |= 1 << uint((offset+1)%8)
		for _, edge := range d.Decode(packet) {
			if edge.MusicEligible {
				t.Fatal("simultaneous contradictory Play/Pause became an action")
			}
		}
		for i := 1; i < len(packet); i++ {
			packet[i] = 0
		}
		edges = d.Decode(packet)
		if len(edges) != 2 || edges[0].Pressed || edges[1].Pressed {
			t.Fatal(edges)
		}
	}
}

func TestUnknownModelsAndBluetoothNeverQualifyForMusicActions(t *testing.T) {
	for _, identity := range [][2]uint16{{0x4052, 3}, {0x0422, 3}, {0x24c7, 5}, {0x24b7, 3}} {
		d := NewDecoder(identity[0], identity[1], mediaLayout(1, 0))
		d.Decode([]byte{1, 0})
		edges := d.Decode([]byte{1, 1})
		if len(edges) != 1 || edges[0].MusicEligible {
			t.Fatal(identity, edges)
		}
	}
	layouts := mediaLayout(1, 0)
	layouts[0].Fields[0].Flags = 3
	d := NewDecoder(0x24c7, 3, layouts)
	d.Decode([]byte{1, 0})
	for _, edge := range d.Decode([]byte{1, 1}) {
		if edge.MusicEligible {
			t.Fatal("unrecognized constant-field flags enabled music")
		}
	}
}

func TestMalformedVendorAndStandardDataDoNotTriggerQuirk(t *testing.T) {
	layouts := mediaLayout(1, 0)
	layouts[0].Fields[0].Flags = 2
	d := NewDecoder(0x24c8, 3, layouts)
	d.Decode([]byte{1, 0})
	if edges := d.Decode([]byte{1, 1}); len(edges) != 1 || edges[0].MusicEligible {
		t.Fatal(edges)
	}
	if len(d.Decode([]byte{1, 0, 0})) != 0 || len(d.Decode([]byte{3, 0})) != 0 || len(d.Decode(nil)) != 0 {
		t.Fatal("malformed report accepted")
	}
	layouts[0].Fields[0].UsagePage = 0xff00
	if len(NewDecoder(0x24c7, 3, layouts).Controls) != 0 {
		t.Fatal("vendor payload became buttons")
	}
	layouts[0].Fields[0].UsagePage = 0xc
	layouts[0].Fields[0].Flags = 0
	if len(NewDecoder(0x24c7, 3, layouts).Controls) != 0 {
		t.Fatal("array guessed into buttons")
	}
}

func TestInitialHeldButtonIsNotACommand(t *testing.T) {
	d := NewDecoder(0x24c8, 3, mediaLayout(1, 0))
	if len(d.Decode([]byte{1, 3})) != 0 {
		t.Fatal("startup toggled playback")
	}
	if edges := d.Decode([]byte{1, 0}); len(edges) != 2 || edges[0].Pressed || edges[1].Pressed {
		t.Fatal(edges)
	}
}

func TestNormalMediaFieldDoesNotHideOrDuplicateConstantQuirk(t *testing.T) {
	layouts := mediaLayout(1, 9)
	layouts[0].Fields = append([]firmware.HIDField{{OffsetBits: 3, SizeBits: 1, Count: 1, UsagePage: 0xc, Usages: []uint32{0xcd}, Flags: 2, LogicalMax: 1}}, layouts[0].Fields...)
	d := NewDecoder(0x24c7, 3, layouts)
	d.Decode([]byte{1, 0, 0})
	edges := d.Decode([]byte{1, 0, 2})
	if len(edges) != 1 || !edges[0].MusicEligible {
		t.Fatal("normal idle field hid the quirk", edges)
	}
	d.Decode([]byte{1, 0, 0})
	edges = d.Decode([]byte{1, 8, 2})
	for _, edge := range edges {
		if edge.MusicEligible {
			t.Fatal("desktop and daemon would both toggle", edges)
		}
	}
}

func FuzzButtonDecoder(f *testing.F) {
	f.Add([]byte{1, 0})
	f.Add([]byte{1, 255})
	f.Fuzz(func(t *testing.T, data []byte) {
		d := NewDecoder(0x24c7, 3, mediaLayout(1, 9))
		d.Decode(data)
		d.Decode(data)
	})
}

func TestSpeak510TelephonyMuteIsObservedNotTreatedAsMusic(t *testing.T) {
	// Shape from the supplied Speak 510 descriptor: report 3, page 0x0b,
	// PhoneMute usage 0x2f at payload bit 4. No packet is sent to hardware.
	layouts := []firmware.HIDReport{{ID: 3, Kind: "input", Bytes: 3, Fields: []firmware.HIDField{{OffsetBits: 4, SizeBits: 1, Count: 1, UsagePage: 0xb, Usages: []uint32{0x2f}, Flags: 7, LogicalMax: 1}}}}
	d := NewDecoder(0x0422, 3, layouts)
	edges := d.Decode([]byte{3, 0, 0})
	if len(edges) != 1 || !edges[0].Initial || edges[0].Pressed {
		t.Fatal(edges)
	}
	edges = d.Decode([]byte{3, 16, 0})
	if len(edges) != 1 || edges[0].Name != "microphone-mute" || edges[0].Kind != "switch" || !edges[0].Pressed || edges[0].MusicEligible || edges[0].Initial {
		t.Fatal(edges)
	}
	if len(d.Decode([]byte{3, 16, 0})) != 0 {
		t.Fatal("held mute repeated")
	}
}
