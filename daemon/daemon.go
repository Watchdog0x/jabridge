// daemon — Jabridge service lifecycle manager.
//
// Manages PID file, Unix socket, device polling, and graceful shutdown.
// Runs as a regular user service (no root required).

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/daemon/pipewire"
	"github.com/Watchdog0x/jabridge/internal/history"
)

// Config holds daemon startup parameters.
type Config struct {
	SocketPath      string          // default: $XDG_RUNTIME_DIR/jabridge.sock
	PIDPath         string          // default: $XDG_RUNTIME_DIR/jabridge.pid
	BusylightSender BusylightSender // nil if device has no busylight
	MaxConnections  int
	IdleTimeout     time.Duration
	DisablePipeWire bool // tests and hosts without PipeWire
}

// DefaultConfig returns paths under $XDG_RUNTIME_DIR.
func DefaultConfig() Config {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	return Config{
		SocketPath:     filepath.Join(dir, "jabridge.sock"),
		PIDPath:        filepath.Join(dir, "jabridge.pid"),
		MaxConnections: 32,
		IdleTimeout:    30 * time.Second,
	}
}

// Daemon is the long-running service.
type Daemon struct {
	cfg       Config
	listener  net.Listener
	stopPoll  context.CancelFunc
	done      chan struct{}
	api       ipc.API
	pwMon     *pipewire.Monitor
	busylight *BusylightController
	stopOnce  sync.Once
	connSlots chan struct{}
	events    *ipc.EventBus
}

// Start runs the service until SIGTERM or SIGINT.
func Start(cfg Config, pollFunc func(context.Context), api ipc.API) error {
	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stopSignals()
	return Run(ctx, cfg, pollFunc, api)
}

// Run creates the PID file and Unix socket, then serves until ctx is canceled.
// It is the programmatic entry point used by tests and service managers.
func Run(ctx context.Context, cfg Config, pollFunc func(context.Context), api ipc.API) (runErr error) {
	entry := history.Event{Component: "service", Action: "run"}
	defer history.CapturePanic(entry)
	finish := history.Begin(entry)
	defer history.EndDeferred(finish, &runErr)
	if cfg.MaxConnections <= 0 {
		cfg.MaxConnections = 32
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 30 * time.Second
	}
	// Single-instance check
	if err := createPIDFile(cfg.PIDPath); err != nil {
		return err
	}
	cleanupPID := true
	defer func() {
		if cleanupPID {
			_ = os.Remove(cfg.PIDPath)
		}
	}()

	if err := removeStaleSocket(cfg.SocketPath); err != nil {
		return err
	}

	// Create Unix socket
	ln, err := net.Listen("unix", cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.SocketPath, err)
	}
	if err := os.Chmod(cfg.SocketPath, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(cfg.SocketPath)
		return fmt.Errorf("secure socket %s: %w", cfg.SocketPath, err)
	}
	pollContext, stopPoll := context.WithCancel(context.Background())

	d := &Daemon{
		cfg:       cfg,
		listener:  ln,
		stopPoll:  stopPoll,
		done:      make(chan struct{}),
		connSlots: make(chan struct{}, cfg.MaxConnections),
		events:    ipc.NewEventBus(),
	}

	// Start device polling
	go func() {
		defer history.CapturePanic(history.Event{Component: "service", Action: "run"})
		pollFunc(pollContext)
	}()

	// Start PipeWire monitor for meeting detection + busylight
	d.busylight = NewBusylightController(cfg.BusylightSender)
	d.api = &busylightAPI{API: api, ctrl: d.busylight}
	go d.watchState(ctx)
	if !cfg.DisablePipeWire {
		d.pwMon = pipewire.NewMonitor(2*time.Second, func(state pipewire.CallState) {
			// Forward to busylight controller (handles feature check internally)
			d.busylight.OnCallStateChange(state)
			if state.InCall {
				fmt.Fprintf(os.Stderr, "[jabridge] call started: %s\n", state.AppName)
			} else {
				fmt.Fprintln(os.Stderr, "[jabridge] call ended")
			}
		})
		go d.pwMon.Start()
	}

	// Accept connections
	go d.acceptLoop()
	history.Record(history.Event{Component: "service", Action: "start", Phase: "ok"})

	fmt.Fprintf(os.Stderr, "[jabridge] daemon started (pid=%d socket=%s)\n", os.Getpid(), cfg.SocketPath)

	<-ctx.Done()
	fmt.Fprintln(os.Stderr, "[jabridge] shutting down...")
	d.Stop()
	cleanupPID = false
	return nil
}

