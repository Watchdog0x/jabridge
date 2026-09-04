// jabra_api — pure-Go replacement for jabraApi.go (no cgo, no libjabra.so).
//
// Replaces every function in the original jabraApi.go that used CGo bindings
// to libjabra.so with native Go implementations using:
//
//   - /sys/bus/usb/devices   for USB device enumeration (already in firmware.go)
//   - /dev/hidrawN           for GNP protocol queries (reuses HidrawTransport)
//   - /sys/class/power_supply for battery status (kernel HID battery driver)
//   - HTTP                   for firmware metadata (already in firmware.go)
//
// Key improvements over libjabra.so:
//   - Battery reads come from the kernel power_supply subsystem, which is
//     instant and event-driven. libjabra.so's Jabra_GetBatteryStatusV2 uses a
//     GNP round-trip that sometimes returns stale levelInPercent (the exact bug
//     documented in jabraApi.go line 50-53).
//   - Device monitoring uses sysfs polling with inotify-style granularity
//     instead of libudev. No root required.
//   - Firmware version comes from GNP class 0x02, op 0x03 — same as jfwu.
//   - Feature flags come from the Jabra cloud API (device capabilities JSON),
//     not from the device itself. libjabra.so does the same but hides it.
//   - No shared library, no LD_LIBRARY_PATH, no libasound/libcurl deps.

package firmware

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ── Device representation ─────────────────────────────────────────────────

// JabraDevice is the pure-Go equivalent of jabra_DeviceInfo from jabraApi.go.
// Fields map 1:1 to the C Jabra_DeviceInfo struct from Common.h.
type JabraDevice struct {
	DeviceID      uint16
	ProductID     uint16
	VendorID      uint16
	DeviceName    string
	USBDevicePath string // sysfs path (e.g. /sys/devices/.../1-5.1)
	SerialNumber  string
	IsDongle      bool
	DongleName    string
	Variant       string
	IsInFWUpdate  bool
	ConnType      DeviceConnType
	HidrawPath    string // /dev/hidrawN for GNP communication
	PowerSupply   string // /sys/class/power_supply/hid-*-battery path
}

type DeviceConnType int

const (
	ConnUSB  DeviceConnType = 0
	ConnBT   DeviceConnType = 1
	ConnDECT DeviceConnType = 2
)

// ── Battery status (reads from kernel power_supply, not GNP) ──────────

// BatteryStatus matches the Jabra_BatteryStatus struct from Common.h.
// Unlike libjabra.so which queries via GNP (and has the delayed-levelInPercent
// bug), this reads from the kernel's power_supply subsystem which is updated
// by the HID battery driver at interrupt speed.
type BatteryStatus struct {
	LevelInPercent uint8
	Charging       bool
	BatteryLow     bool
	Component      BatteryComponent
}

type BatteryComponent int

const (
	BattUnknown       BatteryComponent = 0
	BattMain          BatteryComponent = 1
	BattCombined      BatteryComponent = 2
	BattRight         BatteryComponent = 3
	BattLeft          BatteryComponent = 4
	BattCradle        BatteryComponent = 5
	BattRemoteControl BatteryComponent = 6
)

// GetBatteryStatus reads battery from the kernel power_supply subsystem.
// The HID battery driver (hid-input.c) creates a power_supply device
// for any HID device that reports usage page 0x85 (Battery System) or
// 0x84 (Power Device). Jabra headsets do. This avoids libjabra.so's
// GNP round-trip and the stale-levelInPercent callback bug.
func GetBatteryStatus(dev *JabraDevice) (*BatteryStatus, error) {
	if dev.IsDongle {
		return nil, fmt.Errorf("dongles do not have batteries")
	}

	psPath := dev.PowerSupply
	if psPath == "" {
		var err error
		psPath, err = findPowerSupply(dev)
		if err != nil {
			return nil, fmt.Errorf("no power_supply for %s: %w", dev.DeviceName, err)
		}
		dev.PowerSupply = psPath
	}

	bs := &BatteryStatus{Component: BattMain}

	// capacity → percentage (0-100)
	if cap, err := readSysfsInt(filepath.Join(psPath, "capacity")); err == nil {
		bs.LevelInPercent = uint8(cap)
	}

	// status → charging
	if status, err := os.ReadFile(filepath.Join(psPath, "status")); err == nil {
		s := strings.TrimSpace(string(status))
		bs.Charging = s == "Charging"
	}

	// Low battery: kernel doesn't expose this directly, so we use a threshold.
	// Jabra devices typically trigger batteryLow at 10% (from libjabra.so RE).
	bs.BatteryLow = bs.LevelInPercent <= 10

	return bs, nil
}

