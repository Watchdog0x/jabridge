package firmware

import (
	"os"
	"path/filepath"
	"testing"
)

func TestControlBindingChecksSerialAndUSBGenerationWithoutHardware(t *testing.T) {
	for _, serial := range []string{"", "SERIAL-A"} {
		device := testBoundUSB(t)
		if serial != "" {
			if err := os.WriteFile(filepath.Join(device.SysPath, "serial"), []byte(serial), 0600); err != nil {
				t.Fatal(err)
			}
		}
		binding, err := BindControlDevice(device)
		if err != nil {
			t.Fatal(err)
		}
		if err := binding.Validate(); err != nil {
			t.Fatal(err)
		}
		if serial != "" {
			if err := os.WriteFile(filepath.Join(device.SysPath, "serial"), []byte("SERIAL-B"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := binding.Validate(); err == nil {
				t.Fatal("serial replacement accepted")
			}
		}
		if err := os.WriteFile(filepath.Join(device.SysPath, "devnum"), []byte("3"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := binding.Validate(); err == nil {
			t.Fatal("new USB attachment accepted")
		}
	}
}
