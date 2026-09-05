package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
)

const systemUdevRulePath = "/etc/udev/rules.d/70-jabridge.rules"

//go:embed dist/70-jabridge.rules
var embeddedUdevRule []byte

//go:embed dist/jabridge.service
var embeddedUserService []byte

//go:embed internal/completion/jabridge.bash
var embeddedBashCompletion []byte

func offerDeviceAccessSetup() error {
	found, usable := probeJabraHidrawAccess()
	if !found || usable {
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("one-time device access is needed; run: jabridge setup")
	}
	fmt.Print("Jabridge needs one-time access to your headset. Set it up now? [Y/n] ")
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read setup answer: %w", err)
	}
	if !setupAnswerAccepted(answer) {
		return errors.New("setup skipped; run 'jabridge setup' when you are ready")
	}
	return runSetup(nil)
}

func setupAnswerAccepted(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "", "y", "yes":
		return true
	default:
		return false
	}
}

func runSetup(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		printSetupUsage()
		return nil
	}
	if len(args) == 1 && args[0] == "--system" {
		if os.Geteuid() != 0 {
			return errors.New("system setup requires administrator access")
		}
		if err := installDeviceAccess(systemUdevRulePath); err != nil {
			return err
		}
		return reloadAndTriggerUdev()
	}
	if len(args) != 0 {
		return errors.New("usage: jabridge setup")
	}
	installedExecutable, err := installUserFiles()
	if err != nil {
		return err
	}

	found, usable := probeJabraHidrawAccess()
	// An older rule may make hidraw usable while still omitting input-event
	// access. Always refresh a missing/outdated rule instead of treating one
	// accessible node as proof that the complete setup is installed.
	if setupNeedsDeviceAccessInstall(deviceAccessRuleInstalled(), found, usable) {
		if os.Geteuid() == 0 {
			if err := installDeviceAccess(systemUdevRulePath); err != nil {
				return err
			}
			if err := reloadAndTriggerUdev(); err != nil {
				return err
			}
		} else if err := runPrivilegedSetup(installedExecutable); err != nil {
			return err
		}
	}

	time.Sleep(300 * time.Millisecond)
	found, usable = probeJabraHidrawAccess()
	inputFound, inputUsable := probeJabraInputAccess()
	if found && usable && (!inputFound || inputUsable) {
		fmt.Println("Device access is ready. Starting your user service...")
	}
	if found && usable && inputFound && !inputUsable {
		fmt.Println("Device control is ready. Reconnect USB once for button access. Starting your user service...")
	}
	if found && !usable {
		fmt.Println("Access rule installed. Reconnect USB once. Starting your user service...")
	}
	if err := enableUserService(installedExecutable); err != nil {
		return err
	}
	fmt.Println("Jabridge is installed and will start automatically when you sign in.")
	switch {
	case found && usable && (!inputFound || inputUsable):
		fmt.Println("Device access is ready.")
	case found && usable:
		fmt.Println("Device control is ready. Reconnect USB once for button access.")
	case found:
		fmt.Println("Setup complete. Reconnect the USB device once.")
	default:
		fmt.Println("Setup complete. Connect your Jabra USB device.")
	}
	return nil
}

func probeJabraInputAccess() (found, usable bool) {
	for _, path := range jabraInputPaths() {
		found = true
		file, err := os.Open(path)
		if err == nil {
			_ = file.Close()
			return true, true
		}
	}
	return found, false
}

func setupNeedsDeviceAccessInstall(ruleInstalled, hidrawFound, hidrawUsable bool) bool {
	return !ruleInstalled || hidrawFound && !hidrawUsable
}

func runPrivilegedSetup(executable string) error {
	if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
		executable = resolved
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("jabridge executable is not a regular file: %s", executable)
	}

	var command *exec.Cmd
	if graphicalSession() {
		if helper, lookupErr := exec.LookPath("pkexec"); lookupErr == nil {
			command = exec.Command(helper, executable, "setup", "--system")
		}
	}
	if command == nil {
		helper, lookupErr := exec.LookPath("sudo")
		if lookupErr != nil {
			return errors.New("no authorization helper found; install polkit or sudo")
		}
		command = exec.Command(helper, "--", executable, "setup", "--system")
	}
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("device access setup failed: %w", err)
	}
	return nil
}

func installUserFiles() (string, error) {
	if os.Geteuid() == 0 {
		return "", errors.New("run 'jabridge setup' as your normal user, without sudo")
	}
	homeDirectory, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find Jabridge executable: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
		executable = resolved
	}
	binaryData, err := os.ReadFile(executable)
	if err != nil {
		return "", fmt.Errorf("read Jabridge executable: %w", err)
	}
	installedExecutable := filepath.Join(homeDirectory, ".local", "bin", "jabridge")
	if err := installUserFile(installedExecutable, binaryData, 0o755); err != nil {
		return "", err
	}
	completionPath := filepath.Join(homeDirectory, ".local", "share", "bash-completion", "completions", "jabridge")
	if err := installUserFile(completionPath, embeddedBashCompletion, 0o644); err != nil {
		return "", err
	}
	service, err := userServiceContents()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(homeDirectory, ".config", "jabridge"), 0o700); err != nil {
		return "", fmt.Errorf("create connection settings directory: %w", err)
	}
	servicePath := filepath.Join(homeDirectory, ".config", "systemd", "user", "jabridge.service")
	if err := installUserFile(servicePath, service, 0o644); err != nil {
		return "", err
	}
	return installedExecutable, nil
}

