package firmware

import (
	"strings"
	"testing"
)

func TestSettingsLeaseContendsAndReleasesIndependently(t *testing.T) {
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	first, err := AcquireSettingsWriteLease()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if second, err := AcquireSettingsWriteLease(); err == nil {
		_ = second.Close()
		t.Fatal("independent settings opens did not conflict")
	} else if !strings.Contains(err.Error(), "another Jabridge change") {
		t.Fatal("unhelpful busy error", err)
	}
	// These lock different files. A production settings transaction also holds
	// shared device access, which intentionally blocks a firmware install.
	firmware, err := acquireFirmwareInstallLock()
	if err != nil {
		t.Fatal("settings lock collided with firmware lock", err)
	}
	_ = firmware.Close()
	device, err := AcquireDeviceAccess()
	if err != nil {
		t.Fatal("settings lock collided with normal reads", err)
	}
	_ = device.Close()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	last, err := AcquireSettingsWriteLease()
	if err != nil {
		t.Fatal("settings lock stayed held after close", err)
	}
	_ = last.Close()
}
