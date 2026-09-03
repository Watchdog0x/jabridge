package main

import "testing"

func TestOnlyHardwareCommandsPauseLegacyDirectAccess(t *testing.T) {
	for _, command := range []string{"status", "battery", "settings", "model", "firmware", "fw"} {
		if !commandNeedsDirectHardware(command) {
			t.Errorf("%s did not request exclusive hardware access", command)
		}
	}
	for _, command := range []string{"service", "ipc", "setup", "sound", "update", "completion"} {
		if commandNeedsDirectHardware(command) {
			t.Errorf("%s unnecessarily requested direct hardware access", command)
		}
	}
}