func syncInstalledUserBinary(source string) error {
	target, err := installedUserExecutablePath()
	if err != nil {
		return err
	}
	resolvedSource, sourceErr := filepath.EvalSymlinks(source)
	resolvedTarget, targetErr := filepath.EvalSymlinks(target)
	if sourceErr == nil && targetErr == nil && resolvedSource == resolvedTarget {
		return nil
	}
	content, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read updated Jabridge binary: %w", err)
	}
	return installUserFile(target, content, 0o755)
}

func installedUserExecutablePath() (string, error) {
	homeDirectory, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDirectory, ".local", "bin", "jabridge"), nil
}

func userServiceContents() ([]byte, error) {
	original := []byte("ExecStart=/usr/bin/env jabridge --daemon")
	replacement := []byte("ExecStart=%h/.local/bin/jabridge --daemon")
	service := bytes.Replace(embeddedUserService, original, replacement, 1)
	if bytes.Equal(service, embeddedUserService) {
		return nil, errors.New("bundled user service has no replaceable command")
	}
	return service, nil
}

func installUserFile(target string, content []byte, mode fs.FileMode) error {
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace non-regular path %s", target)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", target, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".jabridge-setup-")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		_ = temporary.Close()
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", target, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close %s: %w", target, err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return fmt.Errorf("install %s: %w", target, err)
	}
	cleanup = false
	return nil
}

func graphicalSession() bool {
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

func installDeviceAccess(target string) error {
	if len(embeddedUdevRule) == 0 {
		return errors.New("bundled device-access rule is empty")
	}
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace non-regular rule path %s", target)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect device-access rule: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create udev rule directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".70-jabridge.rules-")
	if err != nil {
		return fmt.Errorf("create temporary udev rule: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		_ = temporary.Close()
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o644); err != nil {
		return err
	}
	if _, err := temporary.Write(embeddedUdevRule); err != nil {
		return fmt.Errorf("write device-access rule: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync device-access rule: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close device-access rule: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return fmt.Errorf("install device-access rule: %w", err)
	}
	cleanup = false
	return nil
}

func reloadAndTriggerUdev() error {
	udevadm, err := exec.LookPath("udevadm")
	if err != nil {
		return errors.New("udevadm is not installed")
	}
	commands := [][]string{
		{"control", "--reload-rules"},
		{"trigger", "--subsystem-match=hidraw", "--action=add"},
		{"trigger", "--subsystem-match=input", "--action=add"},
		{"settle", "--timeout=5"},
	}
	for _, arguments := range commands {
		output, commandErr := exec.Command(udevadm, arguments...).CombinedOutput()
		if commandErr != nil {
			detail := strings.TrimSpace(string(output))
			if detail == "" {
				detail = commandErr.Error()
			}
			return fmt.Errorf("udevadm %s: %s", arguments[0], detail)
		}
	}
	return nil
}

func deviceAccessRuleInstalled() bool {
	for _, path := range []string{systemUdevRulePath, "/usr/lib/udev/rules.d/70-jabridge.rules"} {
		content, err := os.ReadFile(path)
		if err == nil && bytes.Equal(content, embeddedUdevRule) {
			return true
		}
	}
	return false
}

func probeJabraHidrawAccess() (found, usable bool) {
	entries, err := os.ReadDir("/sys/class/hidraw")
	if err != nil {
		return false, false
	}
	for _, entry := range entries {
		data, readErr := os.ReadFile(filepath.Join("/sys/class/hidraw", entry.Name(), "device", "uevent"))
		if readErr != nil || !jabraHIDUevent(data) {
			continue
		}
		found = true
		file, openErr := os.OpenFile(filepath.Join("/dev", entry.Name()), os.O_RDWR, 0)
		if openErr == nil {
			_ = file.Close()
			return true, true
		}
	}
	return found, false
}

func jabraHIDUevent(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		value, exists := strings.CutPrefix(strings.TrimSpace(line), "HID_ID=")
		if !exists {
			continue
		}
		parts := strings.Split(value, ":")
		return len(parts) == 3 && strings.EqualFold(strings.TrimLeft(parts[1], "0"), "b0e")
	}
	return false
}

func printSetupUsage() {
	fmt.Println(`Usage:
  jabridge setup

Sets up one-time Linux device access. Jabridge opens the normal administrator
authorization prompt, installs the app and Bash completion for the current user,
reloads device access, and enables the background service at sign-in.`)
}
