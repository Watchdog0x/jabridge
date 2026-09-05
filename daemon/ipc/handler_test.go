package ipc

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// mockAPI implements the API interface for testing.
type mockAPI struct{}

func (m *mockAPI) ListDevices() []DeviceInfo {
	return []DeviceInfo{
		{Name: "Jabra Evolve2 85", PID: 0x24b9, Serial: "A1B2C3D4E5F6", IsDongle: false,
			Battery:  &BatteryInfo{Level: 50, Charging: false, Low: false, Component: 1},
			Firmware: "1.5.7"},
		{Name: "Jabra Link 380", PID: 0x24c7, Serial: "D0D1D2D3D4D5", IsDongle: true},
	}
}

func (m *mockAPI) GetBattery() (*BatteryInfo, error) {
	return &BatteryInfo{Level: 50, Charging: false, Low: false, Component: 1}, nil
}

func (m *mockAPI) GetFirmware() string { return "1.5.7" }

func (m *mockAPI) GetFeatures() FeatureInfo {
	return FeatureInfo{BusyLight: true, FactoryReset: true, PairingList: true}
}

func (m *mockAPI) GetPairingList() []PairedDeviceInfo {
	return []PairedDeviceInfo{{Name: "Test Device", Addr: "AA:BB:CC:DD:EE:FF", Connected: true}}
}

func (m *mockAPI) GetSearchList() []PairedDeviceInfo    { return nil }
func (m *mockAPI) SearchNewDevices() error              { return nil }
func (m *mockAPI) ConnectSearchDevice(int) error        { return nil }
func (m *mockAPI) ConnectRememberedDevice(int) error    { return nil }
func (m *mockAPI) DisconnectRememberedDevice(int) error { return nil }
func (m *mockAPI) ForgetRememberedDevice(int) error     { return nil }
func (m *mockAPI) SetBTPairing(bool) error              { return nil }
func (m *mockAPI) GetAutoPairing() (bool, error)        { return true, nil }
func (m *mockAPI) SetAutoPairing(bool) error            { return nil }
func (m *mockAPI) FactoryReset() error                  { return nil }
func (m *mockAPI) SetBusylightMode(string) error        { return nil }
func (m *mockAPI) GetBusylightMode() string             { return "auto" }
func (m *mockAPI) ListSettings(device string) ([]SettingInfo, error) {
	return []SettingInfo{{Device: device, Key: "test", Label: "Test", Value: "On", Editable: true}}, nil
}
func (m *mockAPI) SetSetting(device, key, value string) (SettingInfo, error) {
	return SettingInfo{Device: device, Key: key, Label: "Test", Value: value, Editable: true}, nil
}
func (m *mockAPI) SelectDevice(uint16) error { return nil }
func (m *mockAPI) Shutdown() error           { return nil }

func sendRequest(t *testing.T, conn net.Conn, method string, params interface{}) Response {
	t.Helper()
	id := json.RawMessage(`1`)
	req := Request{JSONRPC: "2.0", ID: id, Method: method}
	if params != nil {
		p, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		req.Params = p
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatal("no response")
	}
	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v (raw: %s)", err, scanner.Text())
	}
	return resp
}

func setupTest(t *testing.T) (client net.Conn, cleanup func()) {
	t.Helper()
	server, client := net.Pipe()
	go HandleConnection(server, &mockAPI{})
	return client, func() { _ = client.Close() }
}

func TestVersion(t *testing.T) {
	client, cleanup := setupTest(t)
	defer cleanup()

	resp := sendRequest(t, client, "version", nil)
	if resp.Error != nil {
		t.Fatalf("error: %v", resp.Error)
	}
	m := resp.Result.(map[string]interface{})
	if m["service"] != "jabridge" {
		t.Errorf("service = %v, want jabridge", m["service"])
	}
	if m["version"] != "1.0.0" {
		t.Errorf("version = %v, want 1.0.0", m["version"])
	}
}

func TestServicePing(t *testing.T) {
	client, cleanup := setupTest(t)
	defer cleanup()
	response := sendRequest(t, client, "service.ping", nil)
	if response.Error != nil {
		t.Fatalf("ping error: %v", response.Error)
	}
	result := response.Result.(map[string]interface{})
	if result["ok"] != true {
		t.Fatalf("ping result = %#v", result)
	}
}

