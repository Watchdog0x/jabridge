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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/buttons"
	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/daemon/pipewire"
	"github.com/Watchdog0x/jabridge/internal/headsetvolume"
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
	SoundBackend    pipewire.SoundBackend
	SoundReady      func(*pipewire.SoundController)
	ButtonsMonitor  buttons.Monitor             // explicitly enabled by the application
	MediaPlayPause  func(context.Context) error // nil uses native MPRIS
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
	cfg             Config
	listener        net.Listener
	stopPoll        context.CancelFunc
	pollDone        chan struct{}
	watchDone       chan struct{}
	acceptDone      chan struct{}
	clientsMu       sync.Mutex
	clients         map[net.Conn]struct{}
	clientWG        sync.WaitGroup
	done            chan struct{}
	api             ipc.API
	sound           *pipewire.SoundController
	soundDone       chan struct{}
	busylight       *BusylightController
	stopOnce        sync.Once
	buttons         *buttons.Controller
	buttonsDone     chan struct{}
	musicQuietUntil atomic.Int64
	connSlots       chan struct{}
	events          *ipc.EventBus
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
		cfg:        cfg,
		listener:   ln,
		stopPoll:   stopPoll,
		pollDone:   make(chan struct{}),
		watchDone:  make(chan struct{}),
		acceptDone: make(chan struct{}),
		clients:    make(map[net.Conn]struct{}),
		done:       make(chan struct{}),
		connSlots:  make(chan struct{}, cfg.MaxConnections),
		events:     ipc.NewEventBus(),
	}

	// Start PipeWire monitor for meeting detection + busylight
	d.busylight = NewBusylightController(cfg.BusylightSender)
	if cfg.ButtonsMonitor != nil {
		d.buttons = buttons.NewController(buttons.Config{Monitor: cfg.ButtonsMonitor, Publish: func(method string, data any) { d.events.Publish(method, data) }, PlayPause: cfg.MediaPlayPause, AllowMusic: func(source buttons.Source) bool {
			return time.Now().UnixNano() < d.musicQuietUntil.Load() && selectedButtonSource(api.ListDevices(), source)
		}})
		d.buttonsDone = make(chan struct{})
		go func() { defer close(d.buttonsDone); d.buttons.Run(pollContext) }()
	}
	if !cfg.DisablePipeWire {
		inCall := false
		d.sound = pipewire.NewSoundController(cfg.SoundBackend, func(state pipewire.SoundState) { d.events.Publish("sound.changed", state) }, func(snapshot *pipewire.Snapshot) {
			d.musicQuietUntil.Store(0)
			if microphoneQuiet(snapshot) {
				d.musicQuietUntil.Store(time.Now().Add(3 * time.Second).UnixNano())
			}
			state := pipewire.DetectCall(snapshot)
			if state.InCall != inCall {
				inCall = state.InCall
				d.busylight.OnCallStateChange(state)
			}
		})
		if cfg.SoundReady != nil {
			cfg.SoundReady(d.sound)
		}
		d.soundDone = make(chan struct{})
		go func() { defer close(d.soundDone); d.sound.Run(pollContext) }()
	}
	d.api = &busylightAPI{API: api, ctrl: d.busylight, sound: d.sound, buttons: d.buttons, ctx: pollContext}
	// Audio routing must be initialized before device selection follows it.
	go func() {
		defer close(d.pollDone)
		defer history.CapturePanic(history.Event{Component: "service", Action: "run"})
		pollFunc(pollContext)
	}()
	go func() { defer close(d.watchDone); d.watchState(pollContext) }()

	// Accept connections
	go func() { defer close(d.acceptDone); d.acceptLoop() }()
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

		if d.stopPoll != nil {
			d.stopPoll()
		}
		if d.acceptDone != nil {
			<-d.acceptDone // No new client goroutines may start after this point.
		}
		d.clientsMu.Lock()
		for conn := range d.clients {
			_ = conn.Close()
		}
		d.clientsMu.Unlock()
		d.clientWG.Wait()
		if d.pollDone != nil {
			<-d.pollDone
		}
		if d.watchDone != nil {
			<-d.watchDone
		}
		if d.soundDone != nil {
			<-d.soundDone
		}
		if d.buttonsDone != nil {
			<-d.buttonsDone
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
			d.clientsMu.Lock()
			if d.clients == nil {
				d.clients = make(map[net.Conn]struct{})
			}
			d.clients[conn] = struct{}{}
			d.clientsMu.Unlock()
			d.clientWG.Add(1)
			go func() {
				defer d.clientWG.Done()
				defer func() {
					d.clientsMu.Lock()
					delete(d.clients, conn)
					d.clientsMu.Unlock()
				}()
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
			d.publishDeviceState(previousDevices, currentDevices)
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
		if existed && deviceIdentityChanged(old, device) {
			history.Record(history.Event{Component: "device", Action: "detach", Phase: "observed", USBProduct: old.PID, Connection: old.Connection})
			bus.Publish("device.detached", old)
			existed = false
		}
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
	ctrl    *BusylightController
	sound   *pipewire.SoundController
	buttons *buttons.Controller
	ctx     context.Context
}

func (a *busylightAPI) GetButtons() buttons.Status {
	if a.buttons == nil {
		return buttons.Status{Mode: "off", Sources: []buttons.Source{}, Error: "Button monitor is disabled"}
	}
	return a.buttons.State()
}
func (a *busylightAPI) ConfigureButtons(mode string) (buttons.Status, error) {
	if a.buttons == nil {
		return buttons.Status{}, errors.New("button monitor is disabled")
	}
	return a.buttons.Configure(mode)
}

func selectedButtonSource(devices []ipc.DeviceInfo, source buttons.Source) bool {
	count, selected := 0, false
	for _, device := range devices {
		if device.PID == source.PID && device.Connection == "usb" {
			count++
			selected = device.Selected
		}
	}
	return source.Connection == "usb" && count == 1 && selected
}

func microphoneQuiet(snapshot *pipewire.Snapshot) bool {
	if snapshot == nil {
		return false
	}
	sources := snapshot.JabraSourceNodes()
	if len(sources) == 0 {
		for _, device := range snapshot.Devices {
			if device.Known && (device.Props.VendorID == "0x0b0e" || device.Props.VendorID == "0b0e") && audioMusicProfile(device.Profile.Name) {
				return true
			}
		}
		return false
	}
	for _, source := range sources {
		switch strings.ToLower(source.State) {
		case "idle", "suspended":
		default:
			return false
		}
	}
	return true
}

func audioMusicProfile(name string) bool {
	return name == "output:analog-stereo" || name == "output:iec958-stereo"
}

func (a *busylightAPI) ChangeSoundMode(target pipewire.SoundTarget, mode string) (pipewire.SoundState, error) {
	if a.sound == nil {
		return pipewire.SoundState{}, errors.New("PipeWire is unavailable")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.sound.ChangeMode(ctx, target, mode)
}

func (a *busylightAPI) GetSound() pipewire.SoundState {
	if a.sound != nil {
		return a.sound.State()
	}
	if sound, ok := a.API.(ipc.SoundAPI); ok {
		return sound.GetSound()
	}
	return pipewire.SoundState{Error: "PipeWire support is disabled", Nodes: []pipewire.SoundNode{}}
}
func (a *busylightAPI) ChangeSound(target pipewire.SoundTarget, action string, percent int, mode string) (pipewire.SoundNode, error) {
	if a.sound != nil {
		ctx := a.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		return a.sound.Change(ctx, target, action, percent, mode)
	}
	if sound, ok := a.API.(ipc.SoundAPI); ok {
		return sound.ChangeSound(target, action, percent, mode)
	}
	return pipewire.SoundNode{}, errors.New("PipeWire support is disabled")
}

func (a *busylightAPI) DiagnoseDevice(id uint16) ([]ipc.DiagnosticCheck, error) {
	diagnostics, ok := a.API.(ipc.DiagnosticAPI)
	if !ok {
		return nil, errors.New("device diagnostics unavailable")
	}
	return diagnostics.DiagnoseDevice(id)
}

func (a *busylightAPI) SetSettingTarget(device, key, value string, target ipc.SettingTarget, previous string) (ipc.SettingInfo, error) {
	settings, ok := a.API.(ipc.TargetedSettingsAPI)
	if !ok {
		return ipc.SettingInfo{}, fmt.Errorf("device-bound setting edits are not available")
	}
	return settings.SetSettingTarget(device, key, value, target, previous)
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

func (a *busylightAPI) SetHeadsetVolume(target ipc.SettingTarget, percent int) (headsetvolume.Value, error) {
	volume, ok := a.API.(ipc.HeadsetVolumeAPI)
	if !ok {
		return headsetvolume.Value{}, errors.New("direct headset volume is unavailable")
	}
	return volume.SetHeadsetVolume(target, percent)
}
