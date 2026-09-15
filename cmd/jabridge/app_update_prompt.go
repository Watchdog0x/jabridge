package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/Watchdog0x/jabridge/internal/buildinfo"
	"github.com/Watchdog0x/jabridge/internal/selfupdate"
	"golang.org/x/term"
)

const startupUpdateCheckTimeout = 2 * time.Second

// Run before entering raw TUI mode, starting a service or taking device locks.
// Help, machine output, background commands and redirected streams must stay
// predictable and must never wait for an update answer.
func startupAppUpdateAllowed(args []string, stdinTTY, stdoutTTY, stderrTTY bool) bool {
	if !stdinTTY || !stdoutTTY || !stderrTTY {
		return false
	}
	for _, arg := range args {
		if arg == "--help" || arg == "-h" || arg == "help" || arg == "--json" || strings.HasPrefix(arg, "--json=") {
			return false
		}
	}
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "status", "battery", "diagnose", "debug", "history", "buttons", "firmware", "fw", "settings", "model", "models", "sound", "audio", "use":
		return true
	default:
		return false
	}
}

func offerStartupAppUpdate(args []string) error {
	if !startupAppUpdateAllowed(args, term.IsTerminal(int(os.Stdin.Fd())), term.IsTerminal(int(os.Stdout.Fd())), term.IsTerminal(int(os.Stderr.Fd()))) {
		return nil
	}
	client := selfupdate.NewClient()
	var restartPath string
	confirm := func(plan selfupdate.Plan) (bool, error) {
		return confirmCLIAppUpdate(os.Stdin, os.Stderr, plan.Version)
	}
	if len(args) == 0 {
		confirm = func(plan selfupdate.Plan) (bool, error) {
			return confirmTUIAppUpdate(buildinfo.Version, plan.Version)
		}
	}
	updated, err := checkAndOfferAppUpdate(func() (selfupdate.Plan, error) {
		ctx, cancel := context.WithTimeout(context.Background(), startupUpdateCheckTimeout)
		defer cancel()
		return client.Check(ctx, buildinfo.Version, false)
	}, confirm, func(plan selfupdate.Plan) error {
		// Capture this before replacement. The updater renames the running
		// inode to a backup, so /proc/self/exe no longer names the live path.
		var err error
		restartPath, err = os.Executable()
		if err != nil {
			return fmt.Errorf("find running app: %w", err)
		}
		// The short metadata timeout does not limit the user's answer or the
		// download. The existing updater has its own download limits.
		if len(args) == 0 {
			return runTUIStartupTask("Updating Jabridge", "Downloading and verifying version "+plan.Version, func(ctx context.Context) error {
				return installAppUpdate(ctx, client, plan, io.Discard)
			})
		}
		return installAppUpdate(context.Background(), client, plan, os.Stderr)
	})
	if err != nil || !updated {
		return err
	}
	return restartUpdatedApp(restartPath, args)
}

func checkAndOfferAppUpdate(check func() (selfupdate.Plan, error), confirm func(selfupdate.Plan) (bool, error), install func(selfupdate.Plan) error) (bool, error) {
	plan, err := check()
	if err != nil || !plan.NewerThanCurrent {
		// An unavailable update server must not prevent normal device use.
		return false, nil
	}
	accepted, err := confirm(plan)
	if err != nil || !accepted {
		return false, err
	}
	if err := install(plan); err != nil {
		return false, err
	}
	return true, nil
}

func confirmCLIAppUpdate(input io.Reader, output io.Writer, version string) (bool, error) {
	if _, err := fmt.Fprintf(output, "A new Jabridge update is available: %s\n", version); err != nil {
		return false, fmt.Errorf("show update notice: %w", err)
	}
	for {
		if _, err := fmt.Fprint(output, "Update now? (yes/no) [no]: "); err != nil {
			return false, fmt.Errorf("show update prompt: %w", err)
		}
		answer, err := readAppUpdateAnswer(input)
		if errors.Is(err, io.EOF) {
			_, _ = fmt.Fprintln(output)
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("read update answer: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "n", "no":
			return false, nil
		case "y", "yes":
			return true, nil
		default:
			if _, err := fmt.Fprintln(output, "Please enter yes or no."); err != nil {
				return false, fmt.Errorf("show update prompt: %w", err)
			}
		}
	}
}

// Read only the answer line. A buffered reader could consume keys intended for
// the following TUI or command and discard them when the user chooses No.
func readAppUpdateAnswer(input io.Reader) (string, error) {
	var answer strings.Builder
	var next [1]byte
	tooLong := false
	for {
		if _, err := io.ReadFull(input, next[:]); err != nil {
			return "", err
		}
		if next[0] == '\n' {
			if tooLong {
				return "invalid", nil
			}
			return answer.String(), nil
		}
		if answer.Len() < 32 {
			answer.WriteByte(next[0])
		} else {
			tooLong = true
		}
	}
}

func restartUpdatedApp(executable string, args []string) error {
	// The new binary checks its own version, so accepting an update does not
	// reopen the old version or ask for the same update a second time.
	if err := syscall.Exec(executable, append([]string{executable}, args...), os.Environ()); err != nil {
		return fmt.Errorf("restart updated app: %w", err)
	}
	return nil
}
