package main

import "testing"

func TestDeviceKindLabelDoesNotCallEveryJabraDeviceAHeadset(t *testing.T) {
	cases := []struct {
		name     string
		isDongle bool
		want     string
	}{
		{"Jabra Link 380", true, "Dongle"},
		{"Jabra Speak2 75", false, "Speakerphone"},
		{"Jabra PanaCast 50", false, "Camera/room device"},
		{"Jabra Scheduler", false, "Room scheduler"},
		{"Jabra Evolve3 85", false, "Headset"},
		{"Unknown Jabra product", false, "Device"},
	}
	for _, test := range cases {
		device := &jabra_DeviceInfo{deviceName: test.name, isDongle: test.isDongle}
		if got := deviceKindLabel(device); got != test.want {
			t.Errorf("%q = %q, want %q", test.name, got, test.want)
		}
	}
}
