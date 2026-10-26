package main

import (
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/btsearch"
)

func TestDualSearchScopeAndReadableChoices(t *testing.T) {
	for _, pid := range []uint16{0x24c7, 0x24c8, 0x2e50, 0x2e51, 0x2e56, 0x2e57} {
		if got := usesDualModeSearch(&jabra_DeviceInfo{isDongle: true, productID: pid}); got != (pid == 0x2e57) {
			t.Fatalf("unexpected dual-mode scope %04x", pid)
		}
	}
	for _, kind := range []byte{0, 1} {
		name := typedSearchName(btsearch.Device{Name: "Headset", BluetoothType: kind})
		if !strings.Contains(name, "Headset") || strings.Contains(name, "LE") != (kind == 1) {
			t.Fatal(name)
		}
	}
}