// findPowerSupply searches /sys/class/power_supply for a HID battery
// that matches the device's USB path. The kernel names these
// "hid-<bus>:<vid>:<pid>.<iface>-battery".
func findPowerSupply(dev *JabraDevice) (string, error) {
	root := "/sys/class/power_supply"
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}

	pidHex := fmt.Sprintf("%04X", dev.ProductID)
	vidHex := fmt.Sprintf("%04X", dev.VendorID)
	target := fmt.Sprintf("%s:%s", vidHex, pidHex)

	for _, entry := range entries {
		name := entry.Name()
		if strings.Contains(strings.ToUpper(name), target) {
			return filepath.Join(root, name), nil
		}
	}

	// Fallback: search by serial in uevent files
	for _, entry := range entries {
		p := filepath.Join(root, entry.Name())
		if uevent, err := os.ReadFile(filepath.Join(p, "uevent")); err == nil {
			if strings.Contains(string(uevent), dev.SerialNumber) {
				return p, nil
			}
		}
	}

	return "", fmt.Errorf("no matching power_supply for VID:PID %s", target)
}

func readSysfsInt(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// ── Firmware version via GNP (class=0x02, op=0x03) ───────────────────

// GetFirmwareVersion sends a GNP query to the device and returns the
// firmware version string (e.g. "1.5.7"). Uses the same class/opcode
// as jfwu — confirmed from the live capture:
//
//	TX: 05 08 00 03 46 02 03
//	RX: 00 08 03 CC 02 03 05 31 2E 33 2E 38  → "1.3.8"
//
// Response format: byte[6] = string length, byte[7..] = ASCII chars.
func GetFirmwareVersion(dev *JabraDevice) (string, error) {
	tr, err := OpenHidraw(dev.HidrawPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = tr.Close() }()

	source := GnpSrcHost
	if dev.IsDongle {
		source = 0x01
	}
	return queryFirmwareVersion(tr, source, 0x01)
}

func queryFirmwareVersion(tr OtaTransport, source, seq byte) (string, error) {
	query := buildInitQuery(source, seq, 0x02, 0x03)
	if err := tr.Write(query); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	resp, err := tr.Read(5 * time.Second)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}

	// Strip report ID if present
	if len(resp) > 0 && resp[0] == GnpReportID {
		resp = resp[1:]
	}
	// Validate response: byte[0]=0x00 (dst), byte[3]=0xC0|len (response),
	// byte[4]=0x02 (class), byte[5]=0x03 (op)
	if len(resp) < 8 || resp[0] != 0x00 {
		return "", fmt.Errorf("invalid response: %x", resp[:min(10, len(resp))])
	}
	if resp[3]&0xC0 != 0xC0 {
		return "", fmt.Errorf("not a response packet: flags=0x%02x", resp[3])
	}
	if resp[4] == 0xFE {
		return "", fmt.Errorf("device returned error: 0x%02x", resp[5])
	}
	// byte[6] = string length, byte[7..] = ASCII
	strLen := int(resp[6])
	if len(resp) < 7+strLen {
		return "", fmt.Errorf("response too short for %d-byte string", strLen)
	}
	return string(resp[7 : 7+strLen]), nil
}

// ── Device info via GNP (class=0x02, op=0x02) ────────────────────────

// DeviceInfo holds the GNP protocol version and variant code.
type DeviceGNPInfo struct {
	ProtocolVersion byte
	VariantCode     uint16
}