func TestServiceShutdownRequest(t *testing.T) {
	client, cleanup := setupTest(t)
	defer cleanup()
	response := sendRequest(t, client, "service.shutdown", nil)
	if response.Error != nil {
		t.Fatalf("shutdown error: %v", response.Error)
	}
}

func TestSubscribeReceivesNotification(t *testing.T) {
	server, client := net.Pipe()
	bus := NewEventBus()
	go HandleConnectionWithBus(server, &mockAPI{}, bus, time.Second)
	defer func() { _ = client.Close() }()

	encoder := json.NewEncoder(client)
	decoder := json.NewDecoder(client)
	request := Request{JSONRPC: "2.0", ID: json.RawMessage(`7`), Method: "subscribe"}
	if err := encoder.Encode(request); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatalf("subscribe error: %v", response.Error)
	}

	bus.Publish("device.attached", DeviceInfo{Name: "Test", PID: 0x1234})
	var notification Notification
	if err := decoder.Decode(&notification); err != nil {
		t.Fatal(err)
	}
	if notification.Method != "device.attached" {
		t.Fatalf("notification = %#v", notification)
	}
}

func TestDevicesList(t *testing.T) {
	client, cleanup := setupTest(t)
	defer cleanup()

	resp := sendRequest(t, client, "devices.list", nil)
	if resp.Error != nil {
		t.Fatalf("error: %v", resp.Error)
	}
	devs, ok := resp.Result.([]interface{})
	if !ok {
		t.Fatalf("result not array: %T", resp.Result)
	}
	if len(devs) != 2 {
		t.Errorf("got %d devices, want 2", len(devs))
	}
}

func TestBattery(t *testing.T) {
	client, cleanup := setupTest(t)
	defer cleanup()

	resp := sendRequest(t, client, "device.battery", nil)
	if resp.Error != nil {
		t.Fatalf("error: %v", resp.Error)
	}
	m := resp.Result.(map[string]interface{})
	if m["level"].(float64) != 50 {
		t.Errorf("level = %v, want 50", m["level"])
	}
}

func TestFirmware(t *testing.T) {
	client, cleanup := setupTest(t)
	defer cleanup()

	resp := sendRequest(t, client, "device.firmware", nil)
	if resp.Error != nil {
		t.Fatalf("error: %v", resp.Error)
	}
	m := resp.Result.(map[string]interface{})
	if m["version"] != "1.5.7" {
		t.Errorf("version = %v, want 1.5.7", m["version"])
	}
}

func TestFeatures(t *testing.T) {
	client, cleanup := setupTest(t)
	defer cleanup()

	resp := sendRequest(t, client, "device.features", nil)
	if resp.Error != nil {
		t.Fatalf("error: %v", resp.Error)
	}
	m := resp.Result.(map[string]interface{})
	if m["busyLight"] != true {
		t.Errorf("busyLight = %v, want true", m["busyLight"])
	}
}

func TestBTList(t *testing.T) {
	client, cleanup := setupTest(t)
	defer cleanup()

	resp := sendRequest(t, client, "bt.list", nil)
	if resp.Error != nil {
		t.Fatalf("error: %v", resp.Error)
	}
	devs := resp.Result.([]interface{})
	if len(devs) != 1 {
		t.Errorf("got %d paired devices, want 1", len(devs))
	}
}

func TestRememberedDeviceAndSearchMethods(t *testing.T) {
	client, cleanup := setupTest(t)
	defer cleanup()
	for _, method := range []string{"bt.search.connect", "bt.connect", "bt.disconnect", "bt.forget"} {
		response := sendRequest(t, client, method, map[string]int{"index": 0})
		if response.Error != nil {
			t.Fatalf("%s error: %v", method, response.Error)
		}
	}
	response := sendRequest(t, client, "bt.search.results", nil)
	if response.Error != nil {
		t.Fatalf("bt.search.results error: %v", response.Error)
	}
}

