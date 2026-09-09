package ipc

type SearchState struct {
	State    string             `json:"state"`
	Count    int                `json:"count"`
	Session  string             `json:"session,omitempty"`
	DeviceID uint16             `json:"deviceId"`
	Error    string             `json:"error,omitempty"`
	Devices  []PairedDeviceInfo `json:"devices,omitempty"`
}

type SearchAPI interface {
	GetSearchState() SearchState
	StopSearch() error
}

type BoundSearchAPI interface {
	ConnectSearchDeviceBound(int, string) error
}
