package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

func runService(args []string) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		printServiceUsage()
		return nil
	}
	if len(args) != 1 {
		return errors.New("usage: jabridge service start|status|stop|restart")
	}

	var installedExecutable string
	if args[0] == "start" || args[0] == "restart" {
		var err error
		installedExecutable, err = installUserFiles()
		if err != nil {
			return err
		}
	}

	if systemdUserAvailable() {
		return runSystemdServiceCommand(args[0])
	}
	return runPortableServiceCommand(args[0], installedExecutable)
}

func runSystemdServiceCommand(action string) error {
	if action == "start" || action == "restart" {
		if err := systemctlUser("daemon-reload"); err != nil {
			return err
		}
	}
	switch action {
	case "start":
		if err := systemctlUser("start", "jabridge.service"); err != nil {
			return err
		}
		if err := waitForService(5 * time.Second); err != nil {
			return err
		}
		fmt.Println("Jabridge service started.")
	case "status":
		active, err := userServiceActive()
		if err != nil {
			return err
		}
		printServiceStatus(active)
	case "stop":
		if err := systemctlUser("stop", "jabridge.service"); err != nil {
			return err
		}
		fmt.Println("Jabridge service stopped.")
	case "restart":
		if err := systemctlUser("restart", "jabridge.service"); err != nil {
			return err
		}
		if err := waitForService(5 * time.Second); err != nil {
			return err
		}
		fmt.Println("Jabridge service restarted.")
	default:
		return fmt.Errorf("unknown service command %q; use start, status, stop, or restart", action)
	}
	return nil
}

func runPortableServiceCommand(action, installedExecutable string) error {
	switch action {
	case "start":
		if err := startPortableService(installedExecutable); err != nil {
			return err
		}
		fmt.Println("Jabridge service started.")
	case "status":
		active, err := serviceReachable(500 * time.Millisecond)
		if err != nil {
			return err
		}
		printServiceStatus(active)
	case "stop":
		if err := stopPortableService(); err != nil {
			return err
		}
		fmt.Println("Jabridge service stopped.")
	case "restart":
		active, _ := serviceReachable(300 * time.Millisecond)
		if active {
			if err := stopPortableService(); err != nil {
				return err
			}
		}
		if err := startPortableService(installedExecutable); err != nil {
			return err
		}
		fmt.Println("Jabridge service restarted.")
	default:
		return fmt.Errorf("unknown service command %q; use start, status, stop, or restart", action)
	}
	return nil
}

func printServiceStatus(active bool) {
	if active {
		fmt.Println("Jabridge service is running.")
	} else {
		fmt.Println("Jabridge service is stopped.")
	}
}

func enableUserService(installedExecutable string) error {
	if systemdUserAvailable() {
		if err := systemctlUser("daemon-reload"); err != nil {
			return err
		}
		if err := systemctlUser("enable", "jabridge.service"); err != nil {
			return err
		}
		if err := systemctlUser("restart", "jabridge.service"); err != nil {
			return err
		}
		return waitForService(5 * time.Second)
	}
	if err := installDesktopAutostart(installedExecutable); err != nil {
		return err
	}
	return startPortableService(installedExecutable)
}

func systemdUserAvailable() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", "--user", "show-environment").Run() == nil
}

func systemctlUser(arguments ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, arguments...)...)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("system service command timed out")
	}
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("systemctl --user %s: %s", strings.Join(arguments, " "), detail)
	}
	return nil
}

func userServiceActive() (bool, error) {
	if active, _ := serviceReachable(300 * time.Millisecond); active {
		return true, nil
	}
	if !systemdUserAvailable() {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "systemctl", "--user", "is-active", "--quiet", "jabridge.service")
	err := command.Run()
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if exitError, ok := err.(*exec.ExitError); ok && (exitError.ExitCode() == 3 || exitError.ExitCode() == 4) {
		return false, nil
	}
	return false, fmt.Errorf("check Jabridge service: %w", err)
}

