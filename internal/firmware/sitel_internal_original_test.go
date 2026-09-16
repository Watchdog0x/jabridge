package firmware

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"
)

func TestLocalEngage75OriginalBluetoothSimulation(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_ENGAGE75_ARCHIVE")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_ENGAGE75_ARCHIVE for full original Bluetooth image simulation")
	}
	manifest, files, err := parseGnVArchive(path)
	if err != nil || manifest.Version != "5.20.1" {
		t.Fatal("unexpected original Engage 75 archive", err)
	}
	for _, revision := range []uint16{0x28, 0x35} {
		s := newInternalFlashSim(revision)
		protected := append([]uint16(nil), s.cpu.flash[251*4096:]...)
		slow, fast := internalFlashWireTransport(t, s)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		f, err := prepareSitelInternalFlash(ctx, slow, fast, s.sleep)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		image, err := parseSitelBluecoreImages(files["Jabra_Engage_75_bts_5.20.1.xpv"], files["Jabra_Engage_75_bts_5.20.1.xdv"], f.chip)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if err := f.transfer(ctx, image, func(uint16) error { return nil }, nil); err != nil {
			cancel()
			t.Fatal(err)
		}
		for sector, want := range image.Sectors {
			if !slices.Equal(s.cpu.flash[int(sector)*4096:int(sector+1)*4096], want) {
				t.Fatal("original image readback differs", f.chip, sector)
			}
		}
		if !slices.Equal(s.cpu.flash[251*4096:], protected) {
			t.Fatal("reserved flash was changed", f.chip)
		}
		if err := f.bootApplication(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		t.Logf("%s: %d original sectors, %d programmed words, reserved sectors unchanged; HID/SPI/own CPU program/controller simulation", f.chip, len(image.Sectors), s.cpu.programs)
		cancel()
	}
}
