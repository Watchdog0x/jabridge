// ipc/handler — JSON-RPC method dispatch for the Jabridge daemon.
//
// Reads newline-delimited JSON requests from a connection, dispatches
// to the right handler, and writes responses. The connection stays
// open for multiple requests (interactive session).

package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"

	"github.com/Watchdog0x/jabridge/internal/buildinfo"
)

// DeviceInfo is the JSON-serializable device representation for IPC.
type DeviceInfo struct {
	Name     string       `json:"name"`
	PID      uint16       `json:"pid"`
	Serial   string       `json:"serial"`
	IsDongle bool         `json:"isDongle"`
	Battery  *BatteryInfo `json:"battery,omitempty"`
	Firmware string       `json:"firmware,omitempty"`
}

type BatteryInfo struct {
	Level     uint8 `json:"level"`
	Charging  bool  `json:"charging"`
	Low       bool  `json:"low"`
	Component int   `json:"component"`
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
	Name      string `json:"name"`
	Addr      string `json:"addr"`
	Connected bool   `json:"connected"`
}

// API is the interface the handler calls to interact with the device layer.
// This decouples the IPC handler from jabraApi.go globals, making it testable.
type API interface {
	ListDevices() []DeviceInfo
	GetBattery() (*BatteryInfo, error)
	GetFirmware() string
	GetFeatures() FeatureInfo
	GetPairingList() []PairedDeviceInfo
	SearchNewDevices() error
	SetBTPairing(enable bool) error
	GetAutoPairing() (bool, error)
	SetAutoPairing(enable bool) error
	FactoryReset() error
	SetBusylightMode(mode string) error
	GetBusylightMode() string
}

// HandleConnection reads JSON-RPC requests from conn and dispatches them.
// Blocks until the connection is closed or an error occurs.
func HandleConnection(conn net.Conn, api API) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	enc := json.NewEncoder(conn)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			enc.Encode(ErrorResponse(nil, ErrCodeParse, "parse error"))
			continue
		}
		if req.Method == "" {
			enc.Encode(ErrorResponse(req.ID, ErrCodeInvalidReq, "missing method"))
			continue
		}

		resp := dispatch(req, api)
		enc.Encode(resp)
	}
}

func dispatch(req Request, api API) Response {
	switch req.Method {
	case "version":
		return SuccessResponse(req.ID, map[string]string{
			"service": buildinfo.ServiceName,
			"version": buildinfo.Version,
		})

	case "devices.list":
		devs := api.ListDevices()
		return SuccessResponse(req.ID, devs)

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

	case "bt.search":
		if err := api.SearchNewDevices(); err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, map[string]bool{"ok": true})

	case "bt.pair":
		var params struct {
			Enable bool `json:"enable"`
		}
		if req.Params != nil {
			json.Unmarshal(req.Params, &params)
		}
		if err := api.SetBTPairing(params.Enable); err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, map[string]bool{"ok": true})

	case "bt.autopair":
		var params struct {
			Enable *bool `json:"enable,omitempty"`
		}
		if req.Params != nil {
			json.Unmarshal(req.Params, &params)
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
		if req.Params != nil {
			json.Unmarshal(req.Params, &params)
		}
		if params.Mode != "" {
			if err := api.SetBusylightMode(params.Mode); err != nil {
				return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
			}
			return SuccessResponse(req.ID, map[string]string{"mode": params.Mode})
		}
		return SuccessResponse(req.ID, map[string]string{"mode": api.GetBusylightMode()})

	case "device.reset":
		if err := api.FactoryReset(); err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, map[string]bool{"ok": true})

	default:
		return ErrorResponse(req.ID, ErrCodeMethodNF, fmt.Sprintf("unknown method: %s", req.Method))
	}
}

// WriteNotification sends a server-initiated notification to a connection.
// Used for push events like call.started, device.attached, etc.
func WriteNotification(w io.Writer, method string, params interface{}) error {
	notif := Notification{JSONRPC: "2.0", Method: method, Params: params}
	return json.NewEncoder(w).Encode(notif)
}
