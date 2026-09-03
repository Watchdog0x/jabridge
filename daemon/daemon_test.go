package daemon

import (
	"fmt"
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
func (n *nilAPI) SearchNewDevices() error                { return nil }
func (n *nilAPI) SetBTPairing(bool) error                { return nil }
func (n *nilAPI) GetAutoPairing() (bool, error)          { return false, nil }
func (n *nilAPI) SetAutoPairing(bool) error              { return nil }
func (n *nilAPI) FactoryReset() error                    { return nil }
func (n *nilAPI) SetBusylightMode(string) error          { return nil }
func (n *nilAPI) GetBusylightMode() string               { return "off" }

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
	os.WriteFile(pidFile, []byte("99999999"), 0644)
	err := checkExistingPID(pidFile)
	if err != nil {
		t.Fatalf("expected nil for stale PID, got: %v", err)
	}
}

func TestCheckExistingPID_LivePID(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "test.pid")
	// Write our own PID — should detect as running
	os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644)
	err := checkExistingPID(pidFile)
	if err == nil {
		t.Fatal("expected error for live PID, got nil")
	}
}

func TestDaemonStartStop(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		SocketPath: filepath.Join(dir, "test.sock"),
		PIDPath:    filepath.Join(dir, "test.pid"),
	}

	// Start daemon in background with a no-op poller
	pollCalled := make(chan struct{})
	errCh := make(chan error, 1)

	go func() {
		// We can't use Start() directly because it blocks on signals.
		// Instead, test the components individually.

		// Write PID
		os.WriteFile(cfg.PIDPath, []byte(strconv.Itoa(os.Getpid())), 0644)

		// Create socket
		ln, err := net.Listen("unix", cfg.SocketPath)
		if err != nil {
			errCh <- err
			return
		}

		d := &Daemon{
			cfg:      cfg,
			listener: ln,
			stopPoll: make(chan struct{}),
			done:     make(chan struct{}),
			api:      &nilAPI{},
		}

		go func() {
			<-d.stopPoll
			close(pollCalled)
		}()

		go d.acceptLoop()

		// Let it run briefly
		time.Sleep(100 * time.Millisecond)

		// Test connection — send a JSON-RPC request and read response
		conn, err := net.Dial("unix", cfg.SocketPath)
		if err != nil {
			errCh <- err
			d.Stop()
			return
		}
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"version"}` + "\n"))
		buf := make([]byte, 256)
		n, _ := conn.Read(buf)
		conn.Close()

		if n == 0 {
			errCh <- fmt.Errorf("empty response from daemon")
			d.Stop()
			return
		}

		// Stop
		d.Stop()

		// Verify cleanup
		if _, err := os.Stat(cfg.SocketPath); !os.IsNotExist(err) {
			errCh <- err
			return
		}
		if _, err := os.Stat(cfg.PIDPath); !os.IsNotExist(err) {
			errCh <- err
			return
		}

		errCh <- nil
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}

	select {
	case <-pollCalled:
		// poll stop was signaled
	case <-time.After(time.Second):
		t.Fatal("poll stop not signaled")
	}
}

func TestConnectionResponse(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg:      Config{SocketPath: sockPath},
		listener: ln,
		stopPoll: make(chan struct{}),
		done:     make(chan struct{}),
		api:      &nilAPI{},
	}
	go d.acceptLoop()
	defer d.listener.Close()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Send a JSON-RPC request
	conn.SetWriteDeadline(time.Now().Add(time.Second))
	conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"version"}` + "\n"))

	buf := make([]byte, 512)
	conn.SetReadDeadline(time.Now().Add(time.Second))
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
