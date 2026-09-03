package main

import "testing"

func TestSerialNumberCheck(t *testing.T) {
	origDeviceManager := deviceManager
	origSelectedDongle := selectedDongle
	origSelectedHeadset := selectedHeadset
	defer func() {
		deviceManager = origDeviceManager
		selectedDongle = origSelectedDongle
		selectedHeadset = origSelectedHeadset
	}()

	t.Run("empty serial number returns false", func(t *testing.T) {
		deviceManager = devices{}
		selectedDongle = -1
		selectedHeadset = -1

		got := serialNumberCheck(&jabra_DeviceInfo{
			isDongle:     true,
			serialNumber: "",
		})
		if got {
			t.Error("expected false for empty serial number, got true")
		}
	})

	t.Run("new dongle when selectedDongle is -1 returns true", func(t *testing.T) {
		deviceManager = devices{}
		selectedDongle = -1
		selectedHeadset = -1

		got := serialNumberCheck(&jabra_DeviceInfo{
			deviceID:     1,
			isDongle:     true,
			serialNumber: "DONGLE001",
		})
		if !got {
			t.Error("expected true for new dongle when selectedDongle==-1, got false")
		}
	})

	t.Run("existing dongle same serial returns false and updates deviceID", func(t *testing.T) {
		deviceManager = devices{
			0: {deviceID: 1, isDongle: true, serialNumber: "DONGLE001", deviceConnection: deviceConnectionType_USB},
		}
		selectedDongle = 0
		selectedHeadset = -1

		got := serialNumberCheck(&jabra_DeviceInfo{
			deviceID:         99,
			isDongle:         true,
			serialNumber:     "DONGLE001",
			deviceConnection: deviceConnectionType_USB,
		})
		if got {
			t.Error("expected false for same serial, got true")
		}
		if deviceManager[0].deviceID != 99 {
			t.Errorf("expected deviceID to be updated to 99, got %d", deviceManager[0].deviceID)
		}
	})

	t.Run("existing dongle different serial returns true", func(t *testing.T) {
		deviceManager = devices{
			0: {deviceID: 1, isDongle: true, serialNumber: "DONGLE001", deviceConnection: deviceConnectionType_USB},
		}
		selectedDongle = 0
		selectedHeadset = -1

		got := serialNumberCheck(&jabra_DeviceInfo{
			deviceID:         2,
			isDongle:         true,
			serialNumber:     "DONGLE002",
			deviceConnection: deviceConnectionType_USB,
		})
		if !got {
			t.Error("expected true for different serial, got false")
		}
	})

	t.Run("new headset when selectedHeadset is -1 returns true", func(t *testing.T) {
		deviceManager = devices{}
		selectedDongle = -1
		selectedHeadset = -1

		got := serialNumberCheck(&jabra_DeviceInfo{
			deviceID:     10,
			isDongle:     false,
			serialNumber: "HS001",
		})
		if !got {
			t.Error("expected true for new headset when selectedHeadset==-1, got false")
		}
	})

	t.Run("selectedDongle points to missing device returns false", func(t *testing.T) {
		deviceManager = devices{}
		selectedDongle = 5
		selectedHeadset = -1

		got := serialNumberCheck(&jabra_DeviceInfo{
			deviceID:     3,
			isDongle:     true,
			serialNumber: "DONGLE003",
		})
		if got {
			t.Error("expected false when selectedDongle=5 but not in deviceManager, got true")
		}
	})

	t.Run("existing headset same serial returns false and updates deviceID", func(t *testing.T) {
		deviceManager = devices{
			0: {deviceID: 10, isDongle: false, serialNumber: "HS001", deviceConnection: deviceConnectionType_BT},
		}
		selectedDongle = -1
		selectedHeadset = 0

		got := serialNumberCheck(&jabra_DeviceInfo{
			deviceID:         50,
			isDongle:         false,
			serialNumber:     "HS001",
			deviceConnection: deviceConnectionType_BT,
		})
		if got {
			t.Error("expected false for same headset serial, got true")
		}
		if deviceManager[0].deviceID != 50 {
			t.Errorf("expected deviceID to be updated to 50, got %d", deviceManager[0].deviceID)
		}
	})

	t.Run("existing headset different serial returns true", func(t *testing.T) {
		deviceManager = devices{
			0: {deviceID: 10, isDongle: false, serialNumber: "HS001", deviceConnection: deviceConnectionType_BT},
		}
		selectedDongle = -1
		selectedHeadset = 0

		got := serialNumberCheck(&jabra_DeviceInfo{
			deviceID:         11,
			isDongle:         false,
			serialNumber:     "HS002",
			deviceConnection: deviceConnectionType_BT,
		})
		if !got {
			t.Error("expected true for different headset serial, got false")
		}
	})

	t.Run("selectedHeadset points to missing device returns false", func(t *testing.T) {
		deviceManager = devices{}
		selectedDongle = -1
		selectedHeadset = 5

		got := serialNumberCheck(&jabra_DeviceInfo{
			deviceID:     12,
			isDongle:     false,
			serialNumber: "HS003",
		})
		if got {
			t.Error("expected false when selectedHeadset=5 but not in deviceManager, got true")
		}
	})
}

