package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	firmwaretool "github.com/Watchdog0x/jabridge/internal/firmware"
)

// A USB setup may expose several management parts through the same HID node.
// Roles are model-specific: an address is not a universal device type.
type controlPart struct {
	Role       string
	Address    byte
	Path       string
	Variant    string
	Firmware   string
	Identity   string // private identity digest; never sent to IPC or debug
	Ready      bool
	Diagnostic string
}

type controlPartPlan struct {
	Role    string
	Address byte
}

func controlPartPlans(device *jabra_DeviceInfo) []controlPartPlan {
	if device == nil || device.deviceConnection != deviceConnectionType_USB || device.isDongle {
		return nil
	}
	switch device.productID {
	case 0x4051, 0x4052, 0x4053, 0x4054:
		return []controlPartPlan{{"headset", 1}, {"controller", 3}}
	case 0x4055, 0x4056:
		return []controlPartPlan{{"headset", 1}}
	}
	return nil
}

var controlPartsMu sync.Mutex

func findControlPart(device *jabra_DeviceInfo, role string) (controlPart, bool) {
	if device != nil {
		for _, part := range device.controlParts {
			if part.Role == role {
				return part, part.Ready
			}
		}
	}
	return controlPart{}, false
}

func hasController(device *jabra_DeviceInfo) bool {
	_, ok := findControlPart(device, "controller")
	return ok
}

func controllerWithoutHeadset(device *jabra_DeviceInfo) bool {
	if !hasController(device) {
		return false
	}
	_, headsetReady := findControlPart(device, "headset")
	return !headsetReady
}

// Only IDENT reads are used. No settings, mode changes or firmware commands.
func readControlPart(h *hidrawConn, path string, plan controlPartPlan) controlPart {
	part := controlPart{Role: plan.Role, Address: plan.Address, Path: path}
	payload, err := gnpQueryPayloadWithDataTimeout(h, plan.Address, nextSeq(), gnpClassDevInfo, gnpOpDeviceInfo, nil, 300*time.Millisecond)
	if err != nil {
		part.Diagnostic = protocolDiagnosticError(err)
		return part
	}
	variant, valid := decodeDeviceVariant(payload)
	if !valid {
		part.Diagnostic = "invalid identity reply"
		return part
	}
	if plan.Role == "headset" && !strings.HasPrefix(variant, "01-") {
		part.Diagnostic = "reply is not a headset identity"
		return part
	}
	if plan.Role == "controller" && !strings.HasPrefix(variant, "03-") {
		part.Diagnostic = "reply is not a known Engage controller identity"
		return part
	}
	part.Variant, part.Ready = variant, true
	if payload, err := gnpQueryPayloadWithDataTimeout(h, plan.Address, nextSeq(), gnpClassDevInfo, gnpOpFirmwareVer, nil, 300*time.Millisecond); err == nil {
		part.Firmware, _ = decodeFirmwareVersionPayload(payload)
	}
	if payload, err := gnpQueryPayloadWithDataTimeout(h, plan.Address, nextSeq(), gnpClassDevInfo, gnpOpSerialNumber, nil, 300*time.Millisecond); err == nil {
		if serial, valid := decodeLengthPrefixedString(payload); valid {
			part.Identity = fmt.Sprintf("%x", sha256.Sum256([]byte(serial)))
		}
	}
	return part
}

func discoverControlParts(paths []string, plans []controlPartPlan, probe func(string, controlPartPlan) controlPart) []controlPart {
	parts := make([]controlPart, 0, len(plans))
	for _, plan := range plans {
		part := controlPart{Role: plan.Role, Address: plan.Address, Diagnostic: "no accessible management interface"}
		for _, path := range paths {
			part = probe(path, plan)
			if part.Ready {
				break
			}
		}
		parts = append(parts, part)
	}
	return parts
}

func applyControlParts(device *jabra_DeviceInfo, parts []controlPart) {
	if !reflect.DeepEqual(device.controlParts, parts) || device.controlTopology == "" {
		device.controlTopology = newDeviceInstance()
	}
	device.controlParts = append([]controlPart(nil), parts...)
	// Never use a controller's identity as the headset/model identity.
	device.gnpDestinationKnown = false
	device.variantType, device.firmwareVersion = "", ""
	if part, ok := findControlPart(device, "headset"); ok {
		device.hidrawPath, device.gnpDestination, device.gnpDestinationKnown = part.Path, part.Address, true
		device.variantType, device.firmwareVersion = part.Variant, part.Firmware
	}
}

func refreshControlParts(device *jabra_DeviceInfo) {
	plans := controlPartPlans(device)
	if len(plans) == 0 {
		return
	}
	controlPartsMu.Lock()
	defer controlPartsMu.Unlock()
	current := deviceForID(device.deviceID)
	if current == nil || current.instance != device.instance {
		return
	}
	if time.Since(current.partsCheckedAt) < 2*time.Second {
		return
	}
	paths := filterGNPManagementPaths(findHidrawPathsForDevice(current), firmwaretool.HasControlLayout)
	parts := discoverControlParts(paths, plans, func(path string, plan controlPartPlan) controlPart {
		h, err := openHidraw(path)
		if err != nil {
			return controlPart{Role: plan.Role, Address: plan.Address, Path: path, Diagnostic: diagnosticError(err)}
		}
		defer h.close()
		return readControlPart(h, path, plan)
	})
	updateDeviceByID(device.deviceID, func(stored *jabra_DeviceInfo) {
		if stored.instance != current.instance {
			return
		}
		applyControlParts(stored, parts)
		stored.partsCheckedAt = time.Now()
	})
}

