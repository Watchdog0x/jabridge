package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type settingScope int

const (
	settingScopeDongle settingScope = iota
	settingScopeHeadset
)

type boolSettingDefinition struct {
	Key              string
	Label            string
	Scope            settingScope
	Class            byte
	Op               byte
	Destination      byte
	Request          []byte
	ResponseIndex    int
	WritePrefix      []byte
	Invert           bool
	Writable         bool
	NeedsConfigMode  bool
	ValidatedLink380 bool
}

type boolSettingValue struct {
	Definition boolSettingDefinition
	Value      bool
	Editable   bool
}

type deviceSettingValue struct {
	Boolean *boolSettingValue
	Choice  *choiceSettingValue
	Remote  *remoteSettingValue
}

type remoteSettingValue struct {
	Device   string
	Key      string
	Label    string
	Value    string
	Editable bool
	Choices  []string
}

func (setting deviceSettingValue) key() string {
	if setting.Boolean != nil {
		return setting.Boolean.Definition.Key
	}
	if setting.Choice != nil {
		return setting.Choice.Definition.Key
	}
	if setting.Remote != nil {
		return setting.Remote.Key
	}
	return ""
}

func (setting deviceSettingValue) label() string {
	if setting.Boolean != nil {
		return setting.Boolean.Definition.Label
	}
	if setting.Choice != nil {
		return setting.Choice.Definition.Label
	}
	if setting.Remote != nil {
		return setting.Remote.Label
	}
	return "Unknown setting"
}

func (setting deviceSettingValue) valueName() string {
	if setting.Boolean != nil {
		return onOff(setting.Boolean.Value)
	}
	if setting.Choice != nil {
		return choiceSettingName(*setting.Choice)
	}
	if setting.Remote != nil {
		return setting.Remote.Value
	}
	return "Unknown"
}

func (setting deviceSettingValue) editable() bool {
	if setting.Boolean != nil {
		return setting.Boolean.Editable
	}
	if setting.Choice != nil {
		return setting.Choice.Editable
	}
	return setting.Remote != nil && setting.Remote.Editable
}

func (setting deviceSettingValue) needsConfigMode() bool {
	if setting.Boolean != nil {
		return setting.Boolean.Definition.NeedsConfigMode
	}
	return setting.Choice != nil && setting.Choice.Definition.NeedsConfigMode
}

func (setting deviceSettingValue) nextValueName() (string, error) {
	if setting.Boolean != nil {
		return onOff(!setting.Boolean.Value), nil
	}
	if setting.Choice != nil {
		index, err := nextChoiceIndex(*setting.Choice)
		if err != nil {
			return "", err
		}
		return setting.Choice.Definition.Choices[index].Name, nil
	}
	if setting.Remote != nil {
		if len(setting.Remote.Choices) == 0 {
			return "", fmt.Errorf("setting %s has no choices", setting.Remote.Key)
		}
		for index, choice := range setting.Remote.Choices {
			if strings.EqualFold(choice, setting.Remote.Value) {
				return setting.Remote.Choices[(index+1)%len(setting.Remote.Choices)], nil
			}
		}
		return setting.Remote.Choices[0], nil
	}
	return "", errors.New("invalid setting")
}

func writeNextDeviceSetting(device *jabra_DeviceInfo, setting deviceSettingValue) error {
	if setting.Boolean != nil {
		return writeBoolSetting(device, setting.Boolean.Definition, !setting.Boolean.Value)
	}
	if setting.Choice != nil {
		index, err := nextChoiceIndex(*setting.Choice)
		if err != nil {
			return err
		}
		return writeChoiceSetting(device, *setting.Choice, index)
	}
	return errors.New("invalid setting")
}

