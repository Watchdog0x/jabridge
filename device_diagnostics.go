package main

import (
	"errors"
	"fmt"
	"path/filepath"
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
		if management != nil && management.controlDiagnostic != "" {
			add("IDENT discovery attempts", "INFO", management.controlDiagnostic)
		}
	} else {
		address := device.gnpDestination
		if device.deviceConnection == deviceConnectionType_BT {
			address = 4
		}
		add("native control", "INFO", fmt.Sprintf("daemon endpoint=%s address=%d; individual reads tested below", filepath.Base(management.hidrawPath), address))
		if version, err := readFirmwareVersion(device); err == nil {
			if safeFirmwareDiagnostic(version) == "version format unrecognized" {
				add("native installed firmware", "FAIL", "response version format unrecognized")
			} else {
				device.firmwareVersion = version
				add("native installed firmware", "PASS", safeFirmwareDiagnostic(version))
			}
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
		var ambiguity *modelcatalog.AmbiguousVariantError
		if errors.As(catalogErr, &ambiguity) {
			variants := strings.Join(ambiguity.Variants, ", ")
			if variants == "" {
				variants = "variant labels unavailable"
			}
			add("online model catalog", "INFO", fmt.Sprintf("USB PID matches %d published variants (%s); device variant is required before selecting settings.", ambiguity.Count, variants))
		} else {
			add("online model catalog", "UNAVAILABLE", "Model/variant lookup failed; model-filtered settings coverage cannot be established.")
		}
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
	if capabilities == nil && !ready && !device.isDongle {
		return []ipc.DiagnosticCheck{{
			Feature: "setting discovery",
			State:   "BLOCKED",
			Detail:  "Management transport and exact model variant are unavailable; individual settings were not probed.",
		}}
	}
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
		annotateSettingQuery(checks, definition.Key, device, definition.Destination, definition.Class, definition.Op)
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
		annotateSettingQuery(checks, definition.Key, device, definition.Destination, definition.Class, definition.Op)
	}
	if !device.isDongle {
		for _, definition := range headsetTextSettingDefinitions {
			run(definition.Key, definition.CatalogProperties, false, func() (string, error) {
				_, err := readTextSetting(device, definition)
				return "text read; value omitted for privacy", err
			})
		}
	}
	checks = append(checks, catalogCoverageChecks(capabilities, covered)...)
	return checks
}

func annotateSettingQuery(checks []ipc.DiagnosticCheck, key string, device *jabra_DeviceInfo, override, class, op byte) {
	if len(checks) == 0 || checks[len(checks)-1].Feature != "setting "+key {
		return
	}
	check := &checks[len(checks)-1]
	if strings.Contains(check.Detail, "query=") {
		return
	}
	address := gnpSrcHost
	if device.isDongle {
		address = gnpSrcDongle
	} else if device.deviceConnection == deviceConnectionType_BT {
		address = 4
	} else if device.gnpDestinationKnown {
		address = device.gnpDestination
	}
	address = settingDestination(override, address)
	check.Detail += fmt.Sprintf("; query=%02x/%02x address=%d", class, op, address)
}

func catalogCoverageChecks(capabilities *modelcatalog.Capabilities, covered map[string]bool) []ipc.DiagnosticCheck {
	if capabilities == nil {
		return nil
	}
	var keys []string
	for key := range capabilities.Properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var checks []ipc.DiagnosticCheck
	for _, key := range keys {
		state := "NOT COVERED"
		if covered[key] {
			state = "INFO"
		}
		checks = append(checks, ipc.DiagnosticCheck{Feature: "catalog property " + key, State: state, Detail: catalogPropertyDetail(capabilities.Properties[key])})
	}
	return checks
}

func catalogPropertyDetail(property modelcatalog.Property) string {
	access := property.Access
	if access == "" {
		access = "unknown"
	}
	restart := "unknown"
	if property.RestartKnown {
		restart = fmt.Sprint(property.RequiresRestart)
	}
	detail := fmt.Sprintf("id=%s; type=%s; choices=%v; access=%s; restart=%s", property.SettingID, property.Kind, property.PossibleValues, access, restart)
	if property.Group != "" {
		detail += "; group=" + property.Group
	}
	if property.Help != "" {
		detail += "; help=" + property.Help
	}
	return detail + "; metadata only, runtime protection not tested"
}
