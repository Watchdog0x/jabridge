package firmware

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallFilePreflightRejectsInvalidInputs(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.zip")
	if err := os.WriteFile(bad, []byte("not firmware"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {filepath.Join(dir, "missing")}, {dir}, {bad}, {bad, "extra"}} {
		if err := ValidateInstallInput(args); err == nil {
			t.Fatalf("accepted invalid input: %v", args)
		}
	}
}

func TestInstallFilePreflightChecksDFUWithoutHardware(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.dfu")
	if err := os.WriteFile(path, syntheticDFU(0x0421, "1.2.3"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInstallInput([]string{path}); err != nil {
		t.Fatal(err)
	}
}
