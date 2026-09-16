package firmware

// The USB DFU engine is shared. Only documented runtime/DFU counterparts
// select it; other model families keep their own updater and packet format.
type usbDFUProfile struct {
	Name        string
	RuntimePIDs []uint16
	DFUPID      uint16
}

// Every runtime/bootloader pair is backed by that exact runtime variant's
// published archive, including mono/stereo and UC/Teams differences. Report
// IDs and lengths come from the live HID descriptor, never from this table.
var usbDFUProfiles = []usbDFUProfile{
	{Name: "Jabra Speak 410", RuntimePIDs: []uint16{0x0410, 0x0412}, DFUPID: 0x0411},
	{Name: "Jabra Speak 510", RuntimePIDs: []uint16{0x0420, 0x0422}, DFUPID: 0x0421},
	{Name: "Jabra Speak 710", RuntimePIDs: []uint16{0x2475, 0x2476, 0x2477}, DFUPID: 0x0982},
	{Name: "Jabra Speak 810", RuntimePIDs: []uint16{0x2454, 0x2456}, DFUPID: 0x0971},
	{Name: "Jabra Evolve 75e", RuntimePIDs: []uint16{0x246c, 0x246d, 0x246e}, DFUPID: 0x097e},
	{Name: "Jabra Link 280", RuntimePIDs: []uint16{0x0910}, DFUPID: 0x090f},
	{Name: "Jabra Biz 2400", RuntimePIDs: []uint16{0x090a, 0x091c}, DFUPID: 0x091b},
	{Name: "Jabra Biz 2400", RuntimePIDs: []uint16{0x2400, 0x2401}, DFUPID: 0x2402},
	{Name: "Jabra Speak 450", RuntimePIDs: []uint16{0x0949}, DFUPID: 0x0948},
	{Name: "Jabra Evolve 65 Stereo", RuntimePIDs: []uint16{0x030b, 0x030c, 0x0311}, DFUPID: 0x094e},
	{Name: "Jabra Evolve 65 Mono", RuntimePIDs: []uint16{0x030d, 0x030e, 0x0310}, DFUPID: 0x0954},
	{Name: "Jabra Biz 2400 II USB BT Mono", RuntimePIDs: []uint16{0x2450, 0x2451}, DFUPID: 0x096a},
	{Name: "Jabra Biz 2400 II USB BT Stereo", RuntimePIDs: []uint16{0x2452, 0x2453}, DFUPID: 0x096c},
	{Name: "Jabra Link 370", RuntimePIDs: []uint16{0x245d, 0x245e}, DFUPID: 0x0976},
	{Name: "Jabra Evolve 75", RuntimePIDs: []uint16{0x2465, 0x2466, 0x2467}, DFUPID: 0x097a},
	{Name: "Jabra Link 370 Teams", RuntimePIDs: []uint16{0x24ae}, DFUPID: 0x0994},
	{Name: "Jabra Speak 750 MS", RuntimePIDs: []uint16{0x24b0, 0x24b2}, DFUPID: 0x0995},
	{Name: "Jabra Speak 750 UC", RuntimePIDs: []uint16{0x24b1, 0x24cb}, DFUPID: 0x0997},
	{Name: "Jabra Connect 4s", RuntimePIDs: []uint16{0x24e8}, DFUPID: 0x0998},
	{Name: "Jabra Link 360", RuntimePIDs: []uint16{0xa345, 0xa346}, DFUPID: 0xa347},
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
	if protocol == 10 || protocol == 13 {
		return panacast50ModePID(pid)
	}
	if protocol == 11 {
		return uvcCameraPID(pid)
	}
	if protocol == 18 {
		_, known := bulkCameraProfileForPID(pid)
		return known
	}
	if protocol == 12 {
		return sitelOTAChildPID(pid)
	}
	if protocol == 5 {
		return conexantPID(pid)
	}
	if protocol == 4 {
		_, known := sitelProfileForPID(pid)
		_, dect := sitelDECTProfileForPID(pid)
		return known || dect
	}
	if protocol == 7 || protocol == 16 || protocol == 17 {
		return true
	}
	_, known := usbDFUProfileForPID(pid)
	return protocol == 1 && known
}
