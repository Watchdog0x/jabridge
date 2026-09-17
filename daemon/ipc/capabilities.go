package ipc

type ServiceCapabilities struct {
	Version          int      `json:"schemaVersion"`
	Protocol         string   `json:"protocol"`
	Framing          string   `json:"framing"`
	KeepaliveSeconds int      `json:"keepaliveSeconds"`
	LossyEvents      bool     `json:"lossyEvents"`
	Reads            []string `json:"reads"`
	Changes          []string `json:"changes"`
	Events           []string `json:"events"`
}

func serviceCapabilities(api API) ServiceCapabilities {
	c := ServiceCapabilities{Version: 1, Protocol: "JSON-RPC 2.0", Framing: "newline-delimited JSON", KeepaliveSeconds: 15, LossyEvents: true,
		Reads:   []string{"service.capabilities", "service.ping", "version", "devices.list", "device.battery", "device.firmware", "device.features", "settings.list", "bt.list", "bt.search.results", "bt.autopair", "device.busylight", "history.status"},
		Changes: []string{"device.select", "settings.set", "bt.connect", "bt.disconnect", "bt.forget", "bt.search", "bt.search.connect", "bt.pair", "bt.autopair", "device.busylight", "device.reset", "service.shutdown"}, Events: []string{"device.attached", "device.detached", "device.battery.update", "device.pairing.update"}}
	if _, ok := api.(DiagnosticAPI); ok {
		c.Reads = append(c.Reads, "diagnostics.device")
	}
	if _, ok := api.(HeadsetVolumeAPI); ok {
		// The firmware's volume access can initialize host-volume handling,
		// so keep this explicit operation out of automatic read-only probing.
		c.Changes = append(c.Changes, "device.volume")
	}
	if _, ok := api.(HeadsetVolumeReadAPI); ok {
		c.Reads = append(c.Reads, "device.volume.info")
		// Even GET_CUR can initialize firmware state. Only the explicit get
		// command performs it; background capability checks read metadata.
		c.Changes = append(c.Changes, "device.volume.get")
	}
	if _, ok := api.(SearchAPI); ok {
		c.Reads = append(c.Reads, "bt.search.status")
		c.Changes = append(c.Changes, "bt.search.stop")
	}
	if _, ok := api.(SoundAPI); ok {
		c.Reads = append(c.Reads, "sound.list")
		c.Changes = append(c.Changes, "sound.volume", "sound.mute", "sound.default")
		c.Events = append(c.Events, "sound.changed")
	}
	if _, ok := api.(SoundModeAPI); ok {
		c.Changes = append(c.Changes, "sound.mode")
	}
	if _, ok := api.(ButtonsAPI); ok {
		c.Reads = append(c.Reads, "buttons.status")
		c.Changes = append(c.Changes, "buttons.configure")
		c.Events = append(c.Events, "device.button", "device.signal", "buttons.changed", "media.action")
	}
	return c
}
