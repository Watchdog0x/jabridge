package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/buildinfo"
	"github.com/Watchdog0x/jabridge/internal/history"
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
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", filepath.Join(home, ".local", "state"))
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
	if err := checkNoSetupFiles(home); err != nil {
		t.Fatal(err)
	}
}

func checkNoSetupFiles(home string) error {
	for _, path := range setupPolicyPaths(home) {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("setup installed %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check setup path: %w", err)
		}
	}
	return nil
}

func TestSetupFileCheckRejectsUnreadablePath(t *testing.T) {
	home := t.TempDir()
	// A regular file in place of a directory produces ENOTDIR even as root.
	if err := os.WriteFile(filepath.Join(home, ".local"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkNoSetupFiles(home); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("setup file check ignored an invalid parent: %v", err)
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

func TestPackagedTUIConnects(t *testing.T) {
	original := buildinfo.Setup
	t.Cleanup(func() { buildinfo.Setup = original })
	for _, test := range []struct {
		name         string
		setup        string
		version      string
		history      history.RecordingStatus
		connects     bool
		legacyRepair bool
	}{
		{name: "matching", setup: "disabled", version: buildinfo.Version, connects: true},
		{name: "history-read-only", setup: "disabled", version: buildinfo.Version, history: history.RecordingStatus{Enabled: true, Error: "read-only-filesystem"}, connects: true},
		{name: "mismatch", setup: "disabled", version: "old-test-version"},
		{name: "default-matching", setup: "enabled", version: buildinfo.Version, connects: true},
		{name: "default-read-only", setup: "enabled", version: buildinfo.Version, history: history.RecordingStatus{Enabled: true, Error: "read-only-filesystem"}, legacyRepair: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			buildinfo.Setup = test.setup
			home := useSetupPolicyHome(t)
			if test.legacyRepair {
				// Preserve the old migration path, but stop at the home lookup
				// before it can copy files or try to start any service.
				t.Setenv("HOME", "")
			}
			listener, err := net.Listen("unix", ipcSocketPath())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				decoder, encoder := json.NewDecoder(conn), json.NewEncoder(conn)
				for {
					var req ipc.Request
					if err := decoder.Decode(&req); err != nil {
						if errors.Is(err, io.EOF) {
							err = nil
						}
						done <- err
						return
					}
					var result any
					switch req.Method {
					case "version":
						result = map[string]string{"version": test.version}
					case "history.status":
						result = test.history
					default:
						done <- fmt.Errorf("unexpected service call %q", req.Method)
						return
					}
					if err := encoder.Encode(ipc.SuccessResponse(req.ID, result)); err != nil {
						done <- err
						return
					}
				}
			}()
			var output bytes.Buffer
			backend, err := connectTUIServiceWithOutput(&output)
			if backend != nil {
				backend.close()
			}
			_ = listener.Close()
			if test.connects {
				if backend == nil || err != nil {
					t.Errorf("matching packaged service: backend=%v, error=%v", backend, err)
				}
			} else if test.legacyRepair {
				if backend != nil || err == nil || !strings.Contains(err.Error(), "install user service: find user home:") {
					t.Errorf("default service migration changed: backend=%v, error=%v", backend, err)
				}
			} else if backend != nil || !errors.Is(err, errPackagedServiceNotRunning) {
				t.Errorf("mismatched packaged service: backend=%v, error=%v", backend, err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("fake service did not stop")
			}
			if !test.legacyRepair && output.Len() != 0 {
				t.Fatalf("packaged service triggered setup output: %q", output.String())
			}
			requireNoSetupFiles(t, home)
		})
	}
}