func TestUpdateStartMenu(t *testing.T) {
	origDeviceManager := deviceManager
	origSelectedDongle := selectedDongle
	origSelectedHeadset := selectedHeadset
	origStartMenu := startMenu
	defer func() {
		deviceManager = origDeviceManager
		selectedDongle = origSelectedDongle
		selectedHeadset = origSelectedHeadset
		startMenu = origStartMenu
	}()

	t.Run("no devices only has Exit", func(t *testing.T) {
		deviceManager = devices{}
		selectedDongle = -1
		selectedHeadset = -1

		updateStartMenu()

		if len(startMenu) != 1 {
			t.Fatalf("expected 1 menu item, got %d: %+v", len(startMenu), startMenu)
		}
		if startMenu[0].id != 5 {
			t.Errorf("expected Exit (id=5), got id=%d label=%q", startMenu[0].id, startMenu[0].label)
		}
	})

	t.Run("dongle exists has Search Settings Exit", func(t *testing.T) {
		deviceManager = devices{
			0: {
				deviceID:     1,
				isDongle:     true,
				deviceName:   "Jabra Link 380",
				serialNumber: "D001",
				featureFlags: &featureFlags{},
			},
		}
		selectedDongle = 0
		selectedHeadset = -1

		updateStartMenu()

		// Expect: Search, Settings, Exit = 3 items
		if len(startMenu) != 3 {
			t.Fatalf("expected 3 menu items, got %d: %+v", len(startMenu), startMenu)
		}
		if startMenu[0].id != 0 {
			t.Errorf("expected Search (id=0), got id=%d", startMenu[0].id)
		}
		if startMenu[1].id != 2 {
			t.Errorf("expected Settings (id=2), got id=%d", startMenu[1].id)
		}
		if startMenu[2].id != 5 {
			t.Errorf("expected Exit (id=5), got id=%d", startMenu[2].id)
		}
	})

	t.Run("dongle with pairingList adds See Remembered Paired Devices", func(t *testing.T) {
		deviceManager = devices{
			0: {
				deviceID:     1,
				isDongle:     true,
				deviceName:   "Jabra Link 380",
				serialNumber: "D001",
				featureFlags: &featureFlags{pairingList: true},
				pairingList: &pairingList{
					count:         2,
					pairedDevices: []pairedDevice{{deviceName: "HS1"}, {deviceName: "HS2"}},
				},
			},
		}
		selectedDongle = 0
		selectedHeadset = -1

		updateStartMenu()

		// Expect: Search, See Remembered, Settings, Exit = 4 items
		if len(startMenu) != 4 {
			t.Fatalf("expected 4 menu items, got %d: %+v", len(startMenu), startMenu)
		}
		if startMenu[1].id != 1 {
			t.Errorf("expected See Remembered Paired Devices (id=1), got id=%d label=%q", startMenu[1].id, startMenu[1].label)
		}
	})

	t.Run("dongle and headset with settings adds headset settings", func(t *testing.T) {
		ds := &deviceSettings{
			items: []settingInfo{{name: "Test"}},
		}
		deviceManager = devices{
			0: {
				deviceID:     1,
				isDongle:     true,
				deviceName:   "Jabra Link 380",
				serialNumber: "D001",
				featureFlags: &featureFlags{},
			},
			1: {
				deviceID:       10,
				isDongle:       false,
				deviceName:     "Jabra Evolve2 85",
				serialNumber:   "HS001",
				deviceSettings: ds,
			},
		}
		selectedDongle = 0
		selectedHeadset = 1

		updateStartMenu()

		// Expect: Search, Settings(dongle), HeadSet Settings, Exit = 4 items
		if len(startMenu) != 4 {
			t.Fatalf("expected 4 menu items, got %d: %+v", len(startMenu), startMenu)
		}
		if startMenu[2].id != 4 {
			t.Errorf("expected HeadSet Settings (id=4), got id=%d label=%q", startMenu[2].id, startMenu[2].label)
		}
	})
}

func TestIsAccessory(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Jabra Evolve2 75 Deskstand", true},
		{"Jabra Charger Stand", true},
		{"My Cradle Device", true},
		{"Jabra Evolve2 85", false},
		{"Jabra Link 380", false},
		{"", false},
		{"DESKSTAND", true},
		{"Jabra Busy Light", true},
		{"Some BusyLight Model", true},
		{"deskstand accessory", true},
		{"Jabra Evolve2 75", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAccessory(tt.name)
			if got != tt.want {
				t.Errorf("isAccessory(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
