package firmware

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirmwareRecoveryStateTurnsSameArchiveIntoRecovery(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", directory)
	archive := filepath.Join(directory, "firmware.zip")
	if err := os.WriteFile(archive, []byte("firmware"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := &BuildVector{
		ProductName: "Test device", Version: "1.2.3",
		TargetUSBPIDs: []string{"1234"},
	}
	first, err := prepareFirmwareTransfer(archive, manifest)
	if err != nil || first.Recovery || first.State.Attempt != 1 {
		t.Fatalf("first transfer = %#v, %v", first, err)
	}
	if err := saveFirmwareRecoveryState(first.State); err != nil {
		t.Fatal(err)
	}
	second, err := prepareFirmwareTransfer(archive, manifest)
	if err != nil || !second.Recovery || second.State.Attempt != 2 {
		t.Fatalf("recovery transfer = %#v, %v", second, err)
	}
	if err := clearFirmwareRecoveryState(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "jabridge", "firmware-recovery.json")); !os.IsNotExist(err) {
		t.Fatalf("recovery state remains: %v", err)
	}
}

func TestFirmwareRecoveryRejectsDifferentArchive(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", directory)
	firstArchive := filepath.Join(directory, "first.zip")
	secondArchive := filepath.Join(directory, "second.zip")
	if err := os.WriteFile(firstArchive, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondArchive, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := &BuildVector{ProductName: "Test", Version: "1", TargetUSBPIDs: []string{"1234"}}
	first, err := prepareFirmwareTransfer(firstArchive, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveFirmwareRecoveryState(first.State); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareFirmwareTransfer(secondArchive, manifest); err == nil || !strings.Contains(err.Error(), "exact archive") {
		t.Fatalf("different recovery archive error = %v", err)
	}
}

func TestFirmwareRecoveryStateRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", directory)
	statePath, err := firmwareRecoveryStatePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, statePath); err != nil {
		t.Fatal(err)
	}
	state := firmwareRecoveryState{FormatVersion: 1, ArchiveSHA256: strings.Repeat("0", 64)}
	if err := saveFirmwareRecoveryState(state); err == nil {
		t.Fatal("symlink recovery state was accepted")
	}
	if err := clearFirmwareRecoveryState(); err == nil {
		t.Fatal("symlink recovery state was removed")
	}
}
