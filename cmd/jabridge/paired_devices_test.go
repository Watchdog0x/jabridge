package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestRememberedDeviceCommandPackets(t *testing.T) {
	address := [6]byte{1, 2, 3, 4, 5, 6}
	connectPayload := append([]byte{1}, address[:]...)
	connect, err := buildGNPReport(gnpSrcDongle, 0x51, gnpFlagCmd, gnpClassPairingDevice, gnpOpBluetoothConnect, connectPayload)
	if err != nil {
		t.Fatal(err)
	}
	connectWant := []byte{0x05, 0x01, 0x00, 0x51, 0x8d, 0x0d, 0x24, 1, 1, 2, 3, 4, 5, 6}
	if !bytes.Equal(connect[:len(connectWant)], connectWant) {
		t.Fatalf("connect packet = %x, want %x", connect[:len(connectWant)], connectWant)
	}

	deletePayload := make([]byte, 3)
	binary.LittleEndian.PutUint16(deletePayload, 0x1234)
	deletePayload[2] = 1
	forget, err := buildGNPReport(gnpSrcDongle, 0x52, gnpFlagCmd, gnpClassPairingDevice, gnpOpDeleteDBRecord, deletePayload)
	if err != nil {
		t.Fatal(err)
	}
	forgetWant := []byte{0x05, 0x01, 0x00, 0x52, 0x89, 0x0d, 0x2a, 0x34, 0x12, 1}
	if !bytes.Equal(forget[:len(forgetWant)], forgetWant) {
		t.Fatalf("forget packet = %x, want %x", forget[:len(forgetWant)], forgetWant)
	}
}

func TestRememberedDeviceAtNeverExposesAddressInError(t *testing.T) {
	withDeviceState(t, devices{0: {
		deviceID: 0, isDongle: true,
		pairingList: &pairingList{pairedDevices: []pairedDevice{{deviceName: "Test", deviceBTAddr: [6]byte{1, 2, 3, 4, 5, 6}}}},
	}}, -1, 0)
	if _, err := rememberedDeviceAt(5); err == nil {
		t.Fatal("out-of-range remembered device was accepted")
	} else if bytes.Contains([]byte(err.Error()), []byte("01")) {
		t.Fatalf("error unexpectedly contains address-like data: %v", err)
	}
}
