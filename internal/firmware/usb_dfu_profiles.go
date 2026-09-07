package firmware

// The USB DFU engine is shared. Only documented runtime/DFU counterparts
// select it; other model families keep their own updater and packet format.
type usbDFUProfile struct {
	Name        string
	RuntimePIDs []uint16
	DFUPID      uint16
	ReportID    byte
}

var usbDFUProfiles = []usbDFUProfile{
	{Name: "Jabra Speak 410", RuntimePIDs: []uint16{0x0412}, DFUPID: 0x0411, ReportID: 2},
	{Name: "Jabra Speak 510", RuntimePIDs: []uint16{0x0420, 0x0422}, DFUPID: 0x0421, ReportID: 2},
	{Name: "Jabra Speak 710", RuntimePIDs: []uint16{0x2475}, DFUPID: 0x0982, ReportID: 5},
	{Name: "Jabra Speak 810", RuntimePIDs: []uint16{0x2456}, DFUPID: 0x0971, ReportID: 5},
}

func (p usbDFUProfile) runtime(pid uint16) bool { return containsPID(p.RuntimePIDs, pid) }

func usbDFUProfileForPID(pid uint16) (usbDFUProfile, bool) {
	for _, profile := range usbDFUProfiles {
		if profile.runtime(pid) || profile.DFUPID == pid {
			return profile, true
		}
	}
	return usbDFUProfile{}, false
}

// This says an implementation exists, not that every model/archive or actual
// flash is qualified. Payload validation and hardware tests remain separate.
func NativeFirmwareProtocolSupported(pid uint16, protocol int) bool {
	if protocol == 7 {
		return true
	}
	_, known := usbDFUProfileForPID(pid)
	return protocol == 1 && known
}