func TestFactoryResetRequiresExactConfirmation(t *testing.T) {
	client, cleanup := setupTest(t)
	defer cleanup()
	if response := sendRequest(t, client, "device.reset", nil); response.Error == nil || response.Error.Code != ErrCodeInvalidP {
		t.Fatalf("reset without confirmation = %#v", response)
	}
	response := sendRequest(t, client, "device.reset", map[string]string{"confirm": "ERASE_REMEMBERED_HEADSETS"})
	if response.Error != nil {
		t.Fatalf("confirmed reset error: %v", response.Error)
	}
}

func TestAutoPairGet(t *testing.T) {
	client, cleanup := setupTest(t)
	defer cleanup()

	resp := sendRequest(t, client, "bt.autopair", nil)
	if resp.Error != nil {
		t.Fatalf("error: %v", resp.Error)
	}
	m := resp.Result.(map[string]interface{})
	if m["enabled"] != true {
		t.Errorf("enabled = %v, want true", m["enabled"])
	}
}

func TestSettingsAndDeviceSelection(t *testing.T) {
	client, cleanup := setupTest(t)
	defer cleanup()
	listed := sendRequest(t, client, "settings.list", map[string]string{"device": "dongle"})
	if listed.Error != nil {
		t.Fatalf("settings.list error: %v", listed.Error)
	}
	settings := listed.Result.([]interface{})
	if len(settings) != 1 {
		t.Fatalf("settings.list result = %#v", listed.Result)
	}
	set := sendRequest(t, client, "settings.set", map[string]string{"device": "dongle", "key": "test", "value": "Off"})
	if set.Error != nil {
		t.Fatalf("settings.set error: %v", set.Error)
	}
	selected := sendRequest(t, client, "device.select", map[string]uint16{"id": 2})
	if selected.Error != nil {
		t.Fatalf("device.select error: %v", selected.Error)
	}
}

func TestUnknownMethod(t *testing.T) {
	client, cleanup := setupTest(t)
	defer cleanup()

	resp := sendRequest(t, client, "nonexistent.method", nil)
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != ErrCodeMethodNF {
		t.Errorf("code = %d, want %d", resp.Error.Code, ErrCodeMethodNF)
	}
}

func TestInvalidWriteParamsAreRejected(t *testing.T) {
	tests := []Request{
		{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "bt.pair", Params: json.RawMessage(`{"enable":"yes"}`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "bt.pair"},
		{JSONRPC: "2.0", ID: json.RawMessage(`3`), Method: "bt.autopair", Params: json.RawMessage(`{"unknown":true}`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`4`), Method: "device.busylight", Params: json.RawMessage(`{"mode":7}`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`5`), Method: "device.select", Params: json.RawMessage(`{"id":"one"}`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`6`), Method: "settings.list", Params: json.RawMessage(`{"device":"other"}`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`7`), Method: "settings.set", Params: json.RawMessage(`{"device":"dongle","key":"","value":"on"}`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`8`), Method: "bt.connect", Params: json.RawMessage(`{"index":-1}`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`9`), Method: "device.reset", Params: json.RawMessage(`{"confirm":"yes"}`)},
	}
	api := &mockAPI{}
	for _, request := range tests {
		response := dispatch(request, api)
		if response.Error == nil || response.Error.Code != ErrCodeInvalidP {
			t.Fatalf("%s response = %#v, want invalid params", request.Method, response)
		}
	}
}

func TestConnectionRejectsWrongJSONRPCVersion(t *testing.T) {
	client, cleanup := setupTest(t)
	defer cleanup()
	if _, err := client.Write([]byte(`{"jsonrpc":"1.0","id":1,"method":"version"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.NewDecoder(client).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != ErrCodeInvalidReq {
		t.Fatalf("response = %#v, want invalid request", response)
	}
}

func TestIdleConnectionIsClosed(t *testing.T) {
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		HandleConnectionWithTimeout(server, &mockAPI{}, 25*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle IPC connection was not closed")
	}
	_ = client.Close()
}

func TestMultipleRequests(t *testing.T) {
	client, cleanup := setupTest(t)
	defer cleanup()

	// Send two requests on the same connection
	r1 := sendRequest(t, client, "version", nil)
	r2 := sendRequest(t, client, "device.firmware", nil)

	if r1.Error != nil || r2.Error != nil {
		t.Fatal("multiple requests should both succeed")
	}
}
