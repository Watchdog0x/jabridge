// Package btsearch decodes bounded Link discovery events. It does not open USB
// devices or start discovery; that work belongs to the background service.
package btsearch

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxResults = 64

func StartCommand() []byte { return []byte{0x20, 0x05, 20} }
func StopCommand() []byte  { return []byte{0x21, 0x01} }

type Device struct {
	Name          string
	Address       [6]byte
	BluetoothType byte
}

type Event struct {
	Complete bool
	Device   Device
}

// Decode accepts normalized GNP packets including canonical report ID 5.
// Other addresses, replies and event classes are not discovery results.
func Decode(packet []byte, address byte) (Event, bool, error) {
	if len(packet) < 7 || packet[0] != 5 || packet[1] != 0 || packet[2] != address || packet[4]&0xc0 != 0 || packet[5] != 0x0d {
		return Event{}, false, nil
	}
	if packet[6] != 0x2b && packet[6] != 0x23 {
		return Event{}, false, nil
	}
	if int(packet[4]&0x3f)+1 != len(packet) || len(packet) > 64 {
		return Event{}, true, errors.New("invalid discovery packet length")
	}
	if packet[6] == 0x23 {
		return Event{Complete: true}, true, nil
	}
	// The legacy decoder takes the name from offset 15 to the bounded packet
	// end. It does not interpret the byte before it as an authoritative length.
	if len(packet) < 15 || !utf8.Valid(packet[15:]) {
		return Event{}, true, errors.New("invalid discovery name bounds or encoding")
	}
	device := Device{BluetoothType: packet[7]}
	copy(device.Address[:], packet[8:14])
	if device.Address == [6]byte{} {
		return Event{}, true, errors.New("discovery result has no address")
	}
	device.Name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, string(packet[15:])))
	if device.Name == "" {
		device.Name = "Unnamed Bluetooth device"
	}
	return Event{Device: device}, true, nil
}

// Results keeps stable insertion order and deduplicates by address. Repeated
// observations update the name, not the index used by the current search UI.
type Results struct {
	Devices  []Device
	Complete bool
}

func (r *Results) Apply(event Event) error {
	if r.Complete {
		return nil
	}
	if event.Complete {
		r.Complete = true
		return nil
	}
	for index, device := range r.Devices {
		if device.Address == event.Device.Address {
			r.Devices[index] = event.Device
			return nil
		}
	}
	if len(r.Devices) >= MaxResults {
		return errors.New("discovery result limit reached")
	}
	r.Devices = append(r.Devices, event.Device)
	return nil
}
