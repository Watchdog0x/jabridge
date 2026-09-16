package headsetvolume

// Capabilities describe operations validated for this model, firmware and
// connection. USB audio controls on a dongle do not establish that it can set
// the connected headset's own gain.
type Capabilities struct {
	Model      string `json:"model,omitempty"`
	Connection string `json:"connection"`
	Read       string `json:"read"`
	SetPercent bool   `json:"setPercent"`
	Reason     string `json:"reason,omitempty"`
}

type usbProfile struct {
	pid            uint16
	channels       byte
	outputTerminal uint16
}

// The four identities and audio layouts come from the same original 1.11.0
// firmware's descriptor builder. Each remains subject to attachment, firmware,
// descriptor and response validation before a volume operation.
func profileForPID(pid uint16) (usbProfile, bool) {
	switch pid {
	case 0x0e36:
		return usbProfile{pid, 2, 0x0402}, true
	case 0x0e37:
		return usbProfile{pid, 2, 0x0301}, true
	case 0x0e38:
		return usbProfile{pid, 1, 0x0402}, true
	case 0x0e39:
		return usbProfile{pid, 1, 0x0301}, true
	default:
		return usbProfile{}, false
	}
}

func KnownModel(pid uint16) bool {
	_, ok := profileForPID(pid)
	return ok
}

func Supported(pid uint16, version string) bool { return KnownModel(pid) && version == Firmware }

func ForDevice(pid uint16, version, connection string) Capabilities {
	c := Capabilities{Connection: connection, Read: "unavailable"}
	if KnownModel(pid) {
		c.Model = "Jabra Evolve2 30 SE"
	}
	if connection != "usb" {
		c.Reason = "Headset volume through a dongle needs a verified command for the connected headset; this route is not available yet."
		return c
	}
	if !KnownModel(pid) {
		c.Reason = "This model does not have a verified headset volume profile yet."
		return c
	}
	if version != Firmware {
		c.Reason = "This model's headset volume profile requires firmware 1.11.0."
		return c
	}
	c.Read = "saved"
	c.SetPercent = true
	return c
}
