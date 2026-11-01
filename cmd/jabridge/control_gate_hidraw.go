package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var errNoControlUSBParent = errors.New("management interface has no physical USB parent")

func controlUSBParent(path string) (string, error) {
	node, err := filepath.EvalSymlinks(filepath.Join("/sys/class/hidraw", filepath.Base(path), "device"))
	if err != nil {
		return "", err
	}
	for parent := node; parent != "/" && parent != "."; parent = filepath.Dir(parent) {
		if _, err := os.Stat(filepath.Join(parent, "idVendor")); err == nil {
			if _, err := os.Stat(filepath.Join(parent, "idProduct")); err == nil {
				return parent, nil
			}
		}
	}
	return "", errNoControlUSBParent
}

func scopedControlPort(port string) bool {
	scoped, _ := controlPortClass(port)
	return scoped
}

func controlPortClass(port string) (bool, bool) {
	if !filepath.IsAbs(port) {
		return false, false
	}
	if link390ControlGate.busy(port) {
		return true, true
	}
	vid, vidErr := readHexFile(filepath.Join(port, "idVendor"))
	pid, pidErr := readHexFile(filepath.Join(port, "idProduct"))
	if vidErr != nil || pidErr != nil {
		return false, false
	}
	return vid == jabraVendorID && scopedControlPID(pid), true
}

func controlDevicePermit(device *jabra_DeviceInfo, search bool) (*controlPermit, error) {
	if device == nil {
		return nil, errLink390Identity
	}
	if controlDeviceBusy(device) {
		return nil, errControlBusy
	}
	if device.deviceConnection == deviceConnectionType_BT {
		device = deviceForID(device.parentDeviceID)
		if device == nil {
			return nil, errLink390Identity
		}
	}
	port, err := controlUSBParent(device.hidrawPath)
	if err != nil {
		if search && scopedControlPID(device.productID) {
			return nil, errLink390Identity
		}
		return link390ControlGate.sharedUnbound()
	}
	scoped, known := controlPortClass(port)
	if !scoped {
		if !known {
			return link390ControlGate.sharedUnbound()
		}
		return nil, nil
	}
	expected, err := filepath.EvalSymlinks(device.usbDevicePath)
	if err != nil || expected != port {
		return nil, errLink390Identity
	}
	vid, vidErr := readHexFile(filepath.Join(port, "idVendor"))
	pid, pidErr := readHexFile(filepath.Join(port, "idProduct"))
	if vidErr != nil || pidErr != nil || vid != device.vendorID || pid != device.productID {
		return nil, errLink390Identity
	}
	return link390ControlGate.shared(port, search)
}

func controlExchangePermit(h *hidrawConn) (*controlPermit, error) {
	if h == nil || h.control == nil && h.f == nil {
		return nil, errControlClosed
	}
	if h.controlOwner != nil {
		if h.controlOwner.valid() && h.controlOwner.port == h.controlPort {
			return nil, nil
		}
		return nil, errControlClosed
	}
	port := h.controlPort
	if port == "" {
		var err error
		port, err = controlUSBParent(h.path)
		if err != nil {
			return link390ControlGate.sharedUnbound()
		}
	}
	scoped, known := controlPortClass(port)
	if !scoped {
		if !known {
			return link390ControlGate.sharedUnbound()
		}
		return nil, nil
	}
	return link390ControlGate.shared(port, false)
}

func controlDeviceBusy(device *jabra_DeviceInfo) bool {
	if device == nil {
		return false
	}
	if device.deviceConnection == deviceConnectionType_BT {
		device = deviceForID(device.parentDeviceID)
		if device == nil {
			return false
		}
	}
	if link390ControlGate.busy(device.usbDevicePath) {
		return true
	}
	if port, err := filepath.EvalSymlinks(device.usbDevicePath); err == nil {
		return link390ControlGate.busy(port)
	}
	return false
}

func inspectControlAttachmentUsing(path string, locate func(string) (string, error)) (string, string, error) {
	port, err := locate(path)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(port)
	if err != nil {
		return port, "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return port, "", errLink390Identity
	}
	bus, address := readTextFile(filepath.Join(port, "busnum")), readTextFile(filepath.Join(port, "devnum"))
	if bus == "" || address == "" {
		return port, "", errLink390Identity
	}
	return port, fmt.Sprintf("%d:%d:%s:%s", stat.Dev, stat.Ino, bus, address), nil
}

// Unknown/non-scoped nodes retain their old idle open behavior. During an
// exclusive operation an unresolved parent cannot bypass ownership.
func openUnderControlPermit[T any](path string, permit *controlPermit, inspect func(string) (string, string, error), open func(string) (T, error), close func(T)) (T, error) {
	var zero T
	port, generation, beforeErr := inspect(path)
	if permit != nil {
		if beforeErr != nil || port != permit.port || generation == "" || !permit.valid() {
			return zero, errLink390Identity
		}
	} else if scopedControlPort(port) || beforeErr != nil && link390ControlGate.active() {
		return zero, errControlBusy
	}
	node, err := open(path)
	if err != nil {
		return zero, err
	}
	after, next, afterErr := inspect(path)
	if permit != nil {
		if afterErr != nil || after != port || next != generation || !permit.valid() {
			close(node)
			return zero, errLink390Changed
		}
	} else if scopedControlPort(after) || afterErr != nil && link390ControlGate.active() {
		close(node)
		return zero, errControlBusy
	}
	return node, nil
}

func openHidrawCoordinated(path string, borrowed *controlPermit, locate func(string) (string, error), openRaw func(string) (*hidrawConn, error)) (*hidrawConn, error) {
	port, locateErr := locate(path)
	var permit *controlPermit
	if borrowed != nil {
		if locateErr != nil || port != borrowed.port || !borrowed.valid() {
			return nil, errControlClosed
		}
		permit = borrowed
	} else if locateErr == nil && scopedControlPort(port) {
		var err error
		permit, err = link390ControlGate.shared(port, false)
		if err != nil {
			return nil, err
		}
	}
	inspect := func(path string) (string, string, error) { return inspectControlAttachmentUsing(path, locate) }
	h, err := openUnderControlPermit(path, permit, inspect, openRaw, func(h *hidrawConn) {
		if h != nil {
			h.close()
		}
	})
	if err == nil && h == nil {
		err = errors.New("management opener returned no handle")
	}
	if err != nil {
		if borrowed == nil {
			permit.release()
		}
		return nil, err
	}
	if permit != nil {
		h.controlPort, h.controlOwner = port, permit
		if borrowed == nil {
			h.controlRelease = permit.release
		}
	} else if locateErr == nil {
		h.controlPort = port
	}
	return h, nil
}
