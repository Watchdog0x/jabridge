package daemon

import (
	"reflect"
	"testing"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

func TestDeviceIdentityReplacementReusingIDIsAnnounced(t *testing.T) {
	old := ipc.DeviceInfo{ID: 7, Instance: "old", PID: 0x24a3, ParentID: 2, Connection: "dongle"}
	for _, test := range []struct {
		name   string
		change func(*ipc.DeviceInfo)
	}{
		{"instance", func(d *ipc.DeviceInfo) { d.Instance = "replacement" }},
		{"product", func(d *ipc.DeviceInfo) { d.PID = 0x253d }},
		{"parent", func(d *ipc.DeviceInfo) { d.ParentID = 3 }},
		{"connection", func(d *ipc.DeviceInfo) { d.Connection = "usb" }},
		{"topology", func(d *ipc.DeviceInfo) { d.Topology = "changed-parts" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := old
			test.change(&current)
			bus := ipc.NewEventBus()
			events, cancel := bus.Subscribe()
			defer cancel()
			publishDeviceChanges(bus, map[uint16]ipc.DeviceInfo{7: old}, map[uint16]ipc.DeviceInfo{7: current})
			for _, want := range []string{"device.detached", "device.attached"} {
				select {
				case got := <-events:
					if got.Method != want {
						t.Fatalf("got %s, want %s", got.Method, want)
					}
				default:
					t.Fatalf("missing %s for replacement with reused ID", want)
				}
			}
		})
	}
}

func TestDeviceIdentityControlInvalidationTargets(t *testing.T) {
	parent := ipc.DeviceInfo{ID: 0, Instance: "adapter", PID: 0x24c7, IsDongle: true, Connection: "usb"}
	child := ipc.DeviceInfo{ID: 7, Instance: "child-a", PID: 0x24a3, ParentID: 0, Connection: "dongle"}
	for _, tc := range []struct {
		name    string
		current []ipc.DeviceInfo
		want    []uint16
	}{
		{"stable", []ipc.DeviceInfo{parent, child}, []uint16{}},
		{"child removed", []ipc.DeviceInfo{parent}, []uint16{0x24c7}},
		{"child same PID replaced", []ipc.DeviceInfo{parent, {ID: 7, Instance: "child-b", PID: 0x24a3, ParentID: 0, Connection: "dongle"}}, []uint16{0x24c7}},
		{"battery only", []ipc.DeviceInfo{parent, {ID: 7, Instance: "child-a", PID: 0x24a3, ParentID: 0, Connection: "dongle", Battery: &ipc.BatteryInfo{Level: 73}}}, []uint16{}},
		{"USB removed", nil, []uint16{0x24c7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := changedControlProducts(indexDevices([]ipc.DeviceInfo{parent, child}), indexDevices(tc.current))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %x want %x", got, tc.want)
			}
		})
	}
	for _, current := range [][]ipc.DeviceInfo{{child}, {{ID: 0, PID: 0x0422, Connection: "usb"}, child}} {
		old := indexDevices(current)
		next := indexDevices(current)
		replacement := next[7]
		replacement.Instance = "new"
		next[7] = replacement
		if got := changedControlProducts(old, next); len(got) != 0 {
			t.Fatal("invented parent binding", got)
		}
	}
}

func TestDeviceIdentityBatteryUpdateDoesNotReattach(t *testing.T) {
	old := ipc.DeviceInfo{ID: 7, Instance: "same", PID: 0x24a3, Connection: "dongle", Battery: &ipc.BatteryInfo{Level: 73}}
	current := old
	current.Battery = &ipc.BatteryInfo{Level: 72}
	bus := ipc.NewEventBus()
	events, cancel := bus.Subscribe()
	defer cancel()
	publishDeviceChanges(bus, map[uint16]ipc.DeviceInfo{7: old}, map[uint16]ipc.DeviceInfo{7: current})
	select {
	case e := <-events:
		if e.Method != "device.battery.update" {
			t.Fatal(e.Method)
		}
	default:
		t.Fatal("missing battery event")
	}
	select {
	case e := <-events:
		t.Fatal("unexpected lifecycle event", e.Method)
	default:
	}
}