// GetDeviceGNPInfo sends a GNP GetDeviceInfo query.
// Capture: TX 05 08 00 00 46 02 02 → RX 00 08 00 C9 02 02 02 01 67
// Response: byte[6]=protocol, byte[7..8]=variant (LE u16).
func GetDeviceGNPInfo(dev *JabraDevice) (*DeviceGNPInfo, error) {
	tr, err := OpenHidraw(dev.HidrawPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tr.Close() }()

	seq := byte(0x00)
	query := buildInitQuery(GnpSrcHost, seq, 0x02, 0x02)
	if err := tr.Write(query); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	resp, err := tr.Read(5 * time.Second)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	if len(resp) > 0 && resp[0] == GnpReportID {
		resp = resp[1:]
	}
	if len(resp) < 9 || resp[0] != 0x00 {
		return nil, fmt.Errorf("invalid response")
	}
	if resp[4] == 0xFE {
		return nil, fmt.Errorf("GNP error: 0x%02x", resp[5])
	}

	return &DeviceGNPInfo{
		ProtocolVersion: resp[6],
		// Variant is big-endian in the GNP response: 01 67 = 0x0167
		VariantCode: binary.BigEndian.Uint16(resp[7:9]),
	}, nil
}

// ── Language ID via GNP (class=0x13, op=0x08) ─────────────────────────

// GetLanguageID returns the device's active language as a Windows LANGID.
// Capture: TX 05 08 00 04 46 13 08 → RX 00 08 04 C8 13 08 09 04
// 0x0409 = English (US).
func GetLanguageID(dev *JabraDevice) (uint16, error) {
	tr, err := OpenHidraw(dev.HidrawPath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tr.Close() }()

	seq := byte(0x02)
	query := buildInitQuery(GnpSrcHost, seq, 0x13, 0x08)
	if err := tr.Write(query); err != nil {
		return 0, fmt.Errorf("write: %w", err)
	}

	resp, err := tr.Read(5 * time.Second)
	if err != nil {
		return 0, fmt.Errorf("read: %w", err)
	}

	if len(resp) > 0 && resp[0] == GnpReportID {
		resp = resp[1:]
	}
	if len(resp) < 8 || resp[0] != 0x00 {
		return 0, fmt.Errorf("invalid response")
	}
	if resp[4] == 0xFE {
		return 0, fmt.Errorf("GNP error (not supported): 0x%02x", resp[5])
	}

	return binary.LittleEndian.Uint16(resp[6:8]), nil
}

// ── Feature flags (from Jabra cloud API, same as libjabra.so) ─────────

// DeviceFeature matches the _DeviceFeature enum from Common.h (1000-1051).
type DeviceFeature uint32

const (
	FeatureBusyLight                   DeviceFeature = 1000
	FeatureFactoryReset                DeviceFeature = 1001
	FeaturePairingList                 DeviceFeature = 1002
	FeatureRemoteMMI                   DeviceFeature = 1003
	FeatureMusicEqualizer              DeviceFeature = 1004
	FeatureEarbudInterconnectionStatus DeviceFeature = 1005
	FeatureStepRate                    DeviceFeature = 1006
	FeatureHeartRate                   DeviceFeature = 1007
	FeatureRRInterval                  DeviceFeature = 1008
	FeatureRingtoneUpload              DeviceFeature = 1009
	FeatureImageUpload                 DeviceFeature = 1010
	FeatureNeedsExplicitRebootAfterOta DeviceFeature = 1011
	FeatureNeedsCradleToCompleteFwu    DeviceFeature = 1012
	FeatureRemoteMMIv2                 DeviceFeature = 1013
	FeatureLogging                     DeviceFeature = 1014
	FeaturePreferredSoftphoneList      DeviceFeature = 1015
	FeatureVoiceAssistant              DeviceFeature = 1016
	FeaturePlayRingtone                DeviceFeature = 1017
	FeatureSetDateTime                 DeviceFeature = 1018
	FeatureFullWizardMode              DeviceFeature = 1019
	FeatureLimitedWizardMode           DeviceFeature = 1020
	FeatureOnHeadDetection             DeviceFeature = 1021
	FeatureSettingsChangeNotification  DeviceFeature = 1022
	FeatureAudioStreaming              DeviceFeature = 1023
	FeatureCustomerSupport             DeviceFeature = 1024
	FeatureMySound                     DeviceFeature = 1025
	FeatureUIConfigurableButtons       DeviceFeature = 1026
	FeatureManualBusyLight             DeviceFeature = 1027
	FeatureWhiteboard                  DeviceFeature = 1028
	FeatureVideo                       DeviceFeature = 1029
	FeatureAmbienceModes               DeviceFeature = 1030
	FeatureSealingTest                 DeviceFeature = 1031
	FeatureAMASupport                  DeviceFeature = 1032
	FeatureAmbienceModesLoop           DeviceFeature = 1033
	FeatureFFANC                       DeviceFeature = 1034
	FeatureGoogleBisto                 DeviceFeature = 1035
	FeatureVirtualDirector             DeviceFeature = 1036
	FeaturePictureInPicture            DeviceFeature = 1037
	FeatureDateTimeIsUTC               DeviceFeature = 1038
	FeatureRemoteControl               DeviceFeature = 1039
	FeatureUserConfigurableHDR         DeviceFeature = 1040
	FeatureDECTBasicPairing            DeviceFeature = 1041
	FeatureDECTSecurePairing           DeviceFeature = 1042
	FeatureDECTOTAFWU                  DeviceFeature = 1043
	FeatureXpressURL                   DeviceFeature = 1044
	FeaturePasswordProvisioning        DeviceFeature = 1045
	FeatureEthernet                    DeviceFeature = 1046
	FeatureWLAN                        DeviceFeature = 1047
	FeatureEthernetAuthCert            DeviceFeature = 1048
	FeatureEthernetAuthMSCHAPv2        DeviceFeature = 1049
	FeatureWLANAuthCert                DeviceFeature = 1050
	FeatureWLANAuthMSCHAPv2            DeviceFeature = 1051
)

