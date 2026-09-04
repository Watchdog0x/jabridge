package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

var deviceDiagnosticMu sync.Mutex

func (j *jabraAPIBridge) DiagnoseDevice(id uint16) ([]ipc.DiagnosticCheck, error) {
	if !deviceDiagnosticMu.TryLock() {
		return nil, fmt.Errorf("another device diagnostic is already running")
	}
	defer deviceDiagnosticMu.Unlock()
	device := deviceForID(id)
	if device == nil {
		return nil, fmt.Errorf("device disconnected before diagnostic")
	}
	checks := []ipc.DiagnosticCheck{}
	add := func(feature, state, detail string) {
		checks = append(checks, ipc.DiagnosticCheck{Feature: feature, State: state, Detail: detail})
	}
	management := device
	if device.deviceConnection == deviceConnectionType_BT {
		management = deviceForID(device.parentDeviceID)
	}
	if management != nil && !management.gnpDestinationKnown {
		// Retry the existing IDENT reads after permissions may have changed.
		// Keep ownership in the daemon and use only supported descriptors.
		enrichUSBDevice(management)
		management = deviceForID(management.deviceID)
		if current := deviceForID(id); current != nil {
			device = current
		}
	}
	ready := management != nil && management.gnpDestinationKnown
	if !ready {
		add("native control", "BLOCKED", "No responsive management endpoint. Check HID access/report diagnostics; not proof of unsupported hardware.")
	} else {
		add("native control", "INFO", "Using the service-owned management endpoint; individual reads are tested below.")
		if version, err := readFirmwareVersion(device); err == nil {
			add("native installed firmware", "PASS", safeFirmwareDiagnostic(version))
		} else {
			add("native installed firmware", "FAIL", protocolDiagnosticError(err))
		}
		if !device.isDongle {
			if battery, err := readGNPBatteryStatus(device); err == nil {
				add("native battery", "PASS", fmt.Sprintf("%d%%; charging=%t; extra batteries=%d", battery.levelInPercent, battery.charging, len(battery.components)))
			} else {
				detail := protocolDiagnosticError(err)
				state := "UNAVAILABLE"
				if detail == "response format/value rejected by native decoder" {
					state = "FAIL"
				}
				add("native battery", state, detail+"; wired devices may have no battery")
			}
		}
	}
	capabilities, catalogErr := lookupDeviceModel(device)
	if catalogErr != nil {
		add("online model catalog", "UNAVAILABLE", "Model/variant lookup failed; model-filtered settings coverage cannot be established.")
	} else {
		add("online model catalog", "INFO", fmt.Sprintf("profile=%s; properties=%d; exact installed version=%t (metadata, not a hardware test)", safeFirmwareDiagnostic(capabilities.Firmware), len(capabilities.Properties), capabilities.DeviceFirmware != "" && capabilities.ExactFirmwareProfile))
		if capabilities.FirmwareProtocolKnown {
			add("firmware update protocol", "INFO", fmt.Sprintf("catalog protocol %d; transfer/recovery not tested", capabilities.FirmwareProtocol))
		}
	}
	checks = append(checks, diagnoseSettings(device, capabilities, ready)...)
	for _, feature := range []string{"setting writes and read-back", "pairing/reset", "firmware installation/recovery", "microphone and speaker audio quality", "button/wheel behavior", "meeting-app call control", "USB reconnect and power cycle"} {
		add(feature, "NOT TESTED", "Needs a separate hardware test; not exercised by this read-only report.")
	}
	return checks, nil
}

func protocolDiagnosticError(err error) string {
	if err == nil {
		return "ready"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "timeout"), strings.Contains(message, "timed out"):
		return "device reply timed out"
	case strings.Contains(message, "nak"):
		return "device rejected query"
	case strings.Contains(message, "invalid"), strings.Contains(message, "returned"), strings.Contains(message, "too short"):
		return "response format/value rejected by native decoder"
	default:
		return diagnosticError(err)
	}
}

func safeFirmwareDiagnostic(value string) string {
	if len(value) == 0 || len(value) > 40 {
		return "version format unrecognized"
	}
	for _, char := range value {
		if (char < '0' || char > '9') && char != '.' && char != '-' {
			return "version format unrecognized"
		}
	}
	return value
}

func diagnoseSettings(device *jabra_DeviceInfo, capabilities *modelcatalog.Capabilities, ready bool) []ipc.DiagnosticCheck {
	checks := []ipc.DiagnosticCheck{}
	covered := map[string]bool{}
	seen := map[string]bool{}
	failures := 0
	deadline := time.Now().Add(30 * time.Second)
	run := func(key string, properties []string, probe bool, read func() (string, error)) {
		if seen[key] {
			return
		}
		_, supported := firstCatalogProperty(capabilities, properties)
		if capabilities != nil && !device.isDongle && !supported {
			return
		}
		if capabilities == nil && !device.isDongle && !probe {
			return
		}
		seen[key] = true
		for _, property := range properties {
			covered[property] = true
		}
		check := ipc.DiagnosticCheck{Feature: "setting " + key}
		switch {
		case !ready:
			check.State = "BLOCKED"
			check.Detail = "Management endpoint unavailable."
		case failures >= 3 || time.Now().After(deadline):
			check.State = "NOT TESTED"
			check.Detail = "Stopped after repeated read failures or the read-time budget."
		default:
			value, err := read()
			if err != nil {
				failures++
				check.State = "FAIL"
				check.Detail = protocolDiagnosticError(err)
			} else {
				check.State = "PASS"
				check.Detail = value + " (read only; writes not tested)"
			}
		}
		checks = append(checks, check)
	}
	scope := settingScopeHeadset
	if device.isDongle {
		scope = settingScopeDongle
	}
	for _, definition := range settingDefinitions(scope) {
		run(definition.Key, definition.CatalogProperties, definition.ProbeWithoutCatalog, func() (string, error) { value, err := readBoolSetting(device, definition); return onOff(value), err })
	}
	for _, definition := range choiceSettingDefinitions(scope) {
		run(definition.Key, definition.CatalogProperties, definition.ProbeWithoutCatalog, func() (string, error) {
			if property, ok := firstCatalogProperty(capabilities, definition.CatalogProperties); ok {
				definition.Choices = choicesAllowedByCatalog(definition.Choices, property.PossibleValues)
			}
			value, err := readChoiceSetting(device, definition)
			if err == nil && value.ChoiceIndex < 0 {
				return "", fmt.Errorf("invalid choice value")
			}
			return choiceSettingName(value), err
		})
	}
	if !device.isDongle {
		for _, definition := range headsetTextSettingDefinitions {
			run(definition.Key, definition.CatalogProperties, false, func() (string, error) {
				_, err := readTextSetting(device, definition)
				return "text read; value omitted for privacy", err
			})
		}
	}
	checks = append(checks, unimplementedCatalogChecks(capabilities, covered)...)
	return checks
}

func unimplementedCatalogChecks(capabilities *modelcatalog.Capabilities, covered map[string]bool) []ipc.DiagnosticCheck {
	if capabilities == nil {
		return nil
	}
	var keys []string
	for key := range capabilities.Properties {
		if !covered[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var checks []ipc.DiagnosticCheck
	for _, key := range keys {
		checks = append(checks, ipc.DiagnosticCheck{Feature: "catalog property " + key, State: "NOT COVERED", Detail: "No matching setting reader in this diagnostic; may be telemetry or an unimplemented setting."})
	}
	return checks
}
