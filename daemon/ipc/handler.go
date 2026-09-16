// ipc/handler — JSON-RPC method dispatch for the Jabridge daemon.
//
// Reads newline-delimited JSON requests from a connection, dispatches
// to the right handler, and writes responses. The connection stays
// open for multiple requests (interactive session).

package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Watchdog0x/jabridge/internal/buildinfo"
	"github.com/Watchdog0x/jabridge/internal/history"
)

// DeviceInfo is the JSON-serializable device representation for IPC.
type DeviceInfo struct {
	ID               uint16            `json:"id"`
	Instance         string            `json:"instance,omitempty"`
	Topology         string            `json:"topology,omitempty"`
	Name             string            `json:"name"`
	PID              uint16            `json:"pid"`
	Variant          string            `json:"variant,omitempty"`
	Serial           string            `json:"serial"`
	IsDongle         bool              `json:"isDongle"`
	Connection       string            `json:"connection"`
	ParentID         uint16            `json:"parentId,omitempty"`
	Battery          *BatteryInfo      `json:"battery,omitempty"`
	Firmware         string            `json:"firmware,omitempty"`
	FirmwareIdentity string            `json:"firmwareIdentity,omitempty"`
	Selected         bool              `json:"selected"`
	Parts            []ControlPartInfo `json:"parts,omitempty"`
}

type ControlPartInfo struct {
	Role     string `json:"role"`
	Address  byte   `json:"address"`
	Variant  string `json:"variant,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	Ready    bool   `json:"ready"`
}

type BatteryInfo struct {
	Level      uint8                  `json:"level"`
	Charging   bool                   `json:"charging"`
	Low        bool                   `json:"low"`
	Component  int                    `json:"component"`
	Components []BatteryComponentInfo `json:"components,omitempty"`
}

type BatteryComponentInfo struct {
	Name      string `json:"name"`
	Level     uint8  `json:"level"`
	Charging  bool   `json:"charging"`
	Component int    `json:"component"`
}

type FeatureInfo struct {
	BusyLight    bool `json:"busyLight"`
	FactoryReset bool `json:"factoryReset"`
	PairingList  bool `json:"pairingList"`
	RemoteMMI    bool `json:"remoteMMI"`
	MusicEQ      bool `json:"musicEqualizer"`
	OnHeadDetect bool `json:"onHeadDetection"`
}

type PairedDeviceInfo struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Addr      string `json:"addr"`
	Connected bool   `json:"connected"`
}

type SettingInfo struct {
	Device     string         `json:"device"`
	Key        string         `json:"key"`
	Label      string         `json:"label"`
	Value      string         `json:"value"`
	Editable   bool           `json:"editable"`
	Choices    []string       `json:"choices,omitempty"`
	Help       string         `json:"help,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	MaxBytes   int            `json:"maxBytes,omitempty"`
	Target     *SettingTarget `json:"target,omitempty"`
	MayRestart bool           `json:"mayRestart,omitempty"`
	Component  string         `json:"component,omitempty"`
}

type SettingTarget struct {
	ID       uint16 `json:"id"`
	Instance string `json:"instance"`
	Topology string `json:"topology,omitempty"`
}

type TargetedSettingsAPI interface {
	SetSettingTarget(device, key, value string, target SettingTarget, previous string) (SettingInfo, error)
}

// DiagnosticCheck records evidence, not a blanket compatibility verdict.
type DiagnosticCheck struct {
	Feature string `json:"feature"`
	State   string `json:"state"`
	Detail  string `json:"detail"`
}

type DiagnosticAPI interface {
	DiagnoseDevice(id uint16) ([]DiagnosticCheck, error)
}

// API is the interface the handler calls to interact with the device layer.
// This decouples the IPC handler from jabraApi.go globals, making it testable.
type API interface {
	ListDevices() []DeviceInfo
	GetBattery() (*BatteryInfo, error)
	GetFirmware() string
	GetFeatures() FeatureInfo
	GetPairingList() []PairedDeviceInfo
	GetSearchList() []PairedDeviceInfo
	SearchNewDevices() error
	ConnectSearchDevice(index int) error
	ConnectRememberedDevice(index int) error
	DisconnectRememberedDevice(index int) error
	ForgetRememberedDevice(index int) error
	SetBTPairing(enable bool) error
	GetAutoPairing() (bool, error)
	SetAutoPairing(enable bool) error
	FactoryReset() error
	SetBusylightMode(mode string) error
	GetBusylightMode() string
	ListSettings(device string) ([]SettingInfo, error)
	SetSetting(device, key, value string) (SettingInfo, error)
	SelectDevice(id uint16) error
	Shutdown() error
}

