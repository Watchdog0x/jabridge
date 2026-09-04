package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

type settingScope int

const (
	settingScopeDongle settingScope = iota
	settingScopeHeadset
)

type boolSettingDefinition struct {
	Key                 string
	Label               string
	Scope               settingScope
	Class               byte
	Op                  byte
	Destination         byte
	Request             []byte
	ResponseIndex       int
	WritePrefix         []byte
	Invert              bool
	Writable            bool
	NeedsConfigMode     bool
	ValidatedLink380    bool
	CatalogProperties   []string
	HasRawValues        bool
	FalseRaw            byte
	TrueRaw             byte
	BitMask             byte
	PreservePayload     bool
	ProbeWithoutCatalog bool
}

type boolSettingValue struct {
	Definition boolSettingDefinition
	Value      bool
	Editable   bool
}

type deviceSettingValue struct {
	Boolean *boolSettingValue
	Choice  *choiceSettingValue
	Text    *textSettingValue
	Remote  *remoteSettingValue
}

type textSettingDefinition struct {
	Key               string
	Label             string
	Class             byte
	Op                byte
	Destination       byte
	CatalogProperties []string
	Writable          bool
	MaxBytes          int
}

type textSettingValue struct {
	Definition textSettingDefinition
	Value      string
	Editable   bool
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
	if setting.Text != nil {
		return setting.Text.Definition.Key
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
	if setting.Text != nil {
		return setting.Text.Definition.Label
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
	if setting.Text != nil {
		return setting.Text.Value
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
	if setting.Text != nil {
		return setting.Text.Editable
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
	if setting.Text != nil {
		return "", fmt.Errorf("use `jabridge settings set headset.%s VALUE` to edit text", setting.Text.Definition.Key)
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
	if setting.Text != nil {
		return fmt.Errorf("use the settings command to edit %s", setting.Text.Definition.Key)
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
		CatalogProperties: []string{"sidetoneEnabled"}, ProbeWithoutCatalog: true,
	},
	{
		Key: "sidetone", Label: "Sidetone", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x7c, Invert: true, Writable: true,
		ResponseIndex: 0, PreservePayload: true,
		CatalogProperties: []string{"sidetoneEnabledDsp"},
	},
	{
		Key: "in-call-busylight", Label: "In-call busylight", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x39, Writable: true,
		CatalogProperties: []string{"inCallBusyLightEnabled"}, ProbeWithoutCatalog: true,
	},
	{
		Key: "on-head-detection", Label: "On-head detection", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x92, Writable: true,
		CatalogProperties: []string{"onHeadDetectionEnabled"}, ProbeWithoutCatalog: true,
	},
	{
		Key: "music-mode", Label: "Music mode", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x25, Writable: true,
		CatalogProperties: []string{"musicModeEnabled"}, ProbeWithoutCatalog: true,
	},
	{
		Key: "auto-answer-on-head", Label: "Answer call when worn", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x91, Request: []byte{1}, ResponseIndex: 1,
		WritePrefix: []byte{1}, Writable: true,
		CatalogProperties: []string{"autoAnswerCallOnHeadEnabled"}, ProbeWithoutCatalog: true,
	},
	{
		Key: "auto-pause-music", Label: "Pause music when removed", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x8e, Request: []byte{0}, ResponseIndex: 1,
		WritePrefix: []byte{0}, Writable: true,
		CatalogProperties: []string{"autoPauseMusicOnHeadEnabled"}, ProbeWithoutCatalog: true,
	},
	{
		Key: "reverse-stereo", Label: "Reverse left and right audio", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x82, Writable: true,
		CatalogProperties: []string{"reversedStereoChannelsEnabled"}, ProbeWithoutCatalog: true,
	},
	{
		Key: "smart-ringer", Label: "SmartRinger", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0xda, Destination: 3, Writable: true,
		CatalogProperties: []string{"smartRingerEnabled", "baseSmartRingerEnabled"}, ProbeWithoutCatalog: true,
	},
	{
		Key: "boom-arm-answer", Label: "Answer call by rotating boom arm", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x98, Writable: true, BitMask: 0x02, PreservePayload: true,
		CatalogProperties: []string{"boomArmRotateAcceptCall"},
	},
	{
		Key: "auto-reject-call", Label: "Auto reject waiting call", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x3c, Writable: true,
		CatalogProperties: []string{"autoRejectBgWaitingEnabled"},
	},
	{
		Key: "button-sounds", Label: "Button sounds", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x3f, Writable: true,
		HasRawValues: true, FalseRaw: 0x00, TrueRaw: 0xff,
		CatalogProperties: []string{"buttonSoundsEnabled"},
	},
	{
		Key: "firmware-upgrade-lock", Label: "Firmware upgrade lock", Scope: settingScopeHeadset,
		Class: gnpClassFirmwareUpdate, Op: 0x34, Writable: true,
		CatalogProperties: []string{"firmwareUpgradeLock"},
	},
	{
		Key: "prioritize-computer-audio", Label: "Prioritize computer audio", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x99, Writable: true,
		CatalogProperties: []string{"prioritizedComputerAudioEnabled"},
	},
	{
		Key: "headset-ringer", Label: "Ringtone in headset", Scope: settingScopeHeadset,
		Class: gnpClassConfig, Op: 0x13, Request: []byte{0}, ResponseIndex: 1,
		WritePrefix: []byte{0}, Writable: true,
		CatalogProperties: []string{"ringer"},
	},
}

var headsetTextSettingDefinitions = []textSettingDefinition{
	{
		Key: "headset-name", Label: "Headset name", Class: gnpClassConfig, Op: 0x56,
		CatalogProperties: []string{"bluetoothName"}, Writable: true, MaxBytes: 32,
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
		paths := findHidrawPathsForPID(device.vendorID, device.productID)
		if len(paths) == 0 {
			return nil, 0, errors.New("no HID interface found; reconnect USB and run jabridge debug")
		}
		return nil, 0, fmt.Errorf("%s; run jabridge debug for all interfaces", describeHIDAccess(paths[0]))
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
	payload, err := readBoolSettingPayload(device, definition)
	if err != nil {
		return false, err
	}
	return decodeBoolSettingPayload(definition, payload)
}

func readBoolSettingPayload(device *jabra_DeviceInfo, definition boolSettingDefinition) ([]byte, error) {
	h, src, err := settingTransport(device)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	return payload, nil
}

func decodeBoolSettingPayload(definition boolSettingDefinition, payload []byte) (bool, error) {
	if definition.ResponseIndex < 0 || definition.ResponseIndex >= len(payload) {
		return false, fmt.Errorf("setting %s returned %d bytes", definition.Key, len(payload))
	}
	raw := payload[definition.ResponseIndex]
	var value bool
	if definition.BitMask != 0 {
		masked := raw & definition.BitMask
		if masked != 0 && masked != definition.BitMask {
			return false, fmt.Errorf("setting %s returned invalid masked boolean %d", definition.Key, raw)
		}
		value = masked == definition.BitMask
	} else {
		falseRaw, trueRaw := byte(0), byte(1)
		if definition.HasRawValues {
			falseRaw, trueRaw = definition.FalseRaw, definition.TrueRaw
		}
		if raw != falseRaw && raw != trueRaw {
			return false, fmt.Errorf("setting %s returned invalid boolean %d", definition.Key, raw)
		}
		value = raw == trueRaw
	}
	if definition.Invert {
		value = !value
	}
	return value, nil
}

func encodeBoolSettingPayload(definition boolSettingDefinition, value bool) []byte {
	logical := value
	if definition.Invert {
		logical = !logical
	}
	falseRaw, trueRaw := byte(0), byte(1)
	if definition.HasRawValues {
		falseRaw, trueRaw = definition.FalseRaw, definition.TrueRaw
	}
	raw := falseRaw
	if logical {
		raw = trueRaw
		if definition.BitMask != 0 {
			raw = definition.BitMask
		}
	}
	payload := append([]byte(nil), definition.WritePrefix...)
	return append(payload, raw)
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
	if definition.PreservePayload {
		current, err := readBoolSettingPayload(device, definition)
		if err != nil {
			return fmt.Errorf("read %s before write: %w", definition.Key, err)
		}
		raw := payload[len(payload)-1]
		payload, err = mergeSettingPayload(current, definition.ResponseIndex, definition.BitMask, raw)
		if err != nil {
			return fmt.Errorf("setting %s: %w", definition.Key, err)
		}
	}
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

func mergeSettingPayload(current []byte, index int, mask, raw byte) ([]byte, error) {
	if index < 0 || index >= len(current) {
		return nil, fmt.Errorf("setting payload index %d is outside %d bytes", index, len(current))
	}
	result := append([]byte(nil), current...)
	if mask != 0 {
		result[index] = (result[index] &^ mask) | (raw & mask)
	} else {
		result[index] = raw
	}
	return result, nil
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
	var capabilities *modelcatalog.Capabilities
	var catalogErr error
	if scope == settingScopeHeadset {
		capabilities, catalogErr = lookupDeviceModel(device)
	}
	seen := make(map[string]struct{})
	for _, definition := range settingDefinitions(scope) {
		if _, exists := seen[definition.Key]; exists {
			continue
		}
		catalogMatched := scope != settingScopeHeadset || len(definition.CatalogProperties) == 0
		if scope == settingScopeHeadset && catalogErr == nil {
			_, catalogMatched = firstCatalogProperty(capabilities, definition.CatalogProperties)
		} else if scope == settingScopeHeadset && catalogErr != nil {
			catalogMatched = definition.ProbeWithoutCatalog
		}
		if !catalogMatched {
			continue
		}
		value, err := readBoolSetting(device, definition)
		if err != nil {
			continue
		}
		seen[definition.Key] = struct{}{}
		values = append(values, boolSettingValue{
			Definition: definition,
			Value:      value,
			Editable:   canEditBoolSetting(device, definition) && (scope != settingScopeHeadset || catalogErr == nil),
		})
	}
	return values
}

func firstCatalogProperty(capabilities *modelcatalog.Capabilities, names []string) (modelcatalog.Property, bool) {
	if capabilities == nil {
		return modelcatalog.Property{}, false
	}
	for _, name := range names {
		if property, exists := capabilities.Properties[name]; exists {
			return property, true
		}
	}
	return modelcatalog.Property{}, false
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
	if scope == settingScopeHeadset {
		for _, value := range readSupportedTextSettings(device) {
			copy := value
			settings = append(settings, deviceSettingValue{Text: &copy})
		}
	}
	return settings
}

func readSupportedTextSettings(device *jabra_DeviceInfo) []textSettingValue {
	capabilities, err := lookupDeviceModel(device)
	if err != nil {
		return nil
	}
	values := make([]textSettingValue, 0, len(headsetTextSettingDefinitions))
	for _, definition := range headsetTextSettingDefinitions {
		if _, supported := firstCatalogProperty(capabilities, definition.CatalogProperties); !supported {
			continue
		}
		value, readErr := readTextSetting(device, definition)
		if readErr != nil {
			continue
		}
		values = append(values, textSettingValue{Definition: definition, Value: value, Editable: definition.Writable})
	}
	return values
}

func readTextSetting(device *jabra_DeviceInfo, definition textSettingDefinition) (string, error) {
	h, defaultDestination, err := settingTransport(device)
	if err != nil {
		return "", err
	}
	defer h.close()
	payload, err := gnpQueryPayloadWithDataTimeout(
		h, settingDestination(definition.Destination, defaultDestination), nextSeq(),
		definition.Class, definition.Op, nil, 900*time.Millisecond,
	)
	if err != nil {
		return "", err
	}
	if index := strings.IndexByte(string(payload), 0); index >= 0 {
		payload = payload[:index]
	}
	if len(payload) == 0 || !utf8.Valid(payload) {
		return "", errors.New("headset name is empty or invalid UTF-8")
	}
	return string(payload), nil
}

func writeTextSetting(device *jabra_DeviceInfo, definition textSettingDefinition, value string) error {
	value = strings.TrimSpace(value)
	if !definition.Writable {
		return fmt.Errorf("setting %s is read-only", definition.Key)
	}
	if err := validateSettingText(value, definition.MaxBytes); err != nil {
		return err
	}
	payload := append([]byte(value), 0)
	if err := writeSettingPacket(device, definition.Destination, definition.Class, definition.Op, payload, false); err != nil {
		return fmt.Errorf("write %s: %w", definition.Key, err)
	}
	readBack, err := readTextSetting(device, definition)
	if err != nil {
		return fmt.Errorf("verify %s: %w", definition.Key, err)
	}
	if readBack != value {
		return fmt.Errorf("verify %s: wrote %q but read %q", definition.Key, value, readBack)
	}
	return nil
}

func validateSettingText(value string, maximum int) error {
	if value == "" {
		return errors.New("value cannot be empty")
	}
	if !utf8.ValidString(value) {
		return errors.New("value must be valid UTF-8")
	}
	if maximum > 0 && len([]byte(value)) > maximum {
		return fmt.Errorf("value is longer than %d bytes", maximum)
	}
	for _, character := range value {
		if character == 0 || character < 0x20 || character == 0x7f {
			return errors.New("value contains a control character")
		}
	}
	return nil
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
