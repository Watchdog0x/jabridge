package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestButtonInputFailuresOnlyPrescribeSetupForDeniedOpens(t *testing.T) {
	for _, test := range []struct {
		name  string
		errs  []error
		want  string
		setup bool
	}{
		{"no nodes", nil, "no Jabra Linux input nodes", false},
		{"permission", []error{os.ErrPermission}, "access was denied", true},
		{"EPERM", []error{syscall.EPERM}, "access was denied", true},
		{"wrapped permission", []error{&os.PathError{Path: "PRIVATE", Err: os.ErrPermission}}, "access was denied", true},
		{"gone", []error{os.ErrNotExist}, "disappeared", false},
		{"disconnected", []error{syscall.ENODEV}, "disappeared", false},
		{"busy", []error{syscall.EBUSY}, "could not be opened", false},
		{"other", []error{errors.New("PRIVATE")}, "could not be opened", false},
		{"mixed", []error{syscall.ENODEV, os.ErrPermission}, "access was denied", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := buttonInputUnavailable(test.errs)
			if !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "run jabridge setup") != test.setup || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal(err)
			}
		})
	}
}

func TestButtonObserverEmptyAndDisappearedNodesDoNotPrescribeSetup(t *testing.T) {
	for _, paths := range [][]string{nil, {filepath.Join(t.TempDir(), "event-missing")}} {
		var messages []string
		err := observeButtonEvents(context.Background(), paths, func(string, uint16, uint16, int32) { t.Fatal("unexpected event") }, func(s string) { messages = append(messages, s) })
		if err == nil || strings.Contains(err.Error()+strings.Join(messages, "\n"), "run jabridge setup") {
			t.Fatal(err, messages)
		}
		if len(paths) == 0 && !errors.Is(err, errNoJabraInputNodes) {
			t.Fatal(err)
		}
	}
}

func TestButtonObserverRealPermissionDenialAndPartialAccess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can bypass test file permissions")
	}
	dir := t.TempDir()
	denied, ready := filepath.Join(dir, "event-denied"), filepath.Join(dir, "event-ready")
	for name, mode := range map[string]os.FileMode{denied: 0, ready: 0o600} {
		if err := os.WriteFile(name, nil, mode); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var messages []string
	diagnostic := func(s string) { messages = append(messages, s) }
	emit := func(string, uint16, uint16, int32) { t.Fatal("unexpected event") }
	err := observeButtonEvents(ctx, []string{denied}, emit, diagnostic)
	if err == nil || !strings.Contains(err.Error(), "run jabridge setup") || !strings.Contains(strings.Join(messages, "\n"), "permission denied") {
		t.Fatal(err, messages)
	}
	if err := observeButtonEvents(ctx, []string{denied, ready}, emit, diagnostic); err != nil {
		t.Fatalf("one accessible input should still be observed: %v", err)
	}
}
