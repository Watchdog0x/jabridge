package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOnlyHardwareCommandsPauseLegacyDirectAccess(t *testing.T) {
	for _, command := range []string{"status", "battery", "diagnose", "settings", "model", "firmware", "fw"} {
		if !commandNeedsDirectHardware(command) {
			t.Errorf("%s did not request exclusive hardware access", command)
		}
	}
	for _, command := range []string{"service", "ipc", "setup", "sound", "use", "update", "completion", "models"} {
		if commandNeedsDirectHardware(command) {
			t.Errorf("%s unnecessarily requested direct hardware access", command)
		}
	}
}

func TestUpdateRestartsThroughNewExecutableToRefreshEmbeddedUnit(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "new jabridge")
	marker := filepath.Join(dir, "new-unit-installed")
	t.Setenv("JABRIDGE_TEST_RESTART_MARKER", marker)
	if err := os.WriteFile(program, []byte("#!/bin/sh\n[ \"$1\" = service ] && [ \"$2\" = restart ] || exit 9\nprintf 'new unit' > \"$JABRIDGE_TEST_RESTART_MARKER\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := restartUsingUpdatedBinary(program); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "new unit" {
		t.Fatal(string(data), err)
	}
}
