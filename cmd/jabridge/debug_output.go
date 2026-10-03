package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Repeated support commands must produce a fresh report without overwriting
// earlier evidence. Exclusive creation also prevents following a file symlink.
func saveDebugReport(path string, collect func(io.Writer) error) (saved string, err error) {
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	for attempt := 0; attempt < 1000; attempt++ {
		candidate := path
		if attempt > 0 {
			candidate = fmt.Sprintf("%s-%d%s", stem, attempt, ext)
		}
		file, openErr := os.OpenFile(candidate, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(openErr, os.ErrExist) {
			info, statErr := os.Lstat(candidate)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return "", fmt.Errorf("check debug report path: %w", statErr)
			}
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("debug report path %q is not a regular file; choose another --output filename", candidate)
			}
			continue
		}
		if openErr != nil {
			return "", fmt.Errorf("create debug report: %w", openErr)
		}
		defer func() {
			_ = file.Close()
			if err != nil {
				_ = os.Remove(candidate)
			}
		}()
		if err = collect(file); err != nil {
			return "", err
		}
		if err = file.Sync(); err != nil {
			return "", fmt.Errorf("save debug report: %w", err)
		}
		if err = file.Close(); err != nil {
			return "", fmt.Errorf("close debug report: %w", err)
		}
		return candidate, nil
	}
	return "", errors.New("too many existing debug reports; choose another --output filename")
}
