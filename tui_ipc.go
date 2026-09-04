package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/buildinfo"
)

type tuiIPCBackend struct {
	mu     sync.RWMutex
	client *ipc.Client
	socket string
}

var (
	tuiBackendMu sync.RWMutex
	tuiBackend   *tuiIPCBackend
)

func currentTUIBackend() *tuiIPCBackend {
	tuiBackendMu.RLock()
	defer tuiBackendMu.RUnlock()
	return tuiBackend
}

func setTUIBackend(backend *tuiIPCBackend) {
	tuiBackendMu.Lock()
	tuiBackend = backend
	tuiBackendMu.Unlock()
}

func (backend *tuiIPCBackend) clientSnapshot() *ipc.Client {
	if backend == nil {
		return nil
	}
	backend.mu.RLock()
	defer backend.mu.RUnlock()
	return backend.client
}

func (backend *tuiIPCBackend) replaceClient(client *ipc.Client) {
	backend.mu.Lock()
	old := backend.client
	backend.client = client
	backend.mu.Unlock()
	if old != nil && old != client {
		_ = old.Close()
	}
}

func (backend *tuiIPCBackend) close() {
	if backend != nil {
		backend.replaceClient(nil)
	}
}

func connectTUIService() (*tuiIPCBackend, error) {
	socket := ipcSocketPath()
	if client, version, err := dialServiceVersion(socket, 700*time.Millisecond); err == nil && version == buildinfo.Version {
		return &tuiIPCBackend{client: client, socket: socket}, nil
	} else if client != nil {
		_ = client.Close()
	}

	fmt.Println("Starting Jabridge service...")
	installedExecutable, err := installUserFiles()
	if err != nil {
		return nil, fmt.Errorf("install user service: %w", err)
	}
	if err := enableUserService(installedExecutable); err != nil {
		return nil, err
	}
	client, version, err := dialServiceVersion(socket, 5*time.Second)
	if err != nil {
		return nil, err
	}
	if version != buildinfo.Version {
		_ = client.Close()
		return nil, fmt.Errorf("service version %s does not match app version %s", version, buildinfo.Version)
	}
	return &tuiIPCBackend{client: client, socket: socket}, nil
}

func dialServiceVersion(socket string, timeout time.Duration) (*ipc.Client, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client, err := ipc.DialWithRetry(ctx, socket)
	if err != nil {
		return nil, "", fmt.Errorf("connect to Jabridge service: %w", err)
	}
	var result struct {
		Version string `json:"version"`
	}
	callContext, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if err := client.Call(callContext, "version", nil, &result); err != nil {
		_ = client.Close()
		return nil, "", err
	}
	return client, result.Version, nil
}