func pauseUserServiceForDirectCommand() (func() error, error) {
	active, err := userServiceActive()
	if err != nil || !active {
		return func() error { return nil }, err
	}
	if systemdUserAvailable() {
		if err := systemctlUser("stop", "jabridge.service"); err != nil {
			return nil, err
		}
		return func() error {
			if err := systemctlUser("start", "jabridge.service"); err != nil {
				return err
			}
			return waitForService(5 * time.Second)
		}, nil
	}
	if err := stopPortableService(); err != nil {
		return nil, err
	}
	executable, err := installedUserExecutable()
	if err != nil {
		return nil, err
	}
	return func() error { return startPortableService(executable) }, nil
}

func restartUserServiceAfterUpdate() error {
	if systemdUserAvailable() {
		if err := systemctlUser("restart", "jabridge.service"); err != nil {
			return err
		}
		return waitForService(5 * time.Second)
	}
	if err := stopPortableService(); err != nil {
		return err
	}
	executable, err := installedUserExecutable()
	if err != nil {
		return err
	}
	return startPortableService(executable)
}

func commandNeedsDirectHardware(command string) bool {
	switch command {
	case "status", "battery", "diagnose", "settings", "model", "firmware", "fw":
		return true
	default:
		return false
	}
}

func waitForService(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client, err := ipc.DialWithRetry(ctx, ipcSocketPath())
	if err != nil {
		return fmt.Errorf("service did not become ready: %w", err)
	}
	defer func() { _ = client.Close() }()
	pingContext, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	return client.Ping(pingContext)
}

func serviceReachable(timeout time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client, err := ipc.Dial(ctx, ipcSocketPath())
	if err != nil {
		return false, nil
	}
	defer func() { _ = client.Close() }()
	if err := client.Ping(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func startPortableService(executable string) error {
	if executable == "" {
		var err error
		executable, err = installedUserExecutable()
		if err != nil {
			return err
		}
	}
	if active, _ := serviceReachable(300 * time.Millisecond); active {
		return nil
	}
	homeDirectory, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	stateDirectory := filepath.Join(homeDirectory, ".local", "state", "jabridge")
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		return fmt.Errorf("create service log directory: %w", err)
	}
	logFile, err := os.OpenFile(filepath.Join(stateDirectory, "service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open service log: %w", err)
	}
	command := exec.Command(executable, "--daemon")
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start Jabridge service: %w", err)
	}
	_ = command.Process.Release()
	_ = logFile.Close()
	return waitForService(5 * time.Second)
}

func stopPortableService() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := ipc.Dial(ctx, ipcSocketPath())
	if err != nil {
		return nil
	}
	var result map[string]bool
	callErr := client.Call(ctx, "service.shutdown", nil, &result)
	_ = client.Close()
	if callErr != nil {
		return callErr
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if active, _ := serviceReachable(100 * time.Millisecond); !active {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("service did not stop")
}

func installDesktopAutostart(executable string) error {
	homeDirectory, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	escapedExecutable := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", "\\$").Replace(executable)
	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Jabridge service
Comment=Jabra headset background service
Exec="%s" --daemon
Terminal=false
NoDisplay=true
X-GNOME-Autostart-enabled=true
`, escapedExecutable)
	path := filepath.Join(homeDirectory, ".config", "autostart", "jabridge.desktop")
	return installUserFile(path, []byte(content), 0o644)
}

func installedUserExecutable() (string, error) {
	path, err := installedUserExecutablePath()
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("installed Jabridge binary was not found; run jabridge setup")
	}
	return path, nil
}

func printServiceUsage() {
	fmt.Println(`Usage:
  jabridge service start
  jabridge service status
  jabridge service stop
  jabridge service restart

Run ` + "`jabridge setup`" + ` once to install and enable the service for future
sign-ins.`)
}