var dongleBoolSettingDefinitions = []boolSettingDefinition{
	{
		Key: "auto-pairing", Label: "Auto pairing", Scope: settingScopeDongle,
		Class: gnpClassConfig, Op: 0x40, Writable: true, ValidatedLink380: true,
	},
	{
		Key: "prioritize-computer-audio", Label: "Prioritize computer audio", Scope: settingScopeDongle,
		Class: gnpClassConfig, Op: 0x99, Writable: true, ValidatedLink380: true,
	},
	{
		Key: "dedicated-call", Label: "Dedicated call mode", Scope: settingScopeDongle,
		Class: gnpClassConfig, Op: 0x88, Writable: true, ValidatedLink380: true,
	},
	{
		Key: "bluetooth-radio", Label: "Bluetooth radio", Scope: settingScopeDongle,
		Class: gnpClassConfig, Op: 0x63, Request: []byte{0}, ResponseIndex: 1,
		WritePrefix: []byte{0}, Writable: true, NeedsConfigMode: true, ValidatedLink380: true,
	},
	{
		Key: "softphone-integration", Label: "Softphone integration", Scope: settingScopeDongle,
		Class: gnpClassConfig, Op: 0x4c, Writable: true, NeedsConfigMode: true, ValidatedLink380: true,
	},
}

var headsetBoolSettingDefinitions = []boolSettingDefinition{
	{
		Key: "sidetone", Label: "Sidetone", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x85, Invert: true, Writable: true,
	},
	{
		Key: "in-call-busylight", Label: "In-call busylight", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x39, Writable: true,
	},
	{
		Key: "on-head-detection", Label: "On-head detection", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x92, Writable: true,
	},
	{
		Key: "music-mode", Label: "Music mode", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x25, Writable: true,
	},
	{
		Key: "auto-answer-on-head", Label: "Answer call when worn", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x91, Request: []byte{1}, ResponseIndex: 1,
		WritePrefix: []byte{1}, Writable: true,
	},
	{
		Key: "auto-pause-music", Label: "Pause music when removed", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x8e, Request: []byte{0}, ResponseIndex: 1,
		WritePrefix: []byte{0}, Writable: true,
	},
	{
		Key: "reverse-stereo", Label: "Reverse left and right audio", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x82, Writable: true,
	},
	{
		Key: "smart-ringer", Label: "SmartRinger", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0xda, Destination: 3, Writable: true,
	},
}

func settingDefinitions(scope settingScope) []boolSettingDefinition {
	if scope == settingScopeDongle {
		return dongleBoolSettingDefinitions
	}
	return headsetBoolSettingDefinitions
}

func findBoolSettingDefinition(scope settingScope, key string) (boolSettingDefinition, bool) {
	for _, definition := range settingDefinitions(scope) {
		if definition.Key == key {
			return definition, true
		}
	}
	return boolSettingDefinition{}, false
}

func settingTransport(device *jabra_DeviceInfo) (*hidrawConn, byte, error) {
	if device == nil {
		return nil, 0, errors.New("no device selected")
	}
	if device.deviceConnection == deviceConnectionType_BT {
		parent := deviceForID(device.parentDeviceID)
		if parent == nil || !parent.isDongle {
			return nil, 0, errors.New("headset dongle is no longer connected")
		}
		h := openDeviceHidraw(parent)
		if h == nil {
			return nil, 0, errors.New("dongle has no usable GNP hidraw interface")
		}
		return h, 4, nil
	}
	h := openDeviceHidraw(device)
	if h == nil {
		return nil, 0, errors.New("device has no usable GNP hidraw interface")
	}
	src := gnpSrcHost
	if device.isDongle {
		src = gnpSrcDongle
	} else if device.gnpDestinationKnown {
		src = device.gnpDestination
	}
	return h, src, nil
}

func settingDestination(override, defaultDestination byte) byte {
	if override != 0 {
		return override
	}
	return defaultDestination
}

func readBoolSetting(device *jabra_DeviceInfo, definition boolSettingDefinition) (bool, error) {
	h, src, err := settingTransport(device)
	if err != nil {
		return false, err
	}
	defer h.close()

	destination := settingDestination(definition.Destination, src)
	payload, err := gnpQueryPayloadWithDataTimeout(
		h,
		destination,
		nextSeq(),
		definition.Class,
		definition.Op,
		definition.Request,
		900*time.Millisecond,
	)
	if err != nil {
		return false, err
	}
	return decodeBoolSettingPayload(definition, payload)
}

