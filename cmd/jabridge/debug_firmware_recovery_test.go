package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/firmware"
)

func TestFirmwareDebugExplainsUpdateModeAndSavedStage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", dir)
	if err := os.Mkdir(filepath.Join(dir, "jabridge"), 0o700); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{
		"formatVersion": 1, "archiveSha256": strings.Repeat("a", 64), "productName": "PRIVATE_NAME", "firmwareVersion": "2.11.1",
		"targetUsbPids": []string{"0x0E44"}, "attempt": 1, "protocol": 4, "runtimePid": 0x0e41, "bootPid": 0x0e44,
		"usbPort": "PRIVATE_PORT", "phase": "entering-bootloader", "targetIdentitySha256": strings.Repeat("b", 64),
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jabridge", "firmware-recovery.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	oldCheck := checkSitelBootAccess
	checkSitelBootAccess = func(uint16) error { return nil }
	t.Cleanup(func() { checkSitelBootAccess = oldCheck })
	old := diagnoseFirmwareFile
	t.Cleanup(func() { diagnoseFirmwareFile = old })
	diagnoseFirmwareFile = func(context.Context, uint16, string) (firmware.FirmwareDiagnostic, error) {
		return firmware.FirmwareDiagnostic{Latest: firmware.LatestInfo{ProductID: 0x0e44, Version: "2.11.1"}, Protocols: []int{4}, ChecksumPublished: true}, nil
	}
	var report bytes.Buffer
	writeFirmwareDiagnostic(&report, []uint16{0x0e44})
	text := report.String()
	for _, want := range []string{"is in firmware update mode", "runtime PID 0x0e41", "firmware 2.11.1", "last saved stage entering-bootloader", "Native update interface check: ready", "no device commands sent"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	for _, private := range []string{"PRIVATE", strings.Repeat("a", 64), strings.Repeat("b", 64), "version format unrecognized"} {
		if strings.Contains(text, private) {
			t.Fatalf("unexpected report content %q", private)
		}
	}
	steps := strings.Join(reportNextSteps(text+"\nhidraw4: no supported management usage FF00:0001"), "\n")
	if !strings.Contains(steps, "missing audio and normal controls are expected") || strings.Contains(steps, "extend transport support") || strings.Contains(steps, "run jabridge setup") {
		t.Fatal(steps)
	}
}
