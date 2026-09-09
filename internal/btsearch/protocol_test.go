package btsearch

import "testing"

func found(name string, last byte) []byte {
	return append([]byte{5, 0, 1, 0, byte(14 + len(name)), 0x0d, 0x2b, 0, 2, 0, 0, 0, 0, last, byte(len(name))}, []byte(name)...)
}

func TestDiscoveryResultsAndCompletion(t *testing.T) {
	var results Results
	for _, packet := range [][]byte{found("Office", 1), found("Renamed", 1), found("Travel", 2), {5, 0, 1, 0, 6, 0x0d, 0x23}, found("Late", 3)} {
		event, relevant, err := Decode(packet, 1)
		if err != nil || !relevant {
			t.Fatal(event, relevant, err)
		}
		if err := results.Apply(event); err != nil {
			t.Fatal(err)
		}
	}
	if !results.Complete || len(results.Devices) != 2 || results.Devices[0].Name != "Renamed" || results.Devices[1].Name != "Travel" {
		t.Fatal(results)
	}
}

func TestDiscoveryIgnoresOtherTraffic(t *testing.T) {
	for _, offset := range []int{0, 1, 2, 4, 5, 6} {
		packet := found("Office", 1)
		packet[offset] = 0xff
		if _, relevant, err := Decode(packet, 1); err != nil || relevant {
			t.Fatalf("offset %d: %v %v", offset, relevant, err)
		}
	}
}

func TestDiscoveryRejectsMalformedAndSanitizesNames(t *testing.T) {
	packet := found("Office", 1)
	for _, bad := range [][]byte{packet[:len(packet)-1], append(append([]byte(nil), packet...), 0)} {
		if _, _, err := Decode(bad, 1); err == nil {
			t.Fatal("accepted wrong length")
		}
	}
	packet = found("Office", 1)
	packet[14] = 0
	if event, _, err := Decode(packet, 1); err != nil || event.Device.Name != "Office" {
		t.Fatal("opaque byte incorrectly treated as name length", event, err)
	}
	event, _, err := Decode(found("A\x1b\nB", 1), 1)
	if err != nil || event.Device.Name != "AB" {
		t.Fatal(event, err)
	}
}

func TestDiscoveryResultLimit(t *testing.T) {
	var results Results
	for i := 1; i <= MaxResults; i++ {
		if err := results.Apply(Event{Device: Device{Address: [6]byte{2, 0, 0, 0, 0, byte(i)}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := results.Apply(Event{Device: Device{Address: [6]byte{2, 1}}}); err == nil {
		t.Fatal("unbounded results")
	}
}
