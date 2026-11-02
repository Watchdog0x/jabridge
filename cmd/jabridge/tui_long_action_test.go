package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

type longTUIOperationAPI struct {
	jabraAPIBridge
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	polls       atomic.Int32
	features    atomic.Int32
	commits     atomic.Int32
	mu          sync.Mutex
	pollTimes   []time.Time
}

func (a *longTUIOperationAPI) SetSettingTarget(device, key, value string, target ipc.SettingTarget, previous string) (ipc.SettingInfo, error) {
	if device != "headset" || target.ID != 7 || target.Instance != strings.Repeat("a", 32) || previous != "old" {
		return ipc.SettingInfo{}, fmt.Errorf("bound request changed")
	}
	a.commits.Add(1)
	a.enteredOnce.Do(func() { close(a.entered) })
	<-a.release
	return ipc.SettingInfo{Key: key, Value: value, Target: &target}, nil
}
func (a *longTUIOperationAPI) ListDevices() []ipc.DeviceInfo {
	a.mu.Lock()
	a.pollTimes = append(a.pollTimes, time.Now())
	a.mu.Unlock()
	a.polls.Add(1)
	return []ipc.DeviceInfo{{ID: 7, Instance: strings.Repeat("a", 32), PID: 0x4052, Connection: "usb", Selected: true}}
}
func (*longTUIOperationAPI) GetPairingList() []ipc.PairedDeviceInfo { return nil }
func (a *longTUIOperationAPI) GetFeatures() ipc.FeatureInfo {
	a.features.Add(1)
	return ipc.FeatureInfo{}
}
func (*longTUIOperationAPI) GetSound() pipewire.SoundState { return pipewire.SoundState{} }
func (*longTUIOperationAPI) ChangeSound(pipewire.SoundTarget, string, int, string) (pipewire.SoundNode, error) {
	return pipewire.SoundNode{}, nil
}

var _ ipc.SoundAPI = (*longTUIOperationAPI)(nil)

type countedTUIConnection struct {
	net.Conn
	once            sync.Once
	active, closed  *atomic.Int32
	timingMu        sync.Mutex
	incoming        []byte
	pendingDevices  map[string]time.Time
	deviceLatencies []time.Duration
}

func (c *countedTUIConnection) Read(data []byte) (int, error) {
	n, err := c.Conn.Read(data)
	c.timingMu.Lock()
	c.incoming = append(c.incoming, data[:n]...)
	for {
		end := bytes.IndexByte(c.incoming, '\n')
		if end < 0 {
			break
		}
		var request ipc.Request
		if json.Unmarshal(c.incoming[:end], &request) == nil && request.Method == "devices.list" {
			if c.pendingDevices == nil {
				c.pendingDevices = make(map[string]time.Time)
			}
			c.pendingDevices[string(request.ID)] = time.Now()
		}
		c.incoming = c.incoming[end+1:]
	}
	c.timingMu.Unlock()
	return n, err
}
func (c *countedTUIConnection) Write(data []byte) (int, error) {
	var response struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(bytes.TrimSpace(data), &response) == nil && len(response.ID) > 0 {
		c.timingMu.Lock()
		if started, exists := c.pendingDevices[string(response.ID)]; exists {
			c.deviceLatencies = append(c.deviceLatencies, time.Since(started))
			delete(c.pendingDevices, string(response.ID))
		}
		c.timingMu.Unlock()
	}
	return c.Conn.Write(data)
}

func (c *countedTUIConnection) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.active.Add(-1); c.closed.Add(1) })
	return err
}