// HandleConnection reads JSON-RPC requests from conn and dispatches them.
// Blocks until the connection is closed or an error occurs.
func HandleConnection(conn net.Conn, api API) {
	HandleConnectionWithBus(conn, api, nil, 30*time.Second)
}

// HandleConnectionWithTimeout serves one client and closes idle connections.
func HandleConnectionWithTimeout(conn net.Conn, api API, idleTimeout time.Duration) {
	HandleConnectionWithBus(conn, api, nil, idleTimeout)
}

func HandleConnectionWithBus(conn net.Conn, api API, bus *EventBus, idleTimeout time.Duration) {
	defer history.CapturePanic(history.Event{Component: "ipc-server", Action: "request"})
	defer func() { _ = conn.Close() }()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	enc := json.NewEncoder(conn)
	var writeMu sync.Mutex
	writeResponse := func(response Response) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return encodeResponse(conn, enc, response, idleTimeout)
	}
	var unsubscribe func()
	defer func() {
		if unsubscribe != nil {
			unsubscribe()
		}
	}()

	for {
		if idleTimeout > 0 {
			if err := conn.SetReadDeadline(time.Now().Add(idleTimeout)); err != nil {
				return
			}
		}
		if !scanner.Scan() {
			return
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			history.Record(history.Event{Component: "ipc-server", Action: "malformed", Phase: "error", Error: "malformed"})
			if err := writeResponse(ErrorResponse(nil, ErrCodeParse, "parse error")); err != nil {
				return
			}
			continue
		}
		if req.JSONRPC != "2.0" || req.Method == "" {
			if err := writeResponse(ErrorResponse(req.ID, ErrCodeInvalidReq, "invalid JSON-RPC request")); err != nil {
				return
			}
			continue
		}

		if req.Method == "subscribe" {
			if bus == nil {
				err := writeResponse(ErrorResponse(req.ID, ErrCodeInternal, "event subscriptions are unavailable"))
				if err != nil {
					return
				}
				continue
			}
			if unsubscribe != nil {
				unsubscribe()
			}
			events, cancel := bus.Subscribe()
			unsubscribe = cancel
			err := writeResponse(SuccessResponse(req.ID, map[string]bool{"subscribed": true}))
			if err != nil {
				return
			}
			go streamNotifications(conn, enc, &writeMu, events, idleTimeout)
			continue
		}

		resp := dispatch(req, api)
		err := writeResponse(resp)
		if err != nil {
			return
		}
	}
}

func streamNotifications(conn net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, events <-chan Notification, timeout time.Duration) {
	for notification := range events {
		writeMu.Lock()
		if timeout > 0 {
			_ = conn.SetWriteDeadline(time.Now().Add(timeout))
		}
		err := encoder.Encode(notification)
		writeMu.Unlock()
		if err != nil {
			_ = conn.Close()
			return
		}
	}
}

func encodeResponse(conn net.Conn, encoder *json.Encoder, response Response, timeout time.Duration) error {
	if timeout > 0 {
		if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}
	}
	return encoder.Encode(response)
}

func decodeParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("params contain trailing data")
	}
	return nil
}

var mutationMu sync.Mutex

