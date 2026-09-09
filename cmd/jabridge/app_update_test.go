package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const updatedCompletionFixture = "# completion from the updated app\ncomplete -W \"status battery\" jabridge\n"

func updateFixture(t *testing.T, completionCommand, restartCommand string) (string, string, string) {
	t.Helper()
	homeDirectory := t.TempDir()
	t.Setenv("HOME", homeDirectory)
	marker := filepath.Join(homeDirectory, "service-restarted")
	t.Setenv("JABRIDGE_TEST_RESTART_MARKER", marker)
	program := filepath.Join(t.TempDir(), "new jabridge")
	script := "#!/bin/sh\ncase \"$1 $2\" in\n" +
		"'completion bash') " + completionCommand + ";;\n" +
		"'service restart') " + restartCommand + ";;\n" +
		"*) exit 91;;\nesac\n"
	if err := os.WriteFile(program, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	completionPath := filepath.Join(homeDirectory, ".local", "share", "bash-completion", "completions", "jabridge")
	if err := installBashCompletion([]byte("# old completion\n")); err != nil {
		t.Fatal(err)
	}
	return program, completionPath, marker
}

func TestAppUpdateRefreshesCompletionForBothServiceStates(t *testing.T) {
	for _, active := range []bool{false, true} {
		name := "stopped"
		if active {
			name = "running"
		}
		t.Run(name, func(t *testing.T) {
			program, completionPath, marker := updateFixture(t,
				`printf '%s\n' '# completion from the updated app' 'complete -W "status battery" jabridge'`,
				`printf restarted > "$JABRIDGE_TEST_RESTART_MARKER"`)
			if err := completeAppUpdate(program, active); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(completionPath)
			if err != nil || string(content) != updatedCompletionFixture {
				t.Fatalf("completion was not taken from the NEW executable: %q, %v", content, err)
			}
			info, err := os.Stat(completionPath)
			if err != nil || info.Mode().Perm() != 0o644 {
				t.Fatalf("completion mode: %v, %v", info, err)
			}
			_, err = os.Stat(marker)
			if active && err != nil {
				t.Fatalf("running service was not restarted: %v", err)
			}
			if !active && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stopped service was touched: %v", err)
			}
			installed, err := installedUserExecutablePath()
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(installed)
			want, readErr := os.ReadFile(program)
			if err != nil || readErr != nil || string(got) != string(want) {
				t.Fatalf("installed binary was not synchronized: %v, %v", err, readErr)
			}
		})
	}
}

func TestAppUpdateRefreshesExistingStoppedInstallation(t *testing.T) {
	program, _, marker := updateFixture(t, "printf '# new completion\\n'", "exit 91")
	installed, err := installedUserExecutablePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := installUserFile(installed, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := completeAppUpdate(program, false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(installed)
	want, readErr := os.ReadFile(program)
	if err != nil || readErr != nil || string(got) != string(want) {
		t.Fatal("stopped installation still uses old binary", err, readErr)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stopped service was restarted", err)
	}
}

func TestAppUpdateCompletionFailurePreservesExistingFile(t *testing.T) {
	for _, command := range []string{"printf partial; exit 7", "exit 0", "printf '  \\n'"} {
		t.Run(command, func(t *testing.T) {
			program, completionPath, marker := updateFixture(t, command, "exit 91")
			if err := completeAppUpdate(program, false); err == nil || !strings.Contains(err.Error(), "completion") {
				t.Fatalf("missing completion failure: %v", err)
			}
			content, err := os.ReadFile(completionPath)
			if err != nil || string(content) != "# old completion\n" {
				t.Fatalf("existing completion damaged: %q, %v", content, err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("service was touched: %v", err)
			}
		})
	}
}

func TestAppUpdateCompletionRejectsSymlink(t *testing.T) {
	program, completionPath, _ := updateFixture(t, "printf new", "exit 91")
	realPath := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(realPath, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(completionPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, completionPath); err != nil {
		t.Fatal(err)
	}
	if err := completeAppUpdate(program, false); err == nil {
		t.Fatal("completion symlink was accepted")
	}
	content, err := os.ReadFile(realPath)
	if err != nil || string(content) != "unchanged" {
		t.Fatalf("symlink target damaged: %q, %v", content, err)
	}
}

func TestAppUpdateRefreshesCompletionEvenIfServiceRestartFails(t *testing.T) {
	program, completionPath, _ := updateFixture(t, "printf new", "exit 9")
	err := completeAppUpdate(program, true)
	if err == nil || !strings.Contains(err.Error(), "restart updated service") {
		t.Fatalf("missing service restart failure: %v", err)
	}
	content, err := os.ReadFile(completionPath)
	if err != nil || string(content) != "new" {
		t.Fatalf("completion depended on service restart: %q, %v", content, err)
	}
}