func tuiIPCCall(method string, params, result any) error {
	backend := currentTUIBackend()
	if backend == nil {
		return errors.New("jabridge service is not connected")
	}
	client := backend.clientSnapshot()
	if client == nil {
		return errors.New("jabridge service is not connected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return client.Call(ctx, method, params, result)
}

func initialTUIServiceSync(backend *tuiIPCBackend) error {
	client := backend.clientSnapshot()
	if client == nil {
		return errors.New("jabridge service is not connected")
	}
	if err := syncTUIState(client); err != nil {
		return err
	}
	firstScanComplete.Store(true)
	return nil
}

func runTUIServiceSync(ctx context.Context, backend *tuiIPCBackend) {
	for {
		client := backend.clientSnapshot()
		if client == nil {
			return
		}
		subscribeContext, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := client.Subscribe(subscribeContext)
		cancel()
		if err != nil {
			if !reconnectTUIService(ctx, backend) {
				return
			}
			continue
		}

		refresh := time.NewTicker(2 * time.Second)
		keepalive := time.NewTicker(20 * time.Second)
		connected := true
		for connected {
			select {
			case <-ctx.Done():
				refresh.Stop()
				keepalive.Stop()
				return
			case <-client.Done():
				connected = false
			case <-client.Notifications():
				_ = syncTUIState(client)
			case <-refresh.C:
				if err := syncTUIState(client); err != nil {
					connected = false
				}
			case <-keepalive.C:
				pingContext, stop := context.WithTimeout(ctx, 3*time.Second)
				err := client.Ping(pingContext)
				stop()
				if err != nil {
					connected = false
				}
			}
		}
		refresh.Stop()
		keepalive.Stop()
		if !reconnectTUIService(ctx, backend) {
			return
		}
	}
}

func reconnectTUIService(ctx context.Context, backend *tuiIPCBackend) bool {
	setStatus("Service disconnected. Reconnecting...", true)
	client, err := ipc.DialWithRetry(ctx, backend.socket)
	if err != nil {
		return false
	}
	backend.replaceClient(client)
	if err := syncTUIState(client); err != nil {
		_ = client.Close()
		return false
	}
	setStatus("Service reconnected", false)
	return true
}

func syncTUIState(client *ipc.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var deviceInfos []ipc.DeviceInfo
	if err := client.Call(ctx, "devices.list", nil, &deviceInfos); err != nil {
		return err
	}
	var pairings []ipc.PairedDeviceInfo
	_ = client.Call(ctx, "bt.list", nil, &pairings)
	var features ipc.FeatureInfo
	_ = client.Call(ctx, "device.features", nil, &features)
	replaceTUIDeviceState(deviceInfos, pairings, features)
	return nil
}

func replaceTUIDeviceState(infos []ipc.DeviceInfo, pairings []ipc.PairedDeviceInfo, features ipc.FeatureInfo) {
	manager := make(devices, len(infos))
	serviceDongle, serviceHeadset := -1, -1
	for _, info := range infos {
		connection := deviceConnectionType_USB
		if info.Connection == "dongle" {
			connection = deviceConnectionType_BT
		}
		device := &jabra_DeviceInfo{
			deviceID: info.ID, deviceName: info.Name, productID: info.PID,
			vendorID: jabraVendorID, isDongle: info.IsDongle,
			variantType: info.Variant, firmwareVersion: info.Firmware,
			deviceConnection: connection, parentDeviceID: info.ParentID,
			featureFlags: &featureFlags{
				busyLight: features.BusyLight, factoryReset: features.FactoryReset,
				pairingList: features.PairingList, remoteMMI: features.RemoteMMI,
				musicEqualizer: features.MusicEQ, onHeadDetection: features.OnHeadDetect,
			},
		}
		device.batteryStatus = batteryStatusFromIPC(info.Battery)
		manager[int(info.ID)] = device
		if info.Selected {
			if info.IsDongle {
				serviceDongle = int(info.ID)
			} else {
				serviceHeadset = int(info.ID)
			}
		}
	}
	for id, device := range manager {
		if !device.isDongle {
			continue
		}
		list := &pairingList{count: uint16(len(pairings)), listType: pairedDevices}
		for _, pairing := range pairings {
			list.pairedDevices = append(list.pairedDevices, pairedDevice{
				deviceName: pairing.Name, isConnected: pairing.Connected, databaseIndex: uint16(pairing.ID),
			})
		}
		device.pairingList = list
		if len(pairings) > 0 {
			device.featureFlags.pairingList = true
		}
		manager[id] = device
		break
	}

	deviceStateMu.Lock()
	oldDongle, oldHeadset := selectedDongle, selectedHeadset
	newDongle := selectedDongle
	newHeadset := selectedHeadset
	if serviceDongle >= 0 {
		newDongle = serviceDongle
	} else if current := manager[newDongle]; current == nil || !current.isDongle {
		newDongle = firstDeviceIndexIn(manager, true)
	}
	if serviceHeadset >= 0 {
		newHeadset = serviceHeadset
	} else if current := manager[newHeadset]; current == nil || current.isDongle {
		newHeadset = firstDeviceIndexIn(manager, false)
	}
	changed := !reflect.DeepEqual(deviceManager, manager) || oldDongle != newDongle || oldHeadset != newHeadset
	deviceManager = manager
	selectedDongle = newDongle
	selectedHeadset = newHeadset
	deviceStateMu.Unlock()
	if changed {
		requestUIRedraw()
	}
}

func firstDeviceIndexIn(manager devices, dongle bool) int {
	selected := -1
	for id, device := range manager {
		if device != nil && device.isDongle == dongle && (selected < 0 || id < selected) {
			selected = id
		}
	}
	return selected
}

func batteryStatusFromIPC(info *ipc.BatteryInfo) *batteryStatus {
	if info == nil || info.Level > 100 {
		return nil
	}
	status := &batteryStatus{
		levelInPercent: info.Level, charging: info.Charging,
		batteryLow: info.Low, component: batteryComponent(info.Component),
	}
	for _, component := range info.Components {
		if component.Level > 100 {
			continue
		}
		status.components = append(status.components, batteryComponentStatus{
			label: component.Name, levelInPercent: component.Level,
			charging: component.Charging, component: batteryComponent(component.Component),
		})
	}
	return status
}

func loadIPCSettings(scope settingScope) ([]menuItem, []deviceSettingValue, error) {
	deviceName := settingScopeName(scope)
	var response []ipc.SettingInfo
	if err := tuiIPCCall("settings.list", map[string]string{"device": deviceName}, &response); err != nil {
		return nil, nil, err
	}
	device, exists := selectedSettingsDevice(scope)
	if !exists {
		return nil, nil, fmt.Errorf("no %s connected", deviceName)
	}
	connection := "Direct USB"
	if device.deviceConnection == deviceConnectionType_BT {
		connection = "Through dongle"
	}
	lines := []menuItem{{id: -1, label: fmt.Sprintf("Device:             %s", device.deviceName)}}
	if scope == settingScopeHeadset {
		lines = append(lines, menuItem{id: -1, label: fmt.Sprintf("Connection:         %s", connection)})
	}
	lines = append(lines, menuItem{id: -1, label: fmt.Sprintf("USB ID:             0b0e:%04x", device.productID)})
	if scope == settingScopeDongle {
		lines = append(lines,
			menuItem{id: -1, label: fmt.Sprintf("Firmware:           %s", valueOrUnknown(device.firmwareVersion))},
			menuItem{id: -1, label: fmt.Sprintf("Remembered devices: %d", len(responsePairingList(device)))},
		)
	}
	values := make([]deviceSettingValue, 0, len(response))
	for _, setting := range response {
		editable := setting.Editable
		label := setting.Label
		if editable && len(setting.Choices) == 0 {
			editable = false
			label += " (edit with CLI)"
		}
		remote := &remoteSettingValue{
			Device: setting.Device, Key: setting.Key, Label: label,
			Value: setting.Value, Editable: editable,
			Choices: append([]string(nil), setting.Choices...),
		}
		values = append(values, deviceSettingValue{Remote: remote})
	}
	return lines, values, nil
}

func responsePairingList(device *jabra_DeviceInfo) []pairedDevice {
	if device == nil || device.pairingList == nil {
		return nil
	}
	return device.pairingList.pairedDevices
}

func setIPCSetting(setting deviceSettingValue, value string) error {
	if setting.Remote == nil {
		return errors.New("setting is not an IPC setting")
	}
	params := map[string]string{
		"device": setting.Remote.Device,
		"key":    setting.Remote.Key,
		"value":  value,
	}
	var updated ipc.SettingInfo
	return tuiIPCCall("settings.set", params, &updated)
}

func runIPCAction(method string, params any) error {
	var result map[string]bool
	return tuiIPCCall(method, params, &result)
}

func firmwareVersionForTUI(device *jabra_DeviceInfo) (string, error) {
	if currentTUIBackend() == nil {
		return readFirmwareVersion(device)
	}
	if device == nil || strings.TrimSpace(device.firmwareVersion) == "" {
		return "", errors.New("firmware version unavailable")
	}
	return device.firmwareVersion, nil
}
