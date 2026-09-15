package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/internal/selfupdate"
)

func offerCLIForTest(input io.Reader, output io.Writer, check func() (selfupdate.Plan, error), install func(selfupdate.Plan) error) (bool, error) {
	return checkAndOfferAppUpdate(check, func(plan selfupdate.Plan) (bool, error) {
		return confirmCLIAppUpdate(input, output, plan.Version)
	}, install)
}

func TestStartupAppUpdateOnlyPromptsForInteractiveCommands(t *testing.T) {
	for _, args := range [][]string{nil, {"status"}, {"battery"}, {"firmware", "install", "file.zip"}, {"settings"}, {"sound"}, {"debug"}} {
		if !startupAppUpdateAllowed(args, true, true, true) {
			t.Errorf("interactive command skipped: %v", args)
		}
		for _, streams := range [][3]bool{{false, true, true}, {true, false, true}, {true, true, false}} {
			if startupAppUpdateAllowed(args, streams[0], streams[1], streams[2]) {
				t.Errorf("redirected streams would prompt: %v, %v", args, streams)
			}
		}
	}
	for _, args := range [][]string{{"--version"}, {"--help"}, {"--licenses"}, {"daemon"}, {"--daemon"}, {"-d"}, {"service", "restart"}, {"completion", "bash"}, {"ipc", "version"}, {"setup", "--system"}, {"update"}, {"update", "--check"}, {"status", "--help"}, {"models", "--json"}, {"models", "--json=true"}, {"unknown"}} {
		if startupAppUpdateAllowed(args, true, true, true) {
			t.Errorf("noninteractive command would prompt: %v", args)
		}
	}
}

func TestAppUpdateOfferYesNoAndInputPreservation(t *testing.T) {
	plan := selfupdate.Plan{Version: "1.0.3", ArchiveName: "exact-release.tar.gz", NewerThanCurrent: true}
	for _, test := range []struct {
		name, input string
		wantUpdate  bool
		wantRetry   bool
	}{
		{"yes", "yes\nnext command\n", true, false},
		{"uppercase yes", "YES\nnext command\n", true, false},
		{"short yes", "y\nnext command\n", true, false},
		{"no", "no\nnext command\n", false, false},
		{"short no", "N\nnext command\n", false, false},
		{"default no", "\nnext command\n", false, false},
		{"invalid then yes", "maybe\nyes\nnext command\n", true, true},
		{"long answer", strings.Repeat("y", 100) + "\nno\nnext command\n", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := strings.NewReader(test.input)
			var output bytes.Buffer
			checks, installs := 0, 0
			updated, err := offerCLIForTest(input, &output, func() (selfupdate.Plan, error) {
				checks++
				return plan, nil
			}, func(selected selfupdate.Plan) error {
				installs++
				if !reflect.DeepEqual(selected, plan) {
					t.Fatal("installed a different release from the one offered")
				}
				return nil
			})
			if err != nil || updated != test.wantUpdate || checks != 1 || (installs == 1) != test.wantUpdate || installs > 1 {
				t.Fatalf("updated=%v checks=%d installs=%d error=%v", updated, checks, installs, err)
			}
			if !strings.Contains(output.String(), "A new Jabridge update is available: 1.0.3") || !strings.Contains(output.String(), "Update now? (yes/no) [no]:") {
				t.Fatal("missing update prompt", output.String())
			}
			if strings.Contains(output.String(), "Please enter yes or no") != test.wantRetry {
				t.Fatal("wrong invalid-answer handling", output.String())
			}
			remainder, _ := io.ReadAll(input)
			if string(remainder) != "next command\n" {
				t.Fatalf("prompt consumed input for the next command: %q", remainder)
			}
		})
	}
}

func TestAppUpdateOfferOfflineCurrentAndEOFContinueWithoutInstall(t *testing.T) {
	for _, test := range []struct {
		name string
		plan selfupdate.Plan
		err  error
	}{
		{"offline", selfupdate.Plan{}, errors.New("offline")},
		{"timeout", selfupdate.Plan{}, context.DeadlineExceeded},
		{"current", selfupdate.Plan{Version: "1.0.2"}, nil},
		{"EOF", selfupdate.Plan{Version: "1.0.3", NewerThanCurrent: true}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			updated, err := offerCLIForTest(strings.NewReader(""), &output, func() (selfupdate.Plan, error) { return test.plan, test.err }, func(selfupdate.Plan) error {
				t.Fatal("unexpected installation")
				return nil
			})
			if err != nil || updated || (test.name != "EOF" && output.Len() != 0) {
				t.Fatal(updated, err, output.String())
			}
		})
	}
}

