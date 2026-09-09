package daemon

import (
	"sort"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

// Numeric device IDs can be reused, including for children behind a dongle
// which remains connected to USB. Use the registry's existing binding fields.
func deviceIdentityChanged(old, current ipc.DeviceInfo) bool {
	return old.Instance != current.Instance || old.PID != current.PID ||
		old.Connection != current.Connection || old.ParentID != current.ParentID ||
		old.Topology != current.Topology
}

func (d *Daemon) publishDeviceState(previous, current map[uint16]ipc.DeviceInfo) {
	if d.buttons != nil {
		d.buttons.InvalidateSignalsForProducts(changedControlProducts(previous, current))
	}
	publishDeviceChanges(d.events, previous, current)
}

// A source is currently identified by its USB PID, not a private device path.
// Clearing all matching interfaces is conservative when identical adapters
// coexist. No new headset identity is inferred from a routing endpoint.
func changedControlProducts(previous, current map[uint16]ipc.DeviceInfo) []uint16 {
	products := map[uint16]bool{}
	add := func(device ipc.DeviceInfo, snapshot map[uint16]ipc.DeviceInfo) {
		if device.Connection == "dongle" {
			if parent, ok := snapshot[device.ParentID]; ok && parent.IsDongle && parent.Connection == "usb" {
				products[parent.PID] = true
			}
			return
		}
		if device.Connection == "usb" {
			products[device.PID] = true
		}
	}
	for id, device := range previous {
		other, ok := current[id]
		if !ok || deviceIdentityChanged(device, other) {
			add(device, previous)
		}
	}
	for id, device := range current {
		other, ok := previous[id]
		if !ok || deviceIdentityChanged(other, device) {
			add(device, current)
		}
	}
	result := make([]uint16, 0, len(products))
	for pid := range products {
		result = append(result, pid)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
