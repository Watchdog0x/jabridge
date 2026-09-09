package buttons

import (
	"github.com/Watchdog0x/jabridge/internal/firmware"
	"github.com/Watchdog0x/jabridge/internal/gnpevents"
)

type InputDecoder struct {
	buttons  *Decoder
	gnp      *firmware.ControlPacketAssembler
	Controls []Control
}

func NewInputDecoder(pid, bus uint16, layouts []firmware.HIDReport) *InputDecoder {
	buttons := NewDecoder(pid, bus, layouts)
	d := &InputDecoder{buttons: buttons, Controls: buttons.Controls}
	if layout, err := firmware.SelectControlLayout(layouts); err == nil {
		d.gnp = firmware.NewControlPacketAssembler(layout)
	}
	return d
}

func (d *InputDecoder) ObservesGNP() bool { return d.gnp != nil }

func (d *InputDecoder) Decode(packet []byte) ([]Edge, *gnpevents.Signal) {
	edges := d.buttons.Decode(packet)
	if d.gnp == nil {
		return edges, nil
	}
	canonical, err := d.gnp.Push(packet)
	if err != nil || canonical == nil {
		return edges, nil
	}
	if signal, ok := gnpevents.Decode(canonical); ok {
		return edges, &signal
	}
	return edges, nil
}
