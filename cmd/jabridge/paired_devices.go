package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

func rememberedDeviceAt(index int) (pairedDevice, error) {
	dongle, exists := selectedDongleSnapshot()
	if !exists || dongle.pairingList == nil {
		return pairedDevice{}, errors.New("no dongle pairing list available")
	}
	if index < 0 || index >= len(dongle.pairingList.pairedDevices) {
		return pairedDevice{}, fmt.Errorf("remembered device %d is no longer available", index+1)
	}
	return dongle.pairingList.pairedDevices[index], nil
}

func rememberedDeviceByAddress(address [6]byte) (pairedDevice, error) {
	dongle, exists := selectedDongleSnapshot()
	if !exists || dongle.pairingList == nil {
		return pairedDevice{}, errors.New("no dongle pairing list available")
	}
	for _, device := range dongle.pairingList.pairedDevices {
		if device.deviceBTAddr == address {
			return device, nil
		}
	}
	return pairedDevice{}, errors.New("remembered device is no longer available")
}

func rememberedDongle() (*jabra_DeviceInfo, error) {
	dongle, err := selectedDongleDevice()
	if err != nil {
		return nil, err
	}
	if !supportsExperimentalDongleWrites(dongle.productID) {
		return nil, fmt.Errorf("remembered-device editing is not enabled for PID 0x%04x: %w", dongle.productID, ErrNotSupported)
	}
	return dongle, nil
}

func connectRememberedDevice(index int) error {
	dongle, err := rememberedDongle()
	if err != nil {
		return err
	}
	device, err := rememberedDeviceAt(index)
	if err != nil {
		return err
	}
	if device.isConnected {
		return nil
	}
	if device.deviceBTAddr == [6]byte{} {
		return errors.New("remembered device has no usable Bluetooth address")
	}
	if err := disconnectAnyConnectedRememberedDevice(dongle, device.deviceBTAddr); err != nil {
		return err
	}

	h := openDeviceHidraw(dongle)
	if h == nil {
		return errors.New("open dongle GNP interface")
	}
	payload := append([]byte{device.bluetoothType}, device.deviceBTAddr[:]...)
	err = gnpCommand(h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, gnpOpBluetoothConnect, payload)
	h.close()
	if err != nil {
		return fmt.Errorf("connect remembered headset: %w", err)
	}
	return waitForRememberedDevice(device.deviceBTAddr, true, true, 15*time.Second)
}

func disconnectRememberedDevice(index int) error {
	dongle, err := rememberedDongle()
	if err != nil {
		return err
	}
	device, err := rememberedDeviceAt(index)
	if err != nil {
		return err
	}
	if !device.isConnected {
		return nil
	}
	h := openDeviceHidraw(dongle)
	if h == nil {
		return errors.New("open dongle GNP interface")
	}
	err = gnpCommand(h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, gnpOpDisconnectAll, nil)
	h.close()
	if err != nil {
		return fmt.Errorf("disconnect remembered headset: %w", err)
	}
	return waitForRememberedDevice(device.deviceBTAddr, true, false, 10*time.Second)
}

func forgetRememberedDevice(index int) error {
	dongle, err := rememberedDongle()
	if err != nil {
		return err
	}
	device, err := rememberedDeviceAt(index)
	if err != nil {
		return err
	}
	if device.isConnected {
		if err := disconnectRememberedDevice(index); err != nil {
			return err
		}
		device, err = rememberedDeviceByAddress(device.deviceBTAddr)
		if err != nil {
			return err
		}
	}
	payload := make([]byte, 3)
	binary.LittleEndian.PutUint16(payload[:2], device.databaseIndex)
	payload[2] = device.bluetoothType
	h := openDeviceHidraw(dongle)
	if h == nil {
		return errors.New("open dongle GNP interface")
	}
	err = gnpCommand(h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, gnpOpDeleteDBRecord, payload)
	h.close()
	if err != nil {
		return fmt.Errorf("forget remembered headset: %w", err)
	}
	return waitForRememberedDevice(device.deviceBTAddr, false, false, 8*time.Second)
}

func disconnectAnyConnectedRememberedDevice(dongle *jabra_DeviceInfo, except [6]byte) error {
	if dongle == nil || dongle.pairingList == nil {
		return nil
	}
	needsDisconnect := false
	for _, candidate := range dongle.pairingList.pairedDevices {
		if candidate.isConnected && candidate.deviceBTAddr != except {
			needsDisconnect = true
			break
		}
	}
	if !needsDisconnect {
		return nil
	}
	h := openDeviceHidraw(dongle)
	if h == nil {
		return errors.New("open dongle GNP interface")
	}
	err := gnpCommand(h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, gnpOpDisconnectAll, nil)
	h.close()
	if err != nil {
		return fmt.Errorf("disconnect current headset: %w", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pairings, readErr := getPairingList(dongle.deviceID)
		if readErr == nil {
			connected := false
			for _, candidate := range pairings.pairedDevices {
				connected = connected || candidate.isConnected
			}
			updateRememberedPairings(dongle.deviceID, pairings)
			if !connected {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("current headset did not disconnect before timeout")
}

func waitForRememberedDevice(address [6]byte, wantPresent, wantConnected bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		dongle, exists := selectedDongleSnapshot()
		if !exists {
			return errors.New("dongle disconnected while waiting for pairing state")
		}
		pairings, err := getPairingList(dongle.deviceID)
		if err == nil {
			present := false
			connected := false
			for _, candidate := range pairings.pairedDevices {
				if candidate.deviceBTAddr == address {
					present = true
					connected = candidate.isConnected
					break
				}
			}
			updateRememberedPairings(dongle.deviceID, pairings)
			if present == wantPresent && (!wantPresent || connected == wantConnected) {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !wantPresent {
		return errors.New("headset is still in the remembered-device list")
	}
	if wantConnected {
		return errors.New("headset did not connect before timeout")
	}
	return errors.New("headset did not disconnect before timeout")
}

func updateRememberedPairings(deviceID uint16, pairings *pairingList) {
	changed := false
	updateDeviceByID(deviceID, func(stored *jabra_DeviceInfo) {
		if !pairingListsEqual(stored.pairingList, pairings) {
			stored.pairingList = pairings
			changed = true
		}
	})
	if changed {
		requestUIRedraw()
	}
}