// Pin discovery to this physical USB path, not merely its product ID.
func findHidrawPathsForDevice(device *jabra_DeviceInfo) []string {
	paths := findHidrawPathsForPID(device.vendorID, device.productID)
	if device.usbDevicePath == "" {
		return paths
	}
	usb, err := filepath.EvalSymlinks(device.usbDevicePath)
	if err != nil {
		return nil
	}
	var matched []string
	for _, path := range paths {
		node, err := filepath.EvalSymlinks(filepath.Join("/sys/class/hidraw", filepath.Base(path), "device"))
		if err == nil && pathBelongsToUSB(node, usb) {
			matched = append(matched, path)
		}
	}
	return matched
}

func pathBelongsToUSB(node, usb string) bool {
	return strings.HasPrefix(filepath.Clean(node), filepath.Clean(usb)+string(filepath.Separator))
}

func settingPartRole(device *jabra_DeviceInfo, key string, override byte) string {
	if len(controlPartPlans(device)) == 0 {
		return ""
	}
	if override == 3 {
		return "controller"
	}
	return "headset"
}

func settingPartRoute(device *jabra_DeviceInfo, key string, override byte) (*jabra_DeviceInfo, byte, error) {
	role := settingPartRole(device, key, override)
	if role == "" {
		return device, override, nil
	}
	part, ready := findControlPart(device, role)
	if !ready {
		return nil, 0, fmt.Errorf("%s management part unavailable; run jabridge debug", role)
	}
	view := cloneDeviceInfo(device)
	view.hidrawPath, view.gnpDestination, view.gnpDestinationKnown = part.Path, part.Address, true
	return view, part.Address, nil
}

func settingPartTransport(device *jabra_DeviceInfo, key string, override byte) (*hidrawConn, byte, error) {
	view, destination, err := settingPartRoute(device, key, override)
	if err != nil {
		return nil, 0, err
	}
	h, fallback, err := settingTransport(view)
	return h, settingDestination(destination, fallback), err
}

func verifySettingPart(device *jabra_DeviceInfo, key string, override byte, h *hidrawConn) error {
	role := settingPartRole(device, key, override)
	if role == "" {
		return nil
	}
	part, ready := findControlPart(device, role)
	if !ready {
		return errors.New("setting part unavailable")
	}
	current := readControlPart(h, part.Path, controlPartPlan{role, part.Address})
	if !current.Ready || current.Variant != part.Variant || current.Identity != part.Identity {
		return errors.New("device part changed or stopped answering; reopen settings")
	}
	return currentSettingDevice(device)
}

func ipcControlParts(device *jabra_DeviceInfo) []ipc.ControlPartInfo {
	var result []ipc.ControlPartInfo
	for _, part := range device.controlParts {
		result = append(result, ipc.ControlPartInfo{Role: part.Role, Address: part.Address, Variant: part.Variant, Firmware: part.Firmware, Ready: part.Ready})
	}
	return result
}

func controlPartDiagnostics(device *jabra_DeviceInfo) []ipc.DiagnosticCheck {
	var checks []ipc.DiagnosticCheck
	for _, part := range device.controlParts {
		state := "BLOCKED"
		detail := fmt.Sprintf("address=%d; %s", part.Address, part.Diagnostic)
		if part.Ready {
			state = "PASS"
			detail = fmt.Sprintf("IDENT reply; address=%d; variant=%s; firmware=%s; setting reads tested separately", part.Address, part.Variant, safeFirmwareDiagnostic(part.Firmware))
		}
		checks = append(checks, ipc.DiagnosticCheck{Feature: "part " + part.Role, State: state, Detail: detail})
	}
	return checks
}

func verifySettingReadbackPart(device *jabra_DeviceInfo, key string, override byte) error {
	if len(controlPartPlans(device)) == 0 {
		return nil
	}
	h, _, err := settingPartTransport(device, key, override)
	if err != nil {
		return err
	}
	defer h.close()
	return verifySettingPart(device, key, override, h)
}

func settingValuePart(device *jabra_DeviceInfo, setting deviceSettingValue) string {
	if setting.Remote != nil {
		return setting.Remote.Component
	}
	if hasController(device) {
		switch setting.key() {
		case "call-button", "mute-button", "three-dot-button", "four-dot-button", "speed-dial", "speed-dial-2":
			return "controller" // UI grouping only; these use the main setting route.
		}
	}
	override := byte(0)
	if setting.Boolean != nil {
		override = setting.Boolean.Definition.Destination
	}
	if setting.Choice != nil {
		override = setting.Choice.Definition.Destination
	}
	if setting.Text != nil {
		override = setting.Text.Definition.Destination
	}
	return settingPartRole(device, setting.key(), override)
}

func filterControllerSettings(device *jabra_DeviceInfo, settings []deviceSettingValue, controller bool) []deviceSettingValue {
	var result []deviceSettingValue
	for _, setting := range settings {
		if (settingValuePart(device, setting) == "controller") == controller {
			result = append(result, setting)
		}
	}
	return result
}