// Exercise the real refresh/reconnect loop, with a fake device request held
// after one commit. No USB, PipeWire, service manager or installed app is used.
func TestTUILongActionSurvivesSync(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, devices{}, -1, -1)
	soundViewMu.RLock()
	oldSound := soundView
	soundViewMu.RUnlock()
	statusMu.RLock()
	oldMessage, oldError, oldUntil, oldKind := statusMessage, statusIsError, statusUntil, statusKind
	statusMu.RUnlock()
	t.Cleanup(func() {
		updateSoundView(oldSound)
		statusMu.Lock()
		statusMessage, statusIsError, statusUntil, statusKind = oldMessage, oldError, oldUntil, oldKind
		statusMu.Unlock()
	})
	setStatus("", false)
	oldBackend := currentTUIBackend()
	t.Cleanup(func() { setTUIBackend(oldBackend) })
	api := &longTUIOperationAPI{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(api.release) }) }
	defer release()
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "service.sock"))
	if err != nil {
		t.Fatal(err)
	}
	bus := ipc.NewEventBus()
	var acceptedCount, activeCount, closedCount atomic.Int32
	var connectionsMu sync.Mutex
	var connections []*countedTUIConnection
	var handlers sync.WaitGroup
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			tracked := &countedTUIConnection{Conn: connection, active: &activeCount, closed: &closedCount}
			acceptedCount.Add(1)
			activeCount.Add(1)
			connectionsMu.Lock()
			connections = append(connections, tracked)
			connectionsMu.Unlock()
			handlers.Add(1)
			go func() { defer handlers.Done(); ipc.HandleConnectionWithBus(tracked, api, bus, 30*time.Second) }()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	var backend *tuiIPCBackend
	syncDone := make(chan struct{})
	syncStarted := false
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			release()
			if backend != nil {
				backend.close()
			}
			_ = listener.Close()
			<-accepted
			connectionsMu.Lock()
			for _, connection := range connections {
				_ = connection.Close()
			}
			connectionsMu.Unlock()
			if syncStarted {
				select {
				case <-syncDone:
				case <-time.After(2 * time.Second):
					t.Error("refresh loop did not stop")
				}
			}
			handlersDone := make(chan struct{})
			go func() { handlers.Wait(); close(handlersDone) }()
			select {
			case <-handlersDone:
			case <-time.After(2 * time.Second):
				t.Error("IPC handlers did not stop")
			}
		})
	}
	t.Cleanup(stop)
	dialCtx, stopDial := context.WithTimeout(ctx, time.Second)
	client, err := ipc.Dial(dialCtx, listener.Addr().String())
	stopDial()
	if err != nil {
		t.Fatal(err)
	}
	backend = &tuiIPCBackend{client: client, socket: listener.Addr().String()}
	setTUIBackend(backend)
	syncStarted = true
	go func() { defer close(syncDone); runTUIServiceSync(ctx, backend) }()
	waitFeatures := func(want int32, budget time.Duration) {
		deadline := time.Now().Add(budget)
		for api.features.Load() < want && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if api.features.Load() < want {
			t.Fatalf("refresh did not finish: features=%d want=%d", api.features.Load(), want)
		}
	}
	waitFeatures(1, 4*time.Second)
	beforeProbe := api.features.Load()
	bus.Publish("device.attached", map[string]int{"id": 7})
	waitFeatures(beforeProbe+1, time.Second)
	before := api.polls.Load()
	actionDone := make(chan error, 1)
	var result ipc.SettingInfo
	go func() {
		actionDone <- tuiIPCCall("settings.set", map[string]any{"device": "headset", "key": "name", "value": "new", "target": ipc.SettingTarget{ID: 7, Instance: strings.Repeat("a", 32)}, "previous": "old"}, &result)
	}()
	select {
	case <-api.entered:
	case <-time.After(time.Second):
		t.Fatal("setting request did not reach server")
	}
	holdStarted := time.Now()
	holdTimer := time.NewTimer(8 * time.Second)
	defer holdTimer.Stop()
	// Let the periodic refresh start first. The notification branch currently
	// ignores refresh errors, so an immediate event would mask the reconnect.
	select {
	case err := <-actionDone:
		t.Fatalf("action ended before the device replied: %v", err)
	case <-time.After(3 * time.Second):
	}
	eventAt := time.Now()
	bus.Publish("device.attached", map[string]int{"id": 7})
	select {
	case err := <-actionDone:
		t.Fatalf("action ended before the device replied: %v (commits=%d polls=%d same-client=%t)", err, api.commits.Load(), api.polls.Load()-before, backend.clientSnapshot() == client)
	case <-holdTimer.C:
	}
	releasedAt := time.Now()
	release()
	select {
	case err := <-actionDone:
		if err != nil {
			t.Fatal("lost the device reply", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("action did not finish after device reply")
	}
	if result.Key != "name" || result.Value != "new" || result.Target == nil || result.Target.Instance != strings.Repeat("a", 32) {
		t.Fatal("reply changed", result)
	}
	if api.commits.Load() != 1 {
		t.Fatal("device action replayed", api.commits.Load())
	}
	if backend.clientSnapshot() != client {
		t.Fatal("poll connection was replaced")
	}
	select {
	case <-client.Done():
		t.Fatal("poll connection closed")
	default:
	}
	if acceptedCount.Load() != 2 {
		t.Fatal("expected one poll and one action connection", acceptedCount.Load())
	}
	api.mu.Lock()
	times := append([]time.Time(nil), api.pollTimes...)
	api.mu.Unlock()
	during := 0
	eventObserved := false
	for _, at := range times {
		if !at.Before(holdStarted) && at.Before(releasedAt) {
			during++
			if at.Sub(eventAt) < time.Second {
				eventObserved = true
			}
		}
	}
	if during < 3 || !eventObserved {
		t.Fatal("status or subscription stalled during action", during, eventObserved)
	}
	connectionsMu.Lock()
	observedConnections := append([]*countedTUIConnection(nil), connections...)
	connectionsMu.Unlock()
	measured := 0
	for _, connection := range observedConnections {
		connection.timingMu.Lock()
		latencies := append([]time.Duration(nil), connection.deviceLatencies...)
		connection.timingMu.Unlock()
		for _, latency := range latencies {
			measured++
			if latency >= time.Second {
				t.Fatal("device status dispatch took too long", latency)
			}
		}
	}
	if measured < 3 {
		t.Fatal("missing status latency measurements", measured)
	}
	if state := currentSoundView(); state.Error != "" {
		t.Fatal("false sound error", state.Error)
	}
	statusMu.RLock()
	message := statusMessage
	statusMu.RUnlock()
	if strings.Contains(message, "Service disconnected") {
		t.Fatal(message)
	}
	stop()
	if activeCount.Load() != 0 || closedCount.Load() != acceptedCount.Load() {
		t.Fatal("connection leak", acceptedCount.Load(), activeCount.Load(), closedCount.Load())
	}
}

type longReadTUIAPI struct {
	longTUIOperationAPI
	reads atomic.Int32
}

func (a *longReadTUIAPI) ListSettings(string) ([]ipc.SettingInfo, error) {
	a.reads.Add(1)
	a.enteredOnce.Do(func() { close(a.entered) })
	<-a.release
	return []ipc.SettingInfo{{Key: "name", Value: "new"}}, nil
}
func TestTUILongReadSurvivesSync(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, devices{}, -1, -1)
	oldBackend := currentTUIBackend()
	oldSound := currentSoundView()
	statusMu.RLock()
	oldMessage, oldError, oldUntil, oldKind := statusMessage, statusIsError, statusUntil, statusKind
	statusMu.RUnlock()
	setStatus("", false)
	t.Cleanup(func() {
		setTUIBackend(oldBackend)
		updateSoundView(oldSound)
		statusMu.Lock()
		statusMessage, statusIsError, statusUntil, statusKind = oldMessage, oldError, oldUntil, oldKind
		statusMu.Unlock()
	})
	api := &longReadTUIAPI{longTUIOperationAPI: longTUIOperationAPI{entered: make(chan struct{}), release: make(chan struct{})}}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(api.release) }) }
	defer release()
	bus := ipc.NewEventBus()
	backend := newTUIServiceTestBackend(t, api, bus)
	setTUIBackend(backend)
	client := backend.clientSnapshot()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	synced := make(chan struct{})
	go func() { defer close(synced); runTUIServiceSync(ctx, backend) }()
	t.Cleanup(func() {
		cancel()
		release()
		backend.close()
		select {
		case <-synced:
		case <-time.After(2 * time.Second):
			t.Error("read test sync did not stop")
		}
	})
	deadline := time.Now().Add(4 * time.Second)
	for api.features.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if api.features.Load() == 0 {
		t.Fatal("sync loop did not start")
	}
	var result []ipc.SettingInfo
	done := make(chan error, 1)
	go func() { done <- tuiIPCCall("settings.list", map[string]string{"device": "headset"}, &result) }()
	select {
	case <-api.entered:
	case <-time.After(time.Second):
		t.Fatal("read did not start")
	}
	before := api.polls.Load()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		t.Fatal("read ended before reply", err)
	case <-time.After(3 * time.Second):
	}
	eventAt := time.Now()
	bus.Publish("device.attached", map[string]int{"id": 7})
	select {
	case err := <-done:
		t.Fatal("read ended before reply", err)
	case <-timer.C:
	}
	releasedAt := time.Now()
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read did not finish")
	}
	if len(result) != 1 || result[0].Value != "new" || api.reads.Load() != 1 || api.commits.Load() != 0 {
		t.Fatal("wrong or replayed read", result, api.reads.Load(), api.commits.Load())
	}
	if backend.clientSnapshot() != client || api.polls.Load()-before < 3 {
		t.Fatal("read blocked status or caused reconnect")
	}
	api.mu.Lock()
	times := append([]time.Time(nil), api.pollTimes...)
	api.mu.Unlock()
	eventObserved := false
	for _, at := range times {
		if !at.Before(eventAt) && at.Before(releasedAt) && at.Sub(eventAt) < time.Second {
			eventObserved = true
		}
	}
	if !eventObserved || currentSoundView().Error != "" {
		t.Fatal("event or sound state stalled", eventObserved, currentSoundView())
	}
}