func decodeBoolSettingPayload(definition boolSettingDefinition, payload []byte) (bool, error) {
	if definition.ResponseIndex < 0 || definition.ResponseIndex >= len(payload) {
		return false, fmt.Errorf("setting %s returned %d bytes", definition.Key, len(payload))
	}
	raw := payload[definition.ResponseIndex]
	if raw > 1 {
		return false, fmt.Errorf("setting %s returned invalid boolean %d", definition.Key, raw)
	}
	value := raw == 1
	if definition.Invert {
		value = !value
	}
	return value, nil
}

func encodeBoolSettingPayload(definition boolSettingDefinition, value bool) []byte {
	raw := value
	if definition.Invert {
		raw = !raw
	}
	payload := append([]byte(nil), definition.WritePrefix...)
	if raw {
		return append(payload, 1)
	}
	return append(payload, 0)
}

func canEditBoolSetting(device *jabra_DeviceInfo, definition boolSettingDefinition) bool {
	if device == nil || !definition.Writable {
		return false
	}
	if device.isDongle {
		return supportsExperimentalDongleWrites(device.productID) && definition.ValidatedLink380
	}
	// Headset settings are shown only after the matching destination answers,
	// and every write is immediately read back. Direct USB uses destination 8;
	// a headset behind a dongle uses destination 4.
	return true
}

func writeBoolSetting(device *jabra_DeviceInfo, definition boolSettingDefinition, value bool) error {
	if !canEditBoolSetting(device, definition) {
		return fmt.Errorf("setting %s is read-only on this device: %w", definition.Key, ErrNotSupported)
	}
	payload := encodeBoolSettingPayload(definition, value)
	if err := writeSettingPacket(device, definition.Destination, definition.Class, definition.Op, payload, definition.NeedsConfigMode); err != nil {
		return fmt.Errorf("write %s: %w", definition.Key, err)
	}

	readBack, err := readBoolSettingWithRetry(device, definition)
	if err != nil {
		return fmt.Errorf("verify %s: %w", definition.Key, err)
	}
	if readBack != value {
		return fmt.Errorf("verify %s: wrote %s but read %s", definition.Key, onOff(value), onOff(readBack))
	}
	return nil
}

func writeSettingPacket(device *jabra_DeviceInfo, overrideDestination, class, op byte, payload []byte, needsConfigMode bool) error {
	h, defaultDestination, err := settingTransport(device)
	if err != nil {
		return err
	}
	destination := defaultDestination
	if overrideDestination != 0 {
		destination = overrideDestination
	}
	if needsConfigMode {
		if err := enterConfigMode(h, defaultDestination); err != nil {
			h.close()
			return err
		}
	}
	writeErr := gnpCommand(h, destination, nextSeq(), class, op, payload)
	var endErr error
	if needsConfigMode {
		endErr = endConfigMode(h, defaultDestination)
	}
	h.close()
	if writeErr != nil {
		return writeErr
	}
	// Some devices reboot as configuration mode ends and drop the ACK. The
	// caller still performs a reconnect/readback check before reporting success.
	if endErr != nil && !strings.Contains(strings.ToLower(endErr.Error()), "timeout") &&
		!strings.Contains(strings.ToLower(endErr.Error()), "poll failed") {
		return fmt.Errorf("end configuration mode: %w", endErr)
	}
	if needsConfigMode {
		if err := waitForSettingsDeviceReady(device, 12*time.Second); err != nil {
			return err
		}
	}
	return nil
}

