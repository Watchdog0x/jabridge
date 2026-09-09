package main

import "testing"

func TestFirmwareHelpAndInvalidCommandsLeaveServiceAlone(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"version"}, {"unknown"}, {"unknown", "--help"}, {"install", "--help"}, {"verify", "-h"}, {"download", "--help"}, {"manifest", "file.zip"}, {"detect", "file.zip"}} {
		if firmwareCommandNeedsHardware(args) {
			t.Errorf("%v unnecessarily interrupts the service", args)
		}
	}
	for _, args := range [][]string{nil, {"status"}, {"check"}, {"install", "file.zip"}, {"verify", "file.zip"}, {"download"}} {
		if !firmwareCommandNeedsHardware(args) {
			t.Errorf("%v bypasses hardware coordination", args)
		}
	}
}