func dispatch(req Request, api API) (response Response) {
	// Serialize entire mutation/read-back transactions across clients. The
	// transport lock alone only protects one packet exchange at a time.
	switch req.Method {
	case "settings.set", "device.select", "device.reset", "device.busylight", "bt.connect", "bt.disconnect", "bt.forget", "bt.pair", "bt.autopair", "bt.search", "bt.search.stop", "bt.search.connect":
		mutationMu.Lock()
		defer mutationMu.Unlock()
	}
	started := time.Now()
	entry := history.Event{Component: "ipc-server", Action: "request", Method: req.Method, Operation: history.NextOperation()}
	trace := history.TraceMethod(req.Method)
	if trace {
		entry.Phase = "start"
		history.Record(entry)
	}
	defer func() {
		if value := recover(); value != nil {
			entry.Phase = "panic"
			entry.Error = "panic"
			history.Record(entry)
			panic(value)
		}
		if !trace && response.Error == nil {
			return
		}
		entry.Phase = "ok"
		entry.DurationMS = time.Since(started).Milliseconds()
		if response.Error != nil {
			entry.Phase = "error"
			entry.RPCCode = response.Error.Code
			entry.Error = history.Classify(errors.New(response.Error.Message))
		}
		history.Record(entry)
	}()
	switch req.Method {
	case "service.capabilities":
		if err := decodeParams(req.Params, &struct{}{}); err != nil {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "service.capabilities takes no parameters")
		}
		return SuccessResponse(req.ID, serviceCapabilities(api))
	case "buttons.status", "buttons.configure":
		return dispatchButtons(req, api)
	case "sound.list", "sound.default", "sound.volume", "sound.mute", "sound.mode":
		return dispatchSound(req, api)
	case "history.status":
		return SuccessResponse(req.ID, history.LiveStatus())
	case "diagnostics.device":
		var params struct {
			ID *uint16 `json:"id"`
		}
		if err := decodeParams(req.Params, &params); err != nil || params.ID == nil {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "diagnostics.device requires numeric id")
		}
		diagnostics, ok := api.(DiagnosticAPI)
		if !ok {
			return ErrorResponse(req.ID, ErrCodeMethodNF, "device diagnostics unavailable in this service")
		}
		checks, err := diagnostics.DiagnoseDevice(*params.ID)
		if err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, checks)
	case "service.ping":
		return SuccessResponse(req.ID, map[string]bool{"ok": true})
	case "service.shutdown":
		if err := api.Shutdown(); err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, map[string]bool{"ok": true})
	case "version":
		return SuccessResponse(req.ID, map[string]string{
			"service": buildinfo.ServiceName,
			"version": buildinfo.Version,
		})

	case "devices.list":
		devs := api.ListDevices()
		return SuccessResponse(req.ID, devs)

	case "device.select":
		var params struct {
			ID *uint16 `json:"id"`
		}
		if err := decodeParams(req.Params, &params); err != nil || params.ID == nil {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "device.select requires numeric id")
		}
		if err := api.SelectDevice(*params.ID); err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, map[string]bool{"ok": true})

	case "settings.list":
		var params struct {
			Device string `json:"device"`
		}
		if err := decodeParams(req.Params, &params); err != nil || (params.Device != "dongle" && params.Device != "headset" && params.Device != "controller") {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "settings.list requires device dongle, headset or controller")
		}
		settings, err := api.ListSettings(params.Device)
		if err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, settings)

	case "settings.set":
		var params struct {
			Device   string         `json:"device"`
			Key      string         `json:"key"`
			Value    *string        `json:"value"`
			Target   *SettingTarget `json:"target"`
			Previous *string        `json:"previous"`
		}
		if err := decodeParams(req.Params, &params); err != nil ||
			(params.Device != "dongle" && params.Device != "headset" && params.Device != "controller") || params.Key == "" || params.Value == nil {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "settings.set requires device, key, and value")
		}
		if params.Target != nil || params.Previous != nil {
			if params.Target == nil || len(params.Target.Instance) != 32 || params.Previous == nil {
				return ErrorResponse(req.ID, ErrCodeInvalidP, "bound settings.set requires target and previous value")
			}
			targeted, ok := api.(TargetedSettingsAPI)
			if !ok {
				return ErrorResponse(req.ID, ErrCodeMethodNF, "service does not support device-bound setting edits")
			}
			setting, err := targeted.SetSettingTarget(params.Device, params.Key, *params.Value, *params.Target, *params.Previous)
			if err != nil {
				return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
			}
			return SuccessResponse(req.ID, setting)
		}
		setting, err := api.SetSetting(params.Device, params.Key, *params.Value)
		if err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, setting)

	case "device.battery":
		bat, err := api.GetBattery()
		if err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, bat)

	case "device.firmware":
		ver := api.GetFirmware()
		return SuccessResponse(req.ID, map[string]string{"version": ver})

	case "device.features":
		feat := api.GetFeatures()
		return SuccessResponse(req.ID, feat)

	case "bt.list":
		list := api.GetPairingList()
		return SuccessResponse(req.ID, list)

	case "bt.search.results":
		return SuccessResponse(req.ID, api.GetSearchList())
	case "bt.search.status", "bt.search.stop":
		search, ok := api.(SearchAPI)
		if !ok {
			return ErrorResponse(req.ID, ErrCodeMethodNF, "search lifecycle unavailable")
		}
		if err := decodeParams(req.Params, &struct{}{}); err != nil {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "search lifecycle takes no parameters")
		}
		if req.Method == "bt.search.stop" {
			if err := search.StopSearch(); err != nil {
				return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
			}
			return SuccessResponse(req.ID, map[string]bool{"ok": true})
		}
		return SuccessResponse(req.ID, search.GetSearchState())

	case "bt.search":
		if err := decodeParams(req.Params, &struct{}{}); err != nil {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "bt.search takes no parameters")
		}
		if err := api.SearchNewDevices(); err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, map[string]bool{"ok": true})

	case "bt.search.connect":
		if bound, ok := api.(BoundSearchAPI); ok {
			var params struct {
				Index   *int   `json:"index"`
				Session string `json:"session"`
			}
			if err := decodeParams(req.Params, &params); err != nil || params.Index == nil || *params.Index < 0 || *params.Index >= 64 || len(params.Session) != 32 {
				return ErrorResponse(req.ID, ErrCodeInvalidP, "search connect requires index and current session")
			}
			if err := bound.ConnectSearchDeviceBound(*params.Index, params.Session); err != nil {
				return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
			}
			return SuccessResponse(req.ID, map[string]bool{"ok": true})
		}
		index, err := decodeDeviceIndex(req.Params)
		if err != nil {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "bt.search.connect requires a non-negative index")
		}
		if err := api.ConnectSearchDevice(index); err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, map[string]bool{"ok": true})

	case "bt.connect", "bt.disconnect", "bt.forget":
		index, err := decodeDeviceIndex(req.Params)
		if err != nil {
			return ErrorResponse(req.ID, ErrCodeInvalidP, req.Method+" requires a non-negative index")
		}
		switch req.Method {
		case "bt.connect":
			err = api.ConnectRememberedDevice(index)
		case "bt.disconnect":
			err = api.DisconnectRememberedDevice(index)
		case "bt.forget":
			err = api.ForgetRememberedDevice(index)
		}
		if err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, map[string]bool{"ok": true})

	case "bt.pair":
		var params struct {
			Enable *bool `json:"enable"`
		}
		if err := decodeParams(req.Params, &params); err != nil || params.Enable == nil {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "bt.pair requires boolean enable")
		}
		if err := api.SetBTPairing(*params.Enable); err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, map[string]bool{"ok": true})

	case "bt.autopair":
		var params struct {
			Enable *bool `json:"enable,omitempty"`
		}
		if err := decodeParams(req.Params, &params); err != nil {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "bt.autopair params must contain boolean enable")
		}
		if params.Enable != nil {
			if err := api.SetAutoPairing(*params.Enable); err != nil {
				return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
			}
			return SuccessResponse(req.ID, map[string]bool{"enabled": *params.Enable})
		}
		enabled, err := api.GetAutoPairing()
		if err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, map[string]bool{"enabled": enabled})

	case "device.busylight":
		var params struct {
			Mode string `json:"mode,omitempty"`
		}
		if err := decodeParams(req.Params, &params); err != nil {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "device.busylight params must contain string mode")
		}
		if params.Mode != "" {
			if err := api.SetBusylightMode(params.Mode); err != nil {
				return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
			}
			return SuccessResponse(req.ID, map[string]string{"mode": params.Mode})
		}
		return SuccessResponse(req.ID, map[string]string{"mode": api.GetBusylightMode()})

	case "device.reset":
		var params struct {
			Confirm string `json:"confirm"`
		}
		if err := decodeParams(req.Params, &params); err != nil || params.Confirm != "ERASE_REMEMBERED_HEADSETS" {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "device.reset requires the exact confirmation")
		}
		if err := api.FactoryReset(); err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, map[string]bool{"ok": true})

	default:
		return ErrorResponse(req.ID, ErrCodeMethodNF, fmt.Sprintf("unknown method: %s", req.Method))
	}
}

func decodeDeviceIndex(raw json.RawMessage) (int, error) {
	var params struct {
		Index *int `json:"index"`
	}
	if err := decodeParams(raw, &params); err != nil || params.Index == nil || *params.Index < 0 {
		return 0, errors.New("invalid device index")
	}
	return *params.Index, nil
}

// WriteNotification sends a server-initiated notification to a connection.
// Used for push events like call.started, device.attached, etc.
func WriteNotification(w io.Writer, method string, params interface{}) error {
	notif := Notification{JSONRPC: "2.0", Method: method, Params: params}
	return json.NewEncoder(w).Encode(notif)
}
