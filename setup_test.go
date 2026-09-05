package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallDeviceAccessAtomically(t *testing.T) {
	target := filepath.Join(t.TempDir(), "rules", "70-jabridge.rules")
	if err := installDeviceAccess(target); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, embeddedUdevRule) {
		t.Fatal("installed rule does not match the bundled rule")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("rule mode = %o, want 644", info.Mode().Perm())
	}
}

func TestInstallDeviceAccessRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	realPath := filepath.Join(directory, "real")
	if err := os.WriteFile(realPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(directory, "70-jabridge.rules")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}
	if err := installDeviceAccess(linkPath); err == nil {
		t.Fatal("symlink rule target was accepted")
	}
	content, err := os.ReadFile(realPath)
	if err != nil || string(content) != "keep" {
		t.Fatalf("symlink target changed: %q, %v", content, err)
	}
}

func TestJabraHIDUevent(t *testing.T) {
	if !jabraHIDUevent([]byte("HID_ID=0003:00000B0E:000024C7\n")) {
		t.Fatal("Jabra HID event was not recognized")
	}
	if jabraHIDUevent([]byte("HID_ID=0003:00001234:00005678\n")) {
		t.Fatal("non-Jabra HID event was accepted")
	}
}

func TestSetupAnswerDefaultsToYes(t *testing.T) {
	for _, answer := range []string{"", "\n", "y", "Y", "yes", " YES "} {
		if !setupAnswerAccepted(answer) {
			t.Errorf("setup answer %q was not accepted", answer)
		}
	}
	for _, answer := range []string{"n", "no", "later"} {
		if setupAnswerAccepted(answer) {
			t.Errorf("setup answer %q was accepted", answer)
		}
	}
}

func TestSetupRefreshesMissingRuleEvenWhenHidrawIsUsable(t *testing.T) {
	for _, test := range []struct {
		name                               string
		ruleInstalled, hidFound, hidUsable bool
		want                               bool
	}{
		{"missing rule with usable hidraw", false, true, true, true},
		{"missing rule without device", false, false, false, true},
		{"current rule with denied hidraw", true, true, false, true},
		{"current rule with usable hidraw", true, true, true, false},
		{"current rule without device", true, false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := setupNeedsDeviceAccessInstall(test.ruleInstalled, test.hidFound, test.hidUsable); got != test.want {
				t.Fatalf("setup decision = %t, want %t", got, test.want)
			}
		})
	}
}

func TestUserServiceUsesInstalledBinaryAndBootHardening(t *testing.T) {
	service, err := userServiceContents()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]byte{
		[]byte("ExecStart=%h/.local/bin/jabridge --daemon"),
		[]byte("ReadWritePaths=%t"),
		[]byte("ReadWritePaths=-%h/.config/jabridge"),
		[]byte("StateDirectory=jabridge"),
		[]byte("StateDirectoryMode=0700"),
		[]byte("WantedBy=default.target"),
	} {
		if !bytes.Contains(service, want) {
			t.Fatalf("user service is missing %q", want)
		}
	}
}

func TestInstallUserFileRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	realPath := filepath.Join(directory, "real")
	if err := os.WriteFile(realPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(directory, "link")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}
	if err := installUserFile(linkPath, []byte("replace"), 0o755); err == nil {
		t.Fatal("symlink user file was accepted")
	}
}

func TestInstallUserFilesCreatesBinaryCompletionAndService(t *testing.T) {
	homeDirectory := t.TempDir()
	t.Setenv("HOME", homeDirectory)
	executable, err := installUserFiles()
	if err != nil {
		t.Fatal(err)
	}
	wantExecutable := filepath.Join(homeDirectory, ".local", "bin", "jabridge")
	if executable != wantExecutable {
		t.Fatalf("installed executable = %q, want %q", executable, wantExecutable)
	}
	for _, path := range []string{
		wantExecutable,
		filepath.Join(homeDirectory, ".local", "share", "bash-completion", "completions", "jabridge"),
		filepath.Join(homeDirectory, ".config", "systemd", "user", "jabridge.service"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("installed file %s: %v", path, err)
		}
	}
}

func TestDesktopAutostartUsesInstalledBinary(t *testing.T) {
	homeDirectory := t.TempDir()
	t.Setenv("HOME", homeDirectory)
	executable := filepath.Join(homeDirectory, ".local", "bin", "jabridge")
	if err := installDesktopAutostart(executable); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(homeDirectory, ".config", "autostart", "jabridge.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte(`Exec="`+executable+`" --daemon`)) {
		t.Fatalf("autostart command = %q", content)
	}
}

func TestSyncInstalledUserBinary(t *testing.T) {
	homeDirectory := t.TempDir()
	t.Setenv("HOME", homeDirectory)
	source := filepath.Join(t.TempDir(), "jabridge-new")
	if err := os.WriteFile(source, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	target, err := installedUserExecutablePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := installUserFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syncInstalledUserBinary(source); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "new binary" {
		t.Fatalf("synced binary = %q, %v", content, err)
	}
}
