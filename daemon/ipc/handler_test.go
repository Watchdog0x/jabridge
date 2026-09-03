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

func (m *mockAPI) SearchNewDevices() error       { return nil }
func (m *mockAPI) SetBTPairing(bool) error       { return nil }
func (m *mockAPI) GetAutoPairing() (bool, error) { return true, nil }
func (m *mockAPI) SetAutoPairing(bool) error     { return nil }
func (m *mockAPI) FactoryReset() error           { return nil }
func (m *mockAPI) SetBusylightMode(string) error { return nil }
func (m *mockAPI) GetBusylightMode() string      { return "auto" }

func sendRequest(t *testing.T, conn net.Conn, method string, params interface{}) Response {
	t.Helper()
	id := json.RawMessage(`1`)
	req := Request{JSONRPC: "2.0", ID: id, Method: method}
	if params != nil {
		p, _ := json.Marshal(params)
		req.Params = p
	}
	data, _ := json.Marshal(req)
	data = append(data, '\n')
	conn.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(time.Second))
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
	return client, func() { client.Close() }
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