// ── Hidraw discovery ──────────────────────────────────────────────────

// findHidrawForDevice locates the /dev/hidrawN node that carries GNP
// traffic for a Jabra USB device. It will not fall back to an arbitrary HID
// interface: the descriptor must declare GNP output report ID 0x05.
func findHidrawForDevice(dev *JabraDevice) (string, error) {
	entries, err := os.ReadDir("/sys/class/hidraw")
	if err != nil {
		return "", err
	}

	var candidates []string
	for _, entry := range entries {
		hidrawSys := filepath.Join("/sys/class/hidraw", entry.Name())
		// Follow the symlink to resolve the real device path
		deviceLink := filepath.Join(hidrawSys, "device")
		resolved, err := filepath.EvalSymlinks(deviceLink)
		if err != nil {
			continue
		}
		// Check if this hidraw belongs to our USB device by matching VID:PID
		// in the ancestor path. The uevent file has HID_ID with bus:vid:pid.
		ueventPath := filepath.Join(resolved, "uevent")
		uevent, err := os.ReadFile(ueventPath)
		if err != nil {
			continue
		}
		if hidUeventMatches(uevent, dev.VendorID, dev.ProductID) {
			candidates = append(candidates, filepath.Join("/dev", entry.Name()))
		}
	}
	if path, found := firstGnpHidraw(candidates, HasGnpOutputReport); found {
		return path, nil
	}
	return "", fmt.Errorf("no GNP hidraw interface for VID:PID %04X:%04X", dev.VendorID, dev.ProductID)
}

func firstGnpHidraw(paths []string, supportsGNP func(string) bool) (string, bool) {
	for _, path := range paths {
		if supportsGNP(path) {
			return path, true
		}
	}
	return "", false
}

func hidUeventMatches(data []byte, vid, pid uint16) bool {
	for _, line := range strings.Split(string(data), "\n") {
		value, found := strings.CutPrefix(strings.TrimSpace(line), "HID_ID=")
		if !found {
			continue
		}
		parts := strings.Split(value, ":")
		if len(parts) != 3 {
			return false
		}
		gotVID, vidErr := strconv.ParseUint(parts[1], 16, 32)
		gotPID, pidErr := strconv.ParseUint(parts[2], 16, 32)
		return vidErr == nil && pidErr == nil && uint16(gotVID) == vid && uint16(gotPID) == pid
	}
	return false
}

// isDonglePID returns true for wireless Link adapters in the current public
// model catalog. Historical IDs are retained so older devices remain visible.
func isDonglePID(pid uint16) bool {
	switch pid {
	case 0xa345, 0xa346,
		0x245d, 0x245e, 0x24ae,
		0x24c7, 0x24c8, 0x24c9, 0x24ca, 0x24e9,
		0x2e50, 0x2e51, 0x2e56, 0x2e57,
		0x1131, 0x1132, 0x1133, 0x1134, 0x1135, 0x1136,
		0x0a17, 0x2483, 0x2484:
		return true
	}
	return false
}
