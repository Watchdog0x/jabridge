package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

// nilAPI is a minimal API implementation for lifecycle tests.
type nilAPI struct{}

func (n *nilAPI) ListDevices() []ipc.DeviceInfo          { return nil }
func (n *nilAPI) GetBattery() (*ipc.BatteryInfo, error)  { return nil, nil }
func (n *nilAPI) GetFirmware() string                    { return "" }
func (n *nilAPI) GetFeatures() ipc.FeatureInfo           { return ipc.FeatureInfo{} }
func (n *nilAPI) GetPairingList() []ipc.PairedDeviceInfo { return nil }
func (n *nilAPI) GetSearchList() []ipc.PairedDeviceInfo  { return nil }
func (n *nilAPI) SearchNewDevices() error                { return nil }
func (n *nilAPI) ConnectSearchDevice(int) error          { return nil }
func (n *nilAPI) ConnectRememberedDevice(int) error      { return nil }
func (n *nilAPI) DisconnectRememberedDevice(int) error   { return nil }
func (n *nilAPI) ForgetRememberedDevice(int) error       { return nil }
func (n *nilAPI) SetBTPairing(bool) error                { return nil }
func (n *nilAPI) GetAutoPairing() (bool, error)          { return false, nil }
func (n *nilAPI) SetAutoPairing(bool) error              { return nil }
func (n *nilAPI) FactoryReset() error                    { return nil }
func (n *nilAPI) SetBusylightMode(string) error          { return nil }
func (n *nilAPI) GetBusylightMode() string               { return "off" }
func (n *nilAPI) ListSettings(string) ([]ipc.SettingInfo, error) {
	return nil, nil
}
func (n *nilAPI) SetSetting(device, key, value string) (ipc.SettingInfo, error) {
	return ipc.SettingInfo{Device: device, Key: key, Value: value}, nil
}
func (n *nilAPI) SelectDevice(uint16) error { return nil }
func (n *nilAPI) Shutdown() error           { return nil }

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.SocketPath == "" {
		t.Fatal("SocketPath is empty")
	}
	if cfg.PIDPath == "" {
		t.Fatal("PIDPath is empty")
	}
	if !filepath.IsAbs(cfg.SocketPath) {
		t.Errorf("SocketPath not absolute: %s", cfg.SocketPath)
	}
}

func TestCheckExistingPID_NoPIDFile(t *testing.T) {
	err := checkExistingPID("/tmp/jabridge-test-nonexistent.pid")
	if err != nil {
		t.Fatalf("expected nil for missing PID file, got: %v", err)
	}
}

func TestCheckExistingPID_StalePID(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "test.pid")
	// Write a PID that doesn't exist (99999999)
	if err := os.WriteFile(pidFile, []byte("99999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := checkExistingPID(pidFile)
	if err != nil {
		t.Fatalf("expected nil for stale PID, got: %v", err)
	}
}

func TestCheckExistingPID_LivePID(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "test.pid")
	// Write our own PID — should detect as running
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	err := checkExistingPID(pidFile)
	if err == nil {
		t.Fatal("expected error for live PID, got nil")
	}
}

func TestConnectionResponse(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	_, stopPoll := context.WithCancel(context.Background())
	d := &Daemon{
		cfg:       Config{SocketPath: sockPath},
		listener:  ln,
		stopPoll:  stopPoll,
		done:      make(chan struct{}),
		api:       &nilAPI{},
		connSlots: make(chan struct{}, 4),
	}
	go d.acceptLoop()
	defer func() { _ = d.listener.Close() }()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	// Send a JSON-RPC request
	if err := conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"version"}` + "\n")); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 512)
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	resp := string(buf[:n])
	if len(resp) == 0 {
		t.Fatal("empty response")
	}
	if resp[0] != '{' {
		t.Errorf("response not JSON: %q", resp)
	}
}

func TestCreatePIDFileIsPrivateAndExclusive(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "jabridge.pid")
	if err := createPIDFile(pidPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(pidPath) })
	info, err := os.Stat(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("PID mode = %o, want 600", got)
	}
	if err := createPIDFile(pidPath); err == nil {
		t.Fatal("second daemon acquired an active PID file")
	}
}

func TestCreatePIDFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	pidPath := filepath.Join(dir, "jabridge.pid")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, pidPath); err != nil {
		t.Fatal(err)
	}
	if err := createPIDFile(pidPath); err == nil {
		t.Fatal("PID symlink was accepted")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("symlink target changed to %q", data)
	}
}

func TestRunCreatesPrivateSocketAndStopsWithContext(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		SocketPath:      filepath.Join(dir, "jabridge.sock"),
		PIDPath:         filepath.Join(dir, "jabridge.pid"),
		MaxConnections:  2,
		IdleTimeout:     time.Second,
		DisablePipeWire: true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cfg, func(pollContext context.Context) {
			<-pollContext.Done()
		}, &nilAPI{})
	}()

	deadline := time.Now().Add(time.Second)
	for {
		info, err := os.Stat(cfg.SocketPath)
		if err == nil {
			if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("socket mode = %o, want 600", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket was not created: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon did not stop after context cancellation")
	}
	if _, err := os.Stat(cfg.SocketPath); !os.IsNotExist(err) {
		t.Fatalf("socket remains after shutdown: %v", err)
	}
}

func TestConnectionLimitRejectsExcessClient(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "limited.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	_, stopPoll := context.WithCancel(context.Background())
	daemon := &Daemon{
		cfg:       Config{SocketPath: socketPath, IdleTimeout: time.Second},
		listener:  listener,
		stopPoll:  stopPoll,
		done:      make(chan struct{}),
		api:       &nilAPI{},
		connSlots: make(chan struct{}, 1),
	}
	go daemon.acceptLoop()
	first, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	deadline := time.Now().Add(time.Second)
	for len(daemon.connSlots) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("first client did not occupy a connection slot")
		}
		time.Sleep(time.Millisecond)
	}
	second, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	if err := second.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	if _, err := second.Read(buffer); err == nil {
		t.Fatal("excess client was not closed")
	}
	_ = first.Close()
	daemon.Stop()
}

func TestPublishDeviceChangesEmitsAttachDetachAndBattery(t *testing.T) {
	bus := ipc.NewEventBus()
	events, cancel := bus.Subscribe()
	defer cancel()
	previous := map[uint16]ipc.DeviceInfo{
		1: {ID: 1, Name: "Old", PID: 1},
		2: {ID: 2, Name: "Battery", PID: 2, Battery: &ipc.BatteryInfo{Level: 40}},
	}
	current := map[uint16]ipc.DeviceInfo{
		2: {ID: 2, Name: "Battery", PID: 2, Battery: &ipc.BatteryInfo{Level: 41}},
		3: {ID: 3, Name: "New", PID: 3},
	}
	publishDeviceChanges(bus, previous, current)
	want := map[string]bool{
		"device.attached":       true,
		"device.detached":       true,
		"device.battery.update": true,
	}
	for range 3 {
		select {
		case event := <-events:
			delete(want, event.Method)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for state notification")
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing notifications: %v", want)
	}
}
