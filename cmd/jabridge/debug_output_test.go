package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/buildinfo"
)

func TestDebugOutputKeepsOldReportAndSavesFreshReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jabridge-debug.txt")
	old := "Jabridge debug 1.0.0\nold report\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		fresh := fmt.Sprintf("Jabridge debug %s\nreport %d\n", buildinfo.Version, i)
		saved, err := saveDebugReport(path, func(w io.Writer) error {
			_, err := io.WriteString(w, fresh)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(filepath.Dir(path), fmt.Sprintf("jabridge-debug-%d.txt", i))
		if saved != want {
			t.Fatalf("saved %q, want %q", saved, want)
		}
		contents, err := os.ReadFile(saved)
		if err != nil || string(contents) != fresh {
			t.Fatalf("fresh report: %q, %v", contents, err)
		}
		info, err := os.Stat(saved)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("report permissions: %v, %v", info, err)
		}
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != old {
		t.Fatalf("old report was changed: %q, %v", contents, err)
	}
}

func TestDebugOutputFailureRemovesOnlyNewReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.txt")
	if err := os.WriteFile(path, []byte("keep this"), 0o600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("collection interrupted")
	saved, err := saveDebugReport(path, func(w io.Writer) error {
		_, _ = io.WriteString(w, "incomplete report")
		return failure
	})
	if !errors.Is(err, failure) || saved != "" {
		t.Fatal(saved, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != "debug.txt" {
		t.Fatalf("failed report left behind: %v, %v", entries, err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "keep this" {
		t.Fatal(string(contents), err)
	}
}

func TestDebugOutputRejectsSymlinkWithoutCollecting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "private.txt")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "debug.txt")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	_, err := saveDebugReport(path, func(io.Writer) error {
		t.Fatal("must reject the path before collecting diagnostics")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "unchanged" {
		t.Fatal(string(contents), err)
	}
}

func TestDebugOutputConcurrentReportsHaveSeparateFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.txt")
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Go(func() {
			content := fmt.Sprintf("report %d", i)
			saved, err := saveDebugReport(path, func(w io.Writer) error {
				_, err := io.WriteString(w, content)
				return err
			})
			if err != nil {
				t.Error(err)
				return
			}
			actual, err := os.ReadFile(saved)
			if err != nil || string(actual) != content {
				t.Errorf("report mixed with another run: %q, %v", actual, err)
			}
		})
	}
	group.Wait()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 8 {
		t.Fatalf("expected 8 separate reports: %v, %v", entries, err)
	}
}
