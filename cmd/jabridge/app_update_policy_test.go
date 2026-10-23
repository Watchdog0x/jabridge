//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/buildinfo"
	"github.com/Watchdog0x/jabridge/internal/selfupdate"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type updatePolicyTransport struct {
	calls atomic.Int32
}

var errUpdatePolicyProbe = errors.New("app update request intercepted by test")

func (p *updatePolicyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	p.calls.Add(1)
	return nil, errUpdatePolicyProbe
}

func useUpdatePolicyTransport(t *testing.T) *updatePolicyTransport {
	t.Helper()
	probe := &updatePolicyTransport{}
	original := http.DefaultTransport
	http.DefaultTransport = probe
	t.Cleanup(func() { http.DefaultTransport = original })
	return probe
}

func useUpdatePolicyTerminal(t *testing.T) *os.File {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "update-policy-pty")
	t.Cleanup(func() { _ = master.Close() })
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	stdin, stdout, stderr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = slave, slave, slave
	t.Cleanup(func() { os.Stdin, os.Stdout, os.Stderr = stdin, stdout, stderr })
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
		t.Fatal("test did not provide three terminal streams")
	}
	return master
}

// This test also runs in binaries built with -ldflags -X. It does not replace
// SelfUpdate, so those runs exercise the policy actually linked by a packager.
func TestAppUpdateBuildPolicy(t *testing.T) {
	if expected, ok := os.LookupEnv("JABRIDGE_TEST_EXPECT_SELF_UPDATE"); ok && buildinfo.SelfUpdate != expected {
		t.Fatalf("linked SelfUpdate = %q, want %q", buildinfo.SelfUpdate, expected)
	}
	probe := useUpdatePolicyTransport(t)
	terminal := useUpdatePolicyTerminal(t)
	for _, args := range [][]string{nil, {"status"}} {
		probe.calls.Store(0)
		if err := offerStartupAppUpdate(args); err != nil {
			t.Fatal(err)
		}
		want := int32(0)
		if buildinfo.SelfUpdateEnabled() {
			want = 1 // An offline update check allows startup to continue.
		}
		if got := probe.calls.Load(); got != want {
			t.Fatalf("startup %v made %d app update requests, want %d", args, got, want)
		}
		poll := []unix.PollFd{{Fd: int32(terminal.Fd()), Events: unix.POLLIN}}
		if _, err := unix.Poll(poll, 0); err != nil {
			t.Fatal(err)
		}
		if poll[0].Revents&unix.POLLIN != 0 {
			t.Fatal("startup unexpectedly printed an update prompt")
		}
	}
	updateArgs := [][]string{{"--check"}, {"--check", "--prerelease"}}
	if !buildinfo.SelfUpdateEnabled() {
		updateArgs = append(updateArgs, nil, []string{"--prerelease"})
	}
	for _, args := range updateArgs {
		probe.calls.Store(0)
		err := runUpdate(args)
		if buildinfo.SelfUpdateEnabled() {
			if !errors.Is(err, errUpdatePolicyProbe) || probe.calls.Load() != 1 {
				t.Fatalf("enabled update %v: error=%v, requests=%d", args, err, probe.calls.Load())
			}
		} else if !errors.Is(err, errAppUpdateDisabled) || probe.calls.Load() != 0 {
			t.Fatalf("disabled update %v: error=%v, requests=%d", args, err, probe.calls.Load())
		}
	}
	probe.calls.Store(0)
	if err := runUpdate([]string{"--help"}); err != nil || probe.calls.Load() != 0 {
		t.Fatalf("update help: error=%v, requests=%d", err, probe.calls.Load())
	}
	for _, args := range [][]string{{"--unknown-option"}, {"unexpected"}} {
		err := runUpdate(args)
		if err == nil || errors.Is(err, errAppUpdateDisabled) || probe.calls.Load() != 0 {
			t.Fatalf("invalid update arguments %v: error=%v, requests=%d", args, err, probe.calls.Load())
		}
	}
}

func TestDisabledAppUpdateStopsBeforeSharedInstaller(t *testing.T) {
	original := buildinfo.SelfUpdate
	t.Cleanup(func() { buildinfo.SelfUpdate = original })
	// A missing service socket and empty PATH also isolate a failed guard from
	// the user's service manager. The empty plan can never replace a binary.
	t.Setenv("JABRIDGE_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
	t.Setenv("PATH", t.TempDir())
	probe := useUpdatePolicyTransport(t)
	for _, mode := range []string{"disabled", "", "typo"} {
		buildinfo.SelfUpdate = mode
		var output bytes.Buffer
		err := installAppUpdate(context.Background(), nil, selfupdate.Plan{}, &output)
		if !errors.Is(err, errAppUpdateDisabled) || output.Len() != 0 || probe.calls.Load() != 0 {
			t.Fatalf("policy %q reached installer: error=%v, output=%q, requests=%d", mode, err, output.String(), probe.calls.Load())
		}
	}
}