// Stop performs graceful shutdown: close listener, stop polling, clean up files.
func (d *Daemon) Stop() {
	d.stopOnce.Do(func() {
		history.Record(history.Event{Component: "service", Action: "stop", Phase: "observed"})
		if d.listener != nil {
			_ = d.listener.Close()
		}

		if d.pwMon != nil {
			d.pwMon.Stop()
		}

		if d.stopPoll != nil {
			d.stopPoll()
		}

		_ = os.Remove(d.cfg.SocketPath)
		_ = os.Remove(d.cfg.PIDPath)

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
		select {
		case d.connSlots <- struct{}{}:
			go func() {
				defer func() { <-d.connSlots }()
				d.handleConnection(conn)
			}()
		default:
			_ = conn.Close()
		}
	}
}

func (d *Daemon) handleConnection(conn net.Conn) {
	ipc.HandleConnectionWithBus(conn, d.api, d.events, d.cfg.IdleTimeout)
}

func (d *Daemon) watchState(ctx context.Context) {
	defer history.CapturePanic(history.Event{Component: "service", Action: "run"})
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	previousDevices := indexDevices(d.api.ListDevices())
	previousPairing := d.api.GetPairingList()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			currentDevices := indexDevices(d.api.ListDevices())
			publishDeviceChanges(d.events, previousDevices, currentDevices)
			previousDevices = currentDevices
			currentPairing := d.api.GetPairingList()
			if !reflect.DeepEqual(previousPairing, currentPairing) {
				history.Record(history.Event{Component: "device", Action: "pairing", Phase: "observed"})
				d.events.Publish("device.pairing.update", currentPairing)
				previousPairing = append([]ipc.PairedDeviceInfo(nil), currentPairing...)
			}
		}
	}
}

func indexDevices(devices []ipc.DeviceInfo) map[uint16]ipc.DeviceInfo {
	result := make(map[uint16]ipc.DeviceInfo, len(devices))
	for _, device := range devices {
		result[device.ID] = device
	}
	return result
}

func publishDeviceChanges(bus *ipc.EventBus, previous, current map[uint16]ipc.DeviceInfo) {
	for id, device := range current {
		old, existed := previous[id]
		if !existed {
			history.Record(history.Event{Component: "device", Action: "attach", Phase: "observed", USBProduct: device.PID, Connection: device.Connection})
			bus.Publish("device.attached", device)
			continue
		}
		if !reflect.DeepEqual(old.Battery, device.Battery) {
			history.Record(history.Event{Component: "device", Action: "battery", Phase: "observed", USBProduct: device.PID, Connection: device.Connection})
			bus.Publish("device.battery.update", device)
		}
	}
	for id, device := range previous {
		if _, exists := current[id]; !exists {
			history.Record(history.Event{Component: "device", Action: "detach", Phase: "observed", USBProduct: device.PID, Connection: device.Connection})
			bus.Publish("device.detached", device)
		}
	}
}

type busylightAPI struct {
	ipc.API
	ctrl *BusylightController
}

func (a *busylightAPI) DiagnoseDevice(id uint16) ([]ipc.DiagnosticCheck, error) {
	diagnostics, ok := a.API.(ipc.DiagnosticAPI)
	if !ok {
		return nil, errors.New("device diagnostics unavailable")
	}
	return diagnostics.DiagnoseDevice(id)
}

func (a *busylightAPI) SetBusylightMode(mode string) error {
	parsed, err := ParseBusylightMode(mode)
	if err != nil {
		return err
	}
	return a.ctrl.SetMode(parsed)
}

func (a *busylightAPI) GetBusylightMode() string {
	return a.ctrl.Mode().String()
}

// checkExistingPID checks if another daemon instance is already running.
func checkExistingPID(pidPath string) error {
	info, err := os.Lstat(pidPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect PID file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("refusing unsafe PID path %s", pidPath)
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return fmt.Errorf("read PID file: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
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

func createPIDFile(pidPath string) error {
	if err := checkExistingPID(pidPath); err != nil {
		return err
	}
	if err := os.Remove(pidPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale PID file: %w", err)
	}
	file, err := os.OpenFile(pidPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create PID file: %w", err)
	}
	created := true
	defer func() {
		_ = file.Close()
		if created {
			_ = os.Remove(pidPath)
		}
	}()
	if _, err := fmt.Fprintf(file, "%d", os.Getpid()); err != nil {
		return fmt.Errorf("write PID file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync PID file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close PID file: %w", err)
	}
	created = false
	return nil
}

func removeStaleSocket(socketPath string) error {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect socket path: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to remove non-socket path %s", socketPath)
	}
	if err := os.Remove(socketPath); err != nil {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	return nil
}
