package main

import (
	"os"
	"path/filepath"
	"testing"
)

func useTemporaryConnectionPreference(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	connectionPreferenceMu.Lock()
	oldDirectory := connectionUserConfigDir
	oldLoaded := connectionPreferenceLoaded
	oldPresent := connectionPreferencePresent
	oldValue := connectionPreferenceValue
	connectionUserConfigDir = func() (string, error) { return directory, nil }
	connectionPreferenceLoaded = false
	connectionPreferencePresent = false
	connectionPreferenceValue = connectionPreference{}
	connectionPreferenceMu.Unlock()
	t.Cleanup(func() {
		connectionPreferenceMu.Lock()
		connectionUserConfigDir = oldDirectory
		connectionPreferenceLoaded = oldLoaded
		connectionPreferencePresent = oldPresent
		connectionPreferenceValue = oldValue
		connectionPreferenceMu.Unlock()
	})
	return filepath.Join(directory, "jabridge", connectionPreferenceFileName)
}

func TestSavedConnectionPreferenceRestoresWirelessRoute(t *testing.T) {
	path := useTemporaryConnectionPreference(t)
	withDeviceState(t, devices{
		0: {deviceID: 0, deviceName: "Jabra Link 380", productID: 0x24c7, isDongle: true},
		1: {deviceID: 1, deviceName: "Jabra Evolve2 65", productID: 0x24b7, deviceConnection: deviceConnectionType_USB},
		2: {deviceID: 2, deviceName: "Jabra Evolve2 65", productID: 0x24b7, deviceConnection: deviceConnectionType_BT, parentDeviceID: 0},
	}, 2, 0)
	if err := saveSelectedConnectionPreference(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("preference mode = %o, want 600", info.Mode().Perm())
	}

	connectionPreferenceMu.Lock()
	connectionPreferenceLoaded = false
	connectionPreferencePresent = false
	connectionPreferenceValue = connectionPreference{}
	connectionPreferenceMu.Unlock()
	deviceStateMu.Lock()
	selectedHeadset = 1
	deviceStateMu.Unlock()
	applySelectedConnectionPreference()
	if selectedHeadset != 2 || selectedDongle != 0 {
		t.Fatalf("restored selection = headset %d dongle %d", selectedHeadset, selectedDongle)
	}
}

func TestConnectionPreferenceDoesNotSelectUnrelatedHeadset(t *testing.T) {
	useTemporaryConnectionPreference(t)
	connectionPreferenceMu.Lock()
	connectionPreferenceLoaded = true
	connectionPreferencePresent = true
	connectionPreferenceValue = connectionPreference{Connection: "usb", ProductID: 0x24b7, Name: "Evolve2 65"}
	connectionPreferenceMu.Unlock()
	withDeviceState(t, devices{
		3: {deviceID: 3, deviceName: "Engage 50 II", productID: 0x1111, deviceConnection: deviceConnectionType_USB},
	}, 3, -1)
	applySelectedConnectionPreference()
	if selectedHeadset != 3 {
		t.Fatalf("unrelated headset selection changed to %d", selectedHeadset)
	}
}