func waitForSettingsDeviceReady(original *jabra_DeviceInfo, timeout time.Duration) error {
	// Configuration-mode exit can acknowledge before USB starts its reboot.
	// Give that delayed detach a chance to happen, then require two consecutive
	// ready observations before returning to the caller.
	time.Sleep(750 * time.Millisecond)
	deadline := time.Now().Add(timeout)
	consecutive := 0
	for time.Now().Before(deadline) {
		scanAndAttachDevices()
		candidate := refreshedSettingsDevice(original)
		ready := candidate != nil && candidate.hidrawPath != ""
		if ready {
			if _, err := os.Stat(candidate.hidrawPath); err == nil {
				consecutive++
				if consecutive >= 2 {
					return nil
				}
			} else {
				consecutive = 0
			}
		} else {
			consecutive = 0
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("device did not become ready within %s after configuration change", timeout)
}

func enterConfigMode(h *hidrawConn, destination byte) error {
	payload, err := gnpQueryPayload(h, destination, nextSeq(), gnpClassPairingDevice, 0x11)
	if err != nil {
		return fmt.Errorf("request configuration permission: %w", err)
	}
	if len(payload) != 1 || payload[0] != 1 {
		return errors.New("device denied configuration permission")
	}
	return nil
}

func endConfigMode(h *hidrawConn, destination byte) error {
	return gnpCommand(h, destination, nextSeq(), gnpClassPairingDevice, 0x12, nil)
}

func readBoolSettingWithRetry(device *jabra_DeviceInfo, definition boolSettingDefinition) (bool, error) {
	var lastErr error
	for attempt := 0; attempt < 12; attempt++ {
		candidate := refreshedSettingsDevice(device)
		value, err := readBoolSetting(candidate, definition)
		if err == nil {
			return value, nil
		}
		lastErr = err
		if !definition.NeedsConfigMode {
			break
		}
		time.Sleep(time.Second)
		scanAndAttachDevices()
	}
	return false, lastErr
}

func refreshedSettingsDevice(original *jabra_DeviceInfo) *jabra_DeviceInfo {
	if original == nil {
		return nil
	}
	for _, candidate := range deviceSnapshots() {
		if candidate == nil || candidate.productID != original.productID || candidate.isDongle != original.isDongle {
			continue
		}
		if original.serialNumber == "" || candidate.serialNumber == original.serialNumber {
			return candidate
		}
	}
	return original
}

func readSupportedBoolSettings(device *jabra_DeviceInfo, scope settingScope) []boolSettingValue {
	values := make([]boolSettingValue, 0, len(settingDefinitions(scope)))
	for _, definition := range settingDefinitions(scope) {
		value, err := readBoolSetting(device, definition)
		if err != nil {
			continue
		}
		values = append(values, boolSettingValue{
			Definition: definition,
			Value:      value,
			Editable:   canEditBoolSetting(device, definition),
		})
	}
	return values
}

func readSupportedDeviceSettings(device *jabra_DeviceInfo, scope settingScope) []deviceSettingValue {
	settings := make([]deviceSettingValue, 0)
	for _, value := range readSupportedBoolSettings(device, scope) {
		copy := value
		settings = append(settings, deviceSettingValue{Boolean: &copy})
	}
	for _, value := range readSupportedChoiceSettings(device, scope) {
		copy := value
		settings = append(settings, deviceSettingValue{Choice: &copy})
	}
	return settings
}

func onOff(value bool) string {
	if value {
		return "On"
	}
	return "Off"
}

func formatBoolSetting(setting boolSettingValue) string {
	state := strings.ToUpper(onOff(setting.Value))
	if setting.Editable {
		return fmt.Sprintf("[%-3s] %s", state, setting.Definition.Label)
	}
	return fmt.Sprintf("[%-3s] %s (read only)", state, setting.Definition.Label)
}

func formatDeviceSetting(setting deviceSettingValue) string {
	value := strings.ToUpper(setting.valueName())
	if setting.editable() {
		return fmt.Sprintf("[%-7s] %s", value, setting.label())
	}
	return fmt.Sprintf("[%-7s] %s (read only)", value, setting.label())
}
