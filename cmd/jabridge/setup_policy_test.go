package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/buildinfo"
)

// useSetupEnabled lets a test exercise the installers in a binary linked with
// -ldflags -X Setup=disabled, which is how packagers run these tests.
func useSetupEnabled(t *testing.T) {
	t.Helper()
	original := buildinfo.Setup
	buildinfo.Setup = "enabled"
	t.Cleanup(func() { buildinfo.Setup = original })
}

// useSetupPolicyHome keeps a test away from the user's files and service
// manager. The empty PATH also stops a failed guard from reaching sudo, pkexec
// or systemctl.
func useSetupPolicyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("JABRIDGE_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
	t.Setenv("PATH", t.TempDir())
	return home
}

// setupPolicyPaths lists everything setup and the service commands can write
// below the home directory.
func setupPolicyPaths(home string) []string {
	return []string{
		filepath.Join(home, ".local", "bin", "jabridge"),
		filepath.Join(home, ".local", "share", "bash-completion", "completions", "jabridge"),
		filepath.Join(home, ".config", "systemd", "user", "jabridge.service"),
		filepath.Join(home, ".config", "autostart", "jabridge.desktop"),
		filepath.Join(home, ".config", "jabridge"),
	}
}

func requireNoSetupFiles(t *testing.T, home string) {
	t.Helper()
	for _, path := range setupPolicyPaths(home) {
		if _, err := os.Lstat(path); err == nil {
			t.Fatalf("setup installed %s", path)
		}
	}
}

// This test also runs in binaries built with -ldflags -X. It does not replace
// Setup, so those runs exercise the policy actually linked by a packager.
func TestSetupBuildPolicy(t *testing.T) {
	if expected, ok := os.LookupEnv("JABRIDGE_TEST_EXPECT_SETUP"); ok && buildinfo.Setup != expected {
		t.Fatalf("linked Setup = %q, want %q", buildinfo.Setup, expected)
	}
	home := useSetupPolicyHome(t)
	running, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := prepareUserService()
	if err != nil {
		t.Fatal(err)
	}
	if buildinfo.SetupEnabled() {
		installed := filepath.Join(home, ".local", "bin", "jabridge")
		if executable != installed {
			t.Fatalf("service executable = %q, want %q", executable, installed)
		}
		if _, err := os.Stat(installed); err != nil {
			t.Fatalf("installed executable: %v", err)
		}
	} else {
		if executable != running {
			t.Fatalf("service executable = %q, want the running %q", executable, running)
		}
		requireNoSetupFiles(t, home)
	}
	// Access that is present or absent from the system needs no offer.
	for _, access := range [][2]bool{{false, false}, {true, true}} {
		if err := offerDeviceAccessSetupFor(access[0], access[1]); err != nil {
			t.Fatalf("access %v: %v", access, err)
		}
	}
	if !buildinfo.SetupEnabled() {
		// A terminal would otherwise be offered a prompt, so only a disabled
		// build can safely check the missing-access case here.
		if err := offerDeviceAccessSetupFor(true, false); !errors.Is(err, errSetupDisabled) {
			t.Fatalf("missing access offer: %v", err)
		}
	}
}

func TestDisabledSetupStopsBeforeInstallers(t *testing.T) {
	original := buildinfo.Setup
	t.Cleanup(func() { buildinfo.Setup = original })
	for _, mode := range []string{"disabled", "", "typo"} {
		t.Run(mode, func(t *testing.T) {
			buildinfo.Setup = mode
			home := useSetupPolicyHome(t)
			if _, err := installUserFiles(); !errors.Is(err, errSetupDisabled) {
				t.Fatalf("installUserFiles: %v", err)
			}
			// Every form of the command stops first, including the one that
			// would run a privileged helper.
			for _, args := range [][]string{nil, {"--force"}, {"--system"}, {"unexpected"}} {
				if err := runSetup(args); !errors.Is(err, errSetupDisabled) {
					t.Fatalf("setup %v: %v", args, err)
				}
			}
			if err := offerDeviceAccessSetupFor(true, false); !errors.Is(err, errSetupDisabled) {
				t.Fatalf("missing access offer: %v", err)
			}
			// The updated executable is never run, and nothing is refreshed.
			if err := completeAppUpdate(filepath.Join(home, "absent"), true); err != nil {
				t.Fatalf("complete app update: %v", err)
			}
			running, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			for name, find := range map[string]func() (string, error){
				"prepare service":      prepareUserService,
				"installed executable": installedUserExecutable,
			} {
				if executable, err := find(); err != nil || executable != running {
					t.Fatalf("%s: executable=%q, error=%v, want %q", name, executable, err, running)
				}
			}
			requireNoSetupFiles(t, home)
		})
	}
}

// Setup help stays available so users can read why the command refuses.
func TestDisabledSetupKeepsHelp(t *testing.T) {
	original := buildinfo.Setup
	t.Cleanup(func() { buildinfo.Setup = original })
	buildinfo.Setup = "disabled"
	home := useSetupPolicyHome(t)
	for _, flag := range []string{"--help", "-h", "help"} {
		if err := runSetup([]string{flag}); err != nil {
			t.Fatalf("setup %s: %v", flag, err)
		}
	}
	requireNoSetupFiles(t, home)
}

func TestDisabledSetupLeavesTerminalMenuServiceToPackage(t *testing.T) {
	original := buildinfo.Setup
	t.Cleanup(func() { buildinfo.Setup = original })
	buildinfo.Setup = "disabled"
	home := useSetupPolicyHome(t)
	var output bytes.Buffer
	backend, err := connectTUIServiceWithOutput(&output)
	if backend != nil {
		backend.close()
		t.Fatal("terminal menu connected without a service")
	}
	if !errors.Is(err, errPackagedServiceNotRunning) || output.Len() != 0 {
		t.Fatalf("terminal menu: error=%v, output=%q", err, output.String())
	}
	requireNoSetupFiles(t, home)
}
