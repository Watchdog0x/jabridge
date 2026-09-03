// daemon — Jabridge service lifecycle manager.
//
// Manages PID file, Unix socket, device polling, and graceful shutdown.
// Runs as a regular user service (no root required).

package daemon

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

// Config holds daemon startup parameters.
type Config struct {
	SocketPath      string          // default: $XDG_RUNTIME_DIR/jabridge.sock
	PIDPath         string          // default: $XDG_RUNTIME_DIR/jabridge.pid
	BusylightSender BusylightSender // nil if device has no busylight
}

// DefaultConfig returns paths under $XDG_RUNTIME_DIR.
func DefaultConfig() Config {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	return Config{
		SocketPath: filepath.Join(dir, "jabridge.sock"),
		PIDPath:    filepath.Join(dir, "jabridge.pid"),
	}
}

// Daemon is the long-running service.
type Daemon struct {
	cfg       Config
	listener  net.Listener
	stopPoll  chan struct{}
	done      chan struct{}
	api       ipc.API
	pwMon     *pipewire.Monitor
	busylight *BusylightController
	stopOnce  sync.Once
}

// Start creates the PID file, opens the Unix socket, and begins
// accepting connections. Blocks until Stop() is called or a signal
// (SIGTERM/SIGINT) is received. The api parameter provides the
// device management functions for the IPC handler.
func Start(cfg Config, pollFunc func(stop <-chan struct{}), api ipc.API) error {
	// Single-instance check
	if err := checkExistingPID(cfg.PIDPath); err != nil {
		return err
	}

	// Write PID file
	if err := os.WriteFile(cfg.PIDPath, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		return fmt.Errorf("write PID file: %w", err)
	}

	// Remove stale socket
	os.Remove(cfg.SocketPath)

	// Create Unix socket
	ln, err := net.Listen("unix", cfg.SocketPath)
	if err != nil {
		os.Remove(cfg.PIDPath)
		return fmt.Errorf("listen %s: %w", cfg.SocketPath, err)
	}

	d := &Daemon{
		cfg:      cfg,
		listener: ln,
		stopPoll: make(chan struct{}),
		done:     make(chan struct{}),
	}

	// Start device polling
	go pollFunc(d.stopPoll)

	// Start PipeWire monitor for meeting detection + busylight
	d.busylight = NewBusylightController(cfg.BusylightSender)
	d.api = &busylightAPI{API: api, ctrl: d.busylight}
	d.pwMon = pipewire.NewMonitor(500*time.Millisecond, func(state pipewire.CallState) {
		// Forward to busylight controller (handles feature check internally)
		d.busylight.OnCallStateChange(state)
		if state.InCall {
			fmt.Fprintf(os.Stderr, "[jabridge] call started: %s\n", state.AppName)
		} else {
			fmt.Fprintln(os.Stderr, "[jabridge] call ended")
		}
	})
	go d.pwMon.Start()

	// Accept connections
	go d.acceptLoop()

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	fmt.Fprintf(os.Stderr, "[jabridge] daemon started (pid=%d socket=%s)\n", os.Getpid(), cfg.SocketPath)

	// Block until signal
	<-sigCh
	fmt.Fprintln(os.Stderr, "[jabridge] shutting down...")
	d.Stop()
	return nil
}

// Stop performs graceful shutdown: close listener, stop polling, clean up files.
func (d *Daemon) Stop() {
	d.stopOnce.Do(func() {
		if d.listener != nil {
			d.listener.Close()
		}

		if d.pwMon != nil {
			d.pwMon.Stop()
		}

		close(d.stopPoll)

		os.Remove(d.cfg.SocketPath)
		os.Remove(d.cfg.PIDPath)

		close(d.done)
		fmt.Fprintln(os.Stderr, "[jabridge] shutdown complete")
	})
}

func (d *Daemon) acceptLoop() {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			// Listener closed — normal during shutdown
			return
		}
		go d.handleConnection(conn)
	}
}

func (d *Daemon) handleConnection(conn net.Conn) {
	ipc.HandleConnection(conn, d.api)
}

type busylightAPI struct {
	ipc.API
	ctrl *BusylightController
}

func (a *busylightAPI) SetBusylightMode(mode string) error {
	parsed, err := ParseBusylightMode(mode)
	if err != nil {
		return err
	}
	a.ctrl.SetMode(parsed)
	return nil
}

func (a *busylightAPI) GetBusylightMode() string {
	return a.ctrl.Mode().String()
}

// checkExistingPID checks if another daemon instance is already running.
func checkExistingPID(pidPath string) error {
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return nil // no PID file — OK
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return nil // corrupt PID file — OK to overwrite
	}
	// Check if process is alive
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	// Signal 0 checks existence without actually signaling
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return nil // process is dead — stale PID file
	}
	return fmt.Errorf("another jabridge daemon is running (pid=%d)", pid)
}