func TestAppUpdateOfferFailureDoesNotReportRestart(t *testing.T) {
	want := errors.New("signature verification failed")
	updated, err := offerCLIForTest(strings.NewReader("yes\n"), io.Discard, func() (selfupdate.Plan, error) {
		return selfupdate.Plan{Version: "1.0.3", NewerThanCurrent: true}, nil
	}, func(selfupdate.Plan) error { return want })
	if updated || !errors.Is(err, want) {
		t.Fatal("failed update was treated as successful", updated, err)
	}
}

func TestAppUpdateOfferWithRealReleaseCheck(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/repos/owner/repo/releases/latest" {
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, `{"tag_name":"v1.0.3","html_url":"https://example.test/release/1.0.3","assets":[{"name":"jabridge_1.0.3_linux_amd64.tar.gz"},{"name":"jabridge_1.0.3_linux_amd64.tar.gz.sha256"},{"name":"jabridge_1.0.3_linux_amd64.tar.gz.sig"}]}`)
	}))
	defer server.Close()
	client := &selfupdate.Client{HTTP: server.Client(), APIBaseURL: server.URL, Repository: "owner/repo", GOOS: "linux", GOARCH: "amd64"}
	var output bytes.Buffer
	updated, err := offerCLIForTest(strings.NewReader("no\n"), &output, func() (selfupdate.Plan, error) {
		return client.Check(context.Background(), "1.0.2", false)
	}, func(selfupdate.Plan) error { t.Fatal("No triggered a download"); return nil })
	if err != nil || updated || requests != 1 || !strings.Contains(output.String(), "1.0.3") {
		t.Fatal(updated, err, requests, output.String())
	}
}

func TestAppUpdateOfferReleaseCheckDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client := &selfupdate.Client{HTTP: server.Client(), APIBaseURL: server.URL, Repository: "owner/repo", GOOS: "linux", GOARCH: "amd64"}
	var output bytes.Buffer
	start := time.Now()
	updated, err := offerCLIForTest(strings.NewReader("yes\n"), &output, func() (selfupdate.Plan, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		return client.Check(ctx, "1.0.2", false)
	}, func(selfupdate.Plan) error { t.Fatal("timed-out check triggered install"); return nil })
	if err != nil || updated || output.Len() != 0 || time.Since(start) > time.Second {
		t.Fatal("unavailable server blocked startup", updated, err)
	}
}

func TestAppUpdateRestartPreservesCommandArguments(t *testing.T) {
	if os.Getenv("JABRIDGE_TEST_UPDATE_RESTART") == "1" {
		if err := restartUpdatedApp(os.Args[0], []string{"-test.run=^TestAppUpdateRestartChild$", "--", "settings", "name with spaces"}); err != nil {
			t.Fatal(err)
		}
		t.Fatal("exec returned")
	}
	command := exec.Command(os.Args[0], "-test.run=^TestAppUpdateRestartPreservesCommandArguments$")
	command.Env = append(os.Environ(), "JABRIDGE_TEST_UPDATE_RESTART=1")
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "RESTART_ARGS_OK") {
		t.Fatalf("restart failed: %s; %v", output, err)
	}
}

func TestAppUpdateRestartChild(t *testing.T) {
	if os.Getenv("JABRIDGE_TEST_UPDATE_RESTART") != "1" {
		t.Skip("restart subprocess only")
	}
	if !reflect.DeepEqual(os.Args[2:], []string{"--", "settings", "name with spaces"}) {
		t.Fatal("restart lost command arguments", os.Args)
	}
	fmt.Println("RESTART_ARGS_OK")
}

func TestAppUpdateRestartAfterExecutableReplacement(t *testing.T) {
	mode := os.Getenv("JABRIDGE_TEST_REPLACED_RESTART")
	if mode == "new" {
		fmt.Println("REPLACED_RESTART_OK")
		return
	}
	if mode == "old" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(executable)
		if err != nil {
			t.Fatal(err)
		}
		// Reproduce the real updater's rename and removal of the running
		// inode. Looking up os.Executable after this returns the backup path.
		backup := executable + ".old"
		if err := os.Rename(executable, backup); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(executable, content, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(backup); err != nil {
			t.Fatal(err)
		}
		t.Setenv("JABRIDGE_TEST_REPLACED_RESTART", "new")
		if err := restartUpdatedApp(executable, []string{"-test.run=^TestAppUpdateRestartAfterExecutableReplacement$"}); err != nil {
			t.Fatal(err)
		}
		t.Fatal("exec returned")
	}
	content, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "jabridge")
	if err := os.WriteFile(path, content, 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(path, "-test.run=^TestAppUpdateRestartAfterExecutableReplacement$")
	command.Env = append(os.Environ(), "JABRIDGE_TEST_REPLACED_RESTART=old")
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "REPLACED_RESTART_OK") {
		t.Fatalf("restart after replacement failed: %s; %v", output, err)
	}
}
