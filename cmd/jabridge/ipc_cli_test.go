package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIPCSocketPathOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "test.sock")
	t.Setenv("JABRIDGE_SOCKET", want)
	if got := ipcSocketPath(); got != want {
		t.Fatalf("socket path = %q, want %q", got, want)
	}
}

func TestDefaultIPCSocketPath(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("JABRIDGE_SOCKET", "")
	t.Setenv("XDG_RUNTIME_DIR", directory)
	if got, want := ipcSocketPath(), filepath.Join(directory, "jabridge.sock"); got != want {
		t.Fatalf("socket path = %q, want %q", got, want)
	}
}

func TestPrintIPCJSON(t *testing.T) {
	oldStdout := os.Stdout
	file, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = file
	t.Cleanup(func() {
		os.Stdout = oldStdout
		_ = file.Close()
	})
	if err := printIPCJSON(map[string]bool{"ok": true}); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "{\n  \"ok\": true\n}\n" {
		t.Fatalf("JSON output = %q", content)
	}
}
