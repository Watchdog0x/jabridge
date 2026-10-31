package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/Watchdog0x/jabridge/internal/firmware"
)

func beginSettingTransaction() (func(), error) {
	device, err := firmware.AcquireDeviceAccess()
	if err != nil {
		return nil, err
	}
	settings, err := firmware.AcquireSettingsWriteLease()
	if err != nil {
		_ = device.Close()
		return nil, err
	}
	return func() { _ = settings.Close(); _ = device.Close() }, nil
}

// An attached headset can update through another host without changing the
// dongle's USB attachment. Check new profile-bound writes on their own handle.
func checkSettingFirmware(expected string, read func() ([]byte, error)) error {
	if expected == "" {
		return nil
	}
	payload, err := read()
	if err != nil {
		return fmt.Errorf("check headset firmware: %w", err)
	}
	actual, err := decodeFirmwareVersionPayload(payload)
	if err != nil {
		return err
	}
	if actual != expected {
		return errors.New("headset firmware changed; reopen Settings before saving")
	}
	return nil
}

func withProfiledSettingTransport(device *jabra_DeviceInfo, key string, override byte, expected string, action func(*hidrawConn, byte) error) error {
	if expected == "" {
		return errors.New("headset firmware profile is unavailable; reopen Settings")
	}
	if err := currentSettingDevice(device); err != nil {
		return err
	}
	h, destination, err := settingPartTransport(device, key, override)
	if err != nil {
		return err
	}
	defer h.close()
	if err := verifySettingPart(device, key, override, h); err != nil {
		return err
	}
	if err := checkSettingFirmware(expected, func() ([]byte, error) {
		return gnpQueryPayloadWithDataTimeout(h, destination, nextSeq(), gnpClassDevInfo, gnpOpFirmwareVer, nil, 900*time.Millisecond)
	}); err != nil {
		return err
	}
	if err := currentSettingDevice(device); err != nil {
		return err
	}
	return action(h, destination)
}

func writeProfiledSettingPacket(device *jabra_DeviceInfo, key string, operation uint64, override, class, op byte, payload []byte, expected string) error {
	return withProfiledSettingTransport(device, key, override, expected, func(h *hidrawConn, destination byte) error {
		err := gnpCommand(h, destination, nextSeq(), class, op, payload)
		recordSettingEvidence(device, key, operation, "setting-ack", err)
		return err
	})
}

func verifyProfiledSettingReadback(device *jabra_DeviceInfo, key string, override byte, expected string) error {
	return withProfiledSettingTransport(device, key, override, expected, func(*hidrawConn, byte) error { return nil })
}

func encodeStructuredChoice(setting choiceSettingValue, wanted byte, current []byte) ([]byte, error) {
	value, err := decodeChoiceSettingPayload(setting.Definition, current)
	if err != nil {
		return nil, err
	}
	if value.Raw != setting.Raw || value.FallbackName != setting.FallbackName {
		return nil, errors.New("setting changed while editing; reopen Settings before saving")
	}
	return setting.Definition.Codec.Encode(current, wanted)
}

// Only route-qualified choices use this path. Existing scalar, EQ and power
// saving writes retain their helpers. The callback runs on the held transport
// after fresh headset firmware and attachment checks, before the command.
func writeProfiledChoicePacket(device *jabra_DeviceInfo, definition choiceSettingDefinition, operation uint64, payload []byte, guard func(*hidrawConn) error) error {
	return withProfiledSettingTransport(device, definition.Key, definition.Destination, definition.ExpectedFirmware, func(h *hidrawConn, destination byte) error {
		return sendProfiledChoicePacket(device, definition, operation, payload, h, destination, guard)
	})
}

func sendProfiledChoicePacket(device *jabra_DeviceInfo, definition choiceSettingDefinition, operation uint64, payload []byte, h *hidrawConn, destination byte, guard func(*hidrawConn) error) error {
	if err := guard(h); err != nil {
		return err
	}
	err := gnpCommand(h, destination, nextSeq(), definition.Class, definition.Op, payload)
	recordSettingEvidence(device, definition.Key, operation, "setting-ack", err)
	return err
}

func verifyChoiceRouteReadback(h *hidrawConn, guard func(*hidrawConn) error) error {
	if err := guard(h); err != nil {
		return errors.New("headset route changed during verification; reopen Settings")
	}
	return nil
}
