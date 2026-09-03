// jabraApi — pure-Go Jabra device API (no cgo, no libjabra.so).
//
// Replaces the original CGo-based jabraApi.go with native Go
// implementations using:
//   - /sys/bus/usb/devices       for USB device enumeration
//   - /dev/hidrawN               for GNP protocol queries/commands
//   - /sys/class/power_supply    for battery status
//   - HTTP to sdkbackend.jabra.com for firmware metadata
//
// All types and function signatures are preserved so tui.go and
// jabraCodes.go compile unchanged.

package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ── Types (unchanged from original) ──────────────────────────────────

type jabra_DeviceInfo struct {
	deviceID               uint16
	productID              uint16
	vendorID               uint16
	deviceName             string
	usbDevicePath          string
	parentInstanceId       string
	errStatus              errorStatusCode
	isDongle               bool
	dongleName             string
	variant                string
	serialNumber           string
	isInFirmwareUpdateMode bool
	deviceConnection       deviceConnectionType
	connectionID           uint32
	parentDeviceID         uint16
	deviceEventsMask       uint32
	featureFlags           *featureFlags
	batteryStatus          *batteryStatus
	pairingList            *pairingList
	// pure-Go additions
	hidrawPath  string // /dev/hidrawN for GNP
	powerSupply string // /sys/class/power_supply/... path
}

type batteryComponent int

const (
	unknown batteryComponent = iota
	headband
	combinde
	right
	left
	cradleBattery
	remoteControl
)

type batteryStatusUnit struct {
	levelInPercent uint8
	component      batteryComponent
}

type batteryStatus struct {
	levelInPercent  uint8
	charging        bool
	batteryLow      bool
	component       batteryComponent
	extraUnitsCount uint32
	extraUnits      []batteryStatusUnit
}

type deviceListType int

const (
	searchResult deviceListType = iota
	pairedDevices
	searchComplete
)

type pairedDevice struct {
	deviceName   string
	deviceBTAddr [6]byte
	isConnected  bool
}

type pairingList struct {
	count         uint16
	listType      deviceListType
	pairedDevices []pairedDevice
}

type secureConnectionMode int

const (
	legacyMode secureConnectionMode = iota
	secureMode
	restrictedMode
)

type featureFlags struct {
	busyLight                          bool
	factoryReset                       bool
	pairingList                        bool
	remoteMMI                          bool
	musicEqualizer                     bool
	earbudInterconnectionStatus        bool
	stepRate                           bool
	heartRate                          bool
	rrInterval                         bool
	ringtoneUpload                     bool
	imageUpload                        bool
	needsExplicitRebootAfterOta        bool
	needsToBePutIncCradleToCompleteFwu bool
	remoteMMIv2                        bool
	logging                            bool
	preferredSoftphoneListInDevice     bool
	voiceAssistant                     bool
	playRingtone                       bool
	setDateTime                        bool
	fullWizardMode                     bool
	limitedWizardMode                  bool
	onHeadDetection                    bool
	settingsChangeNotification         bool
	audioStreaming                     bool
	customerSupport                    bool
	mySound                            bool
	uiConfigurableButtons              bool
	manualBusyLight                    bool
	whiteboard                         bool
	video                              bool
	ambienceModes                      bool
	sealingTest                        bool
	amasupport                         bool
	ambienceModesLoop                  bool
	ffanc                              bool
	googleBisto                        bool
	virtualDirector                    bool
	pictureInPicture                   bool
	dateTimeIsUTC                      bool
	remoteControl                      bool
	userConfigurableHdr                bool
	dectBasicPairing                   bool
	dectSecurePairing                  bool
	dectOtaFwuSupported                bool
	xpressURL                          bool
	passwordProvisioning               bool
	ethernet                           bool
	wlan                               bool
	ethernetAuthenticationCertificate  bool
	ethernetAuthenticationMschapv2     bool
	wlanAuthenticationCertificate      bool
	wlanAuthenticationMschapv2         bool
}

type hidInput int

const (
	undefined hidInput = iota
	offHook
	mute
	flash
	redial
	key0
	key1
	key2
	key3
	key4
	key5
	key6
	key7
	key8
	key9
	keyStar
	keyPound
	keyClear
	online
	speedDial
	voiceMail
	lineBusy
	rejectCall
	outOfRange
	pseudoOffHook
	button1
	button2
	button3
	volumeUp
	volumeDown
	fireAlarm
	jackConnection
	qdConnection
	headsetConnection
)

type devices map[int]*jabra_DeviceInfo
type deviceConnectionType int

const (
	deviceConnectionType_USB deviceConnectionType = iota
	deviceConnectionType_BT
	deviceConnectionType_DECT
)

type menuItem struct {
	id    int
	label string
}

var (
	deviceManager   devices
	selectedHeadset int = -1
	selectedDongle  int = -1

	startMenu          = []menuItem{}
	dongleSettignsMenu = []menuItem{}

	searchDeviceList *pairingList = &pairingList{
		count:         0,
		listType:      searchResult,
		pairedDevices: make([]pairedDevice, 0),
	}

	stopUpdateBattery     = make(chan struct{})
	stopUpdatePairingList = make(chan struct{})
	firstScanComplete     atomic.Bool
)

// ── GNP protocol constants (from RE of jfwu + libjabra.so) ───────────

const (
	gnpReportID  byte = 0x05
	gnpReportLen int  = 63
	gnpSrcHost   byte = 0x08
	gnpSrcDongle byte = 0x01 // dongle responds to src=0x01 (from live probe)
	gnpFlagQuery byte = 0x40
	gnpFlagCmd   byte = 0x80

	// GNP classes discovered from live probe (2026-04-12)
	gnpClassDevInfo byte = 0x02 // device name, serial, fw version, etc.
	gnpClassStatus  byte = 0x04 // battery, features, capabilities

	// GNP opcodes — class 0x02 (DeviceInfo)
	gnpOpDeviceName     byte = 0x00 // → len-prefixed UTF-8
	gnpOpSerialNumber   byte = 0x01 // → len-prefixed ASCII
	gnpOpDeviceInfo     byte = 0x02 // → proto(1B) + variant(2B BE)
	gnpOpFirmwareVer    byte = 0x03 // → len-prefixed ASCII
	gnpOpVersionEncoded byte = 0x0e // → 6 bytes (padding + major.minor.patch)

	// GNP opcodes — class 0x04 (Status/Capabilities)
	gnpOpBattery     byte = 0x01 // → 4 bytes [rssi, flags, component, level%]
	gnpOpBusylight   byte = 0x22 // GET: query returns 0/1; SET: command with payload [0x01=on, 0x00=off]
	gnpOpFeatureList byte = 0x2d // → 12 bytes capability bitmap

	// Current Jabra Device Pairing SDK command surface, recovered from the
	// official managed package and kept separate from older device-specific
	// status classes.
	gnpClassPairingDevice byte = 0x0d
	gnpClassConfig        byte = 0x13
	gnpOpDisconnectAll    byte = 0x05
	gnpOpSearchEnable     byte = 0x20
	gnpOpSearchDisable    byte = 0x21
	gnpOpBluetoothConnect byte = 0x24
	gnpOpDeviceStatus     byte = 0x26
	gnpOpGetDBRecord      byte = 0x28
	gnpOpGetDBName        byte = 0x32
	gnpOpBluetoothPair    byte = 0x30
	gnpOpAutoPairing      byte = 0x40

	// Device writes stay disabled until the command is validated on supported
	// hardware. Read-only enumeration and metadata do not require this switch.
	experimentalWritesEnv = "JABRIDGE_ENABLE_EXPERIMENTAL_WRITES"
	experimentalWritesAck = "I_ACCEPT_THE_BRICK_RISK"
)

func experimentalDeviceWritesEnabled() bool {
	return os.Getenv(experimentalWritesEnv) == experimentalWritesAck
}

// ── Hidraw transport (minimal, for GNP queries) ──────────────────────

type hidrawConn struct {
	f    *os.File
	path string
}

func openHidraw(path string) (*hidrawConn, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &hidrawConn{f: f, path: path}, nil
}

func (h *hidrawConn) close() {
	if h.f != nil {
		h.f.Close()
		h.f = nil
	}
}

func (h *hidrawConn) write(report []byte) error {
	n, err := h.f.Write(report)
	if err != nil {
		return err
	}
	if n < len(report)-1 {
		return fmt.Errorf("hidraw short write: %d/%d", n, len(report))
	}
	return nil
}

func (h *hidrawConn) read(timeout time.Duration) ([]byte, error) {
	fd := int(h.f.Fd())
	syscall.SetNonblock(fd, true)
	defer syscall.SetNonblock(fd, false)

	deadline := time.Now().Add(timeout)
	buf := make([]byte, 256)
	for {
		n, err := syscall.Read(fd, buf)
		if err == nil && n > 0 {
			if buf[0] != gnpReportID {
				continue // skip non-GNP reports
			}
			return append([]byte(nil), buf[:n]...), nil
		}
		if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("hidraw read timeout")
			}
			runtime.Gosched()
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("hidraw read: %w", err)
		}
	}
}

func buildGNPReport(destination, seq, packetType, class, op byte, payload []byte) ([]byte, error) {
	packetLength := 6 + len(payload)
	if packetLength > 0x3f || packetLength+1 > gnpReportLen {
		return nil, fmt.Errorf("GNP payload too large: %d bytes", len(payload))
	}
	buf := make([]byte, gnpReportLen)
	buf[0] = gnpReportID
	buf[1] = destination
	buf[2] = 0x00
	buf[3] = seq
	buf[4] = packetType | byte(packetLength)
	buf[5] = class
	buf[6] = op
	copy(buf[7:], payload)
	return buf, nil
}

// gnpQuery sends a GNP query and returns the response packet.
func gnpQuery(h *hidrawConn, destination, seq, class, op byte) ([]byte, error) {
	return gnpQueryWithPayload(h, destination, seq, class, op, nil)
}

func gnpQueryWithPayload(h *hidrawConn, destination, seq, class, op byte, payload []byte) ([]byte, error) {
	buf, err := buildGNPReport(destination, seq, gnpFlagQuery, class, op, payload)
	if err != nil {
		return nil, err
	}
	if err := h.write(buf); err != nil {
		return nil, err
	}
	resp, err := h.read(5 * time.Second)
	if err != nil {
		return nil, err
	}
	// Strip report ID
	if len(resp) > 0 && resp[0] == gnpReportID {
		resp = resp[1:]
	}
	return resp, nil
}

func parseGNPReplyPayload(resp []byte, seq, class, op byte) ([]byte, error) {
	if len(resp) > 0 && resp[0] == gnpReportID {
		resp = resp[1:]
	}
	if len(resp) < 5 {
		return nil, fmt.Errorf("response too short: %d bytes", len(resp))
	}
	if resp[4] == 0xFE {
		errCode := byte(0)
		if len(resp) > 5 {
			errCode = resp[5]
		}
		return nil, fmt.Errorf("GNP NAK: 0x%02x", errCode)
	}
	if len(resp) < 6 {
		return nil, fmt.Errorf("response header too short: %d bytes", len(resp))
	}
	if resp[2] != seq {
		return nil, fmt.Errorf("GNP reply sequence mismatch: got 0x%02x, want 0x%02x", resp[2], seq)
	}
	if resp[3]&0xc0 != 0xc0 {
		return nil, fmt.Errorf("GNP packet is not a reply: 0x%02x", resp[3])
	}
	if resp[4] != class || resp[5] != op {
		return nil, fmt.Errorf("GNP reply command mismatch: got 0x%02x/0x%02x, want 0x%02x/0x%02x", resp[4], resp[5], class, op)
	}
	length := int(resp[3] & 0x3f)
	if length < 6 || length > len(resp) {
		return nil, fmt.Errorf("invalid GNP reply length: %d", length)
	}
	return append([]byte(nil), resp[6:length]...), nil
}

// gnpQueryPayload sends a query and returns just the data payload
// (stripping the header: dst, src, seq, flags, class, op).
func gnpQueryPayload(h *hidrawConn, src, seq, class, op byte) ([]byte, error) {
	return gnpQueryPayloadWithData(h, src, seq, class, op, nil)
}

func gnpQueryPayloadWithData(h *hidrawConn, destination, seq, class, op byte, requestData []byte) ([]byte, error) {
	resp, err := gnpQueryWithPayload(h, destination, seq, class, op, requestData)
	if err != nil {
		return nil, err
	}
	return parseGNPReplyPayload(resp, seq, class, op)
}

// gnpCommand sends a GNP command (flag 0x80) and waits for ACK.
func gnpCommand(h *hidrawConn, src, seq, class, op byte, payload []byte) error {
	buf, err := buildGNPReport(src, seq, gnpFlagCmd, class, op, payload)
	if err != nil {
		return err
	}
	if err := h.write(buf); err != nil {
		return err
	}
	// Wait for ACK (byte[3]=0xca, byte[4]=0xff)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := h.read(time.Until(deadline))
		if err != nil {
			return fmt.Errorf("ACK: %w", err)
		}
		if len(resp) > 0 && resp[0] == gnpReportID {
			resp = resp[1:]
		}
		if len(resp) >= 5 && resp[3] == 0xca && resp[4] == 0xff {
			return nil // ACK received
		}
		// Check for NAK
		if len(resp) >= 5 && (resp[3]&0xc0) == 0xc0 && resp[4] == 0xFE {
			errCode := byte(0)
			if len(resp) > 5 {
				errCode = resp[5]
			}
			return fmt.Errorf("GNP NAK: 0x%02x", errCode)
		}
		// Keep reading (might get events before ACK)
	}
	return fmt.Errorf("ACK timeout")
}

// openDeviceHidraw opens a GNP hidraw connection for the given device.
// Returns nil if the device doesn't have a hidraw path.
func openDeviceHidraw(dev *jabra_DeviceInfo) *hidrawConn {
	if dev == nil || dev.hidrawPath == "" {
		return nil
	}
	h, err := openHidraw(dev.hidrawPath)
	if err != nil {
		return nil
	}
	return h
}

// gnpSeqCounter is a monotonic sequence counter for GNP packets.
var (
	gnpSeqMu      sync.Mutex
	gnpSeqCounter byte = 0x20
)

func nextSeq() byte {
	gnpSeqMu.Lock()
	defer gnpSeqMu.Unlock()
	s := gnpSeqCounter
	gnpSeqCounter++
	return s
}

// ── USB enumeration via sysfs ────────────────────────────────────────

const jabraVendorID uint16 = 0x0b0e

type usbDev struct {
	sysPath      string
	vendorID     uint16
	productID    uint16
	manufacturer string
	product      string
	serial       string
}

func enumerateJabraUSB() ([]usbDev, error) {
	root := "/sys/bus/usb/devices"
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var devs []usbDev
	for _, entry := range entries {
		dir := filepath.Join(root, entry.Name())
		vid, err := readHexFile(filepath.Join(dir, "idVendor"))
		if err != nil || vid != jabraVendorID {
			continue
		}
		pid, _ := readHexFile(filepath.Join(dir, "idProduct"))
		devs = append(devs, usbDev{
			sysPath:      dir,
			vendorID:     vid,
			productID:    pid,
			manufacturer: readTextFile(filepath.Join(dir, "manufacturer")),
			product:      readTextFile(filepath.Join(dir, "product")),
			serial:       readTextFile(filepath.Join(dir, "serial")),
		})
	}
	return devs, nil
}

func readHexFile(path string) (uint16, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 16, 16)
	return uint16(n), err
}

func readTextFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ── Hidraw discovery ─────────────────────────────────────────────────

func findHidrawForPID(vid, pid uint16) string {
	entries, _ := os.ReadDir("/sys/class/hidraw")
	for _, entry := range entries {
		ueventPath := filepath.Join("/sys/class/hidraw", entry.Name(), "device", "uevent")
		data, err := os.ReadFile(ueventPath)
		if err != nil {
			continue
		}
		if hidUeventMatches(data, vid, pid) {
			return filepath.Join("/dev", entry.Name())
		}
	}
	return ""
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

// ── Power supply discovery ───────────────────────────────────────────

func findPowerSupplyPath(vid, pid uint16, serial string) string {
	entries, _ := os.ReadDir("/sys/class/power_supply")
	vidPid := fmt.Sprintf("%04X:%04X", vid, pid)
	for _, entry := range entries {
		p := filepath.Join("/sys/class/power_supply", entry.Name())
		if strings.Contains(strings.ToUpper(entry.Name()), vidPid) {
			return p
		}
		uevent, err := os.ReadFile(filepath.Join(p, "uevent"))
		if err == nil && serial != "" && strings.Contains(string(uevent), serial) {
			return p
		}
	}
	return ""
}

// ── Dongle detection ─────────────────────────────────────────────────

func isKnownDonglePID(pid uint16) bool {
	switch pid {
	case 0x24c7, 0x24c8, 0x0a17, 0x2483, 0x2484:
		return true
	}
	return false
}

func supportsValidatedPairingReads(pid uint16) bool {
	return pid == 0x24c7 || pid == 0x24c8
}

func isAccessoryName(name string) bool {
	lower := strings.ToLower(name)
	for _, marker := range []string{"deskstand", "desk stand", "charger", "cradle", "busy light", "busylight"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func deviceForID(deviceID uint16) *jabra_DeviceInfo {
	for _, dev := range deviceManager {
		if dev.deviceID == deviceID {
			return dev
		}
	}
	return nil
}

func decodeLengthPrefixedString(payload []byte) (string, bool) {
	if len(payload) == 0 {
		return "", false
	}
	length := int(payload[0])
	if length == 0 || length > len(payload)-1 {
		return "", false
	}
	return string(payload[1 : 1+length]), true
}

// ── Device scan + attach (replaces Jabra_InitializeV2 callbacks) ─────

func scanAndAttachDevices() {
	usbDevs, err := enumerateJabraUSB()
	if err != nil {
		return
	}
	for _, ud := range usbDevs {
		if isAccessoryName(ud.product) {
			continue
		}
		// Skip if already known
		alreadyKnown := false
		for _, existing := range deviceManager {
			if existing.serialNumber == ud.serial && existing.productID == ud.productID {
				alreadyKnown = true
				break
			}
		}
		if alreadyKnown {
			continue
		}

		dev := &jabra_DeviceInfo{
			productID:        ud.productID,
			vendorID:         ud.vendorID,
			deviceName:       ud.product,
			usbDevicePath:    ud.sysPath,
			serialNumber:     ud.serial,
			isDongle:         isKnownDonglePID(ud.productID),
			deviceConnection: deviceConnectionType_USB,
			hidrawPath:       findHidrawForPID(ud.vendorID, ud.productID),
			powerSupply:      findPowerSupplyPath(ud.vendorID, ud.productID, ud.serial),
		}

		// Enrich device info via GNP queries
		if dev.hidrawPath != "" {
			if h := openDeviceHidraw(dev); h != nil {
				src := gnpSrcHost
				if dev.isDongle {
					src = gnpSrcDongle
				}
				// Query device name if sysfs didn't provide one
				if dev.deviceName == "" {
					if payload, err := gnpQueryPayload(h, src, nextSeq(), gnpClassDevInfo, gnpOpDeviceName); err == nil {
						if name, ok := decodeLengthPrefixedString(payload); ok {
							dev.deviceName = name
						}
					}
				}
				// Query serial number if missing
				if dev.serialNumber == "" {
					if payload, err := gnpQueryPayload(h, src, nextSeq(), gnpClassDevInfo, gnpOpSerialNumber); err == nil {
						if serial, ok := decodeLengthPrefixedString(payload); ok {
							dev.serialNumber = serial
						}
					}
				}
				h.close()
			}
		}

		// Start from known-safe state. Capability and pairing commands are not
		// guessed when no validated response exists.
		dev.featureFlags = &featureFlags{}
		if dev.isDongle {
			dev.pairingList = &pairingList{listType: searchComplete, pairedDevices: []pairedDevice{}}
		} else {
			dev.batteryStatus = &batteryStatus{}
		}

		if isNewDevice := serialNumberCheck(dev); isNewDevice {
			deviceManager.add(dev)
			if dev.isDongle && supportsValidatedPairingReads(dev.productID) {
				dev.pairingList = getPairingList(dev.deviceID)
				dev.featureFlags.pairingList = true
				updateStartMenu()
			} else if !dev.isDongle {
				if battery, err := getBatteryStatus(dev.deviceID); err == nil {
					dev.batteryStatus = battery
				}
			}
		}
	}
}

// pollDevices runs the device scan loop (replaces udev monitoring).
func pollDevices(stop <-chan struct{}) {
	// Initial scan
	scanAndAttachDevices()
	firstScanComplete.Store(true)
	requestUIRedraw()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			scanAndAttachDevices()
		}
	}
}

/****************************************************************************/
/*                           GENERAL UTILITIES                              */
/****************************************************************************/

func updateDongleSettignsMenu() {
	dongleSettignsMenu = []menuItem{}
	if dongle, exists := deviceManager[selectedDongle]; exists {
		dongleSettignsMenu = append(dongleSettignsMenu,
			menuItem{id: -1, label: fmt.Sprintf("Device:             %s", dongle.deviceName)},
			menuItem{id: -1, label: fmt.Sprintf("USB ID:             0b0e:%04x", dongle.productID)},
		)
		firmware := getFirmwareVersion(dongle.deviceID)
		if firmware == "" {
			firmware = "Unknown"
		}
		dongleSettignsMenu = append(dongleSettignsMenu,
			menuItem{id: -1, label: fmt.Sprintf("Firmware:           %s", firmware)},
		)
		if autoPairing, err := getAutoPairing(); err == nil {
			label := "Off"
			if autoPairing {
				label = "On"
			}
			dongleSettignsMenu = append(dongleSettignsMenu,
				menuItem{id: -1, label: fmt.Sprintf("Auto pairing:        %s (change locked)", label)},
			)
		}
		remembered := 0
		if dongle.pairingList != nil {
			remembered = len(dongle.pairingList.pairedDevices)
		}
		dongleSettignsMenu = append(dongleSettignsMenu,
			menuItem{id: -1, label: fmt.Sprintf("Remembered devices: %d", remembered)},
			menuItem{id: -1, label: "Pair a headset:      Locked"},
			menuItem{id: -1, label: "Factory reset:       Not ready"},
		)
	}
	requestUIRedraw()
}

func updateStartMenu() {
	startMenu = []menuItem{}
	if dongle, dongleexists := deviceManager[selectedDongle]; dongleexists {
		startMenu = append(startMenu,
			menuItem{id: 2, label: "Dongle settings"},
			menuItem{id: 4, label: "Firmware"},
		)
		if dongle.featureFlags != nil && dongle.featureFlags.pairingList {
			if dongle.pairingList != nil && dongle.pairingList.count != 0 {
				startMenu = append(startMenu, menuItem{id: 1, label: fmt.Sprintf("Remembered devices (%d)", len(dongle.pairingList.pairedDevices))})
			}
		}
	} else if _, headsetExists := deviceManager[selectedHeadset]; headsetExists {
		startMenu = append(startMenu, menuItem{id: 4, label: "Firmware"})
	}
	startMenu = append(startMenu, menuItem{id: 5, label: "Quit"})
	requestUIRedraw()
}

func serialNumberCheck(deviceInfo *jabra_DeviceInfo) bool {
	if deviceInfo.serialNumber == "" {
		return false
	}
	if deviceInfo.isDongle {
		if dongle, exists := deviceManager[selectedDongle]; exists {
			if dongle.serialNumber == deviceInfo.serialNumber && dongle.deviceConnection == deviceInfo.deviceConnection {
				dongle.deviceID = deviceInfo.deviceID
				return false
			}
			return true
		}
		return selectedDongle == -1
	}
	if device, exists := deviceManager[selectedHeadset]; exists {
		if device.serialNumber == deviceInfo.serialNumber && device.deviceConnection == deviceInfo.deviceConnection {
			device.deviceID = deviceInfo.deviceID
			return false
		}
		return true
	}
	return selectedHeadset == -1
}

func (d *devices) add(deviceInfo *jabra_DeviceInfo) {
	if *d == nil {
		*d = make(map[int]*jabra_DeviceInfo)
	}
	id := len(*d)
	deviceInfo.deviceID = uint16(id)
	if deviceInfo.isDongle {
		if selectedDongle == -1 {
			selectedDongle = id
			if deviceInfo.featureFlags != nil && deviceInfo.featureFlags.pairingList {
				go updatePairingList()
			}
		}
	} else {
		if selectedHeadset == -1 {
			selectedHeadset = id
			go batteryStatusUpdate()
		}
	}
	(*d)[id] = deviceInfo
	updateStartMenu()
}

func (d *devices) removed(deviceID uint16) {
	if *d == nil {
		return
	}
	var checkDongleExists, checkHeadSetExists bool
	newDevices := make(map[int]*jabra_DeviceInfo)
	nextIndex := 0
	for i := 0; i < len(*d); i++ {
		device, exists := (*d)[i]
		if !exists || device.deviceID == deviceID {
			continue
		}
		if device.isDongle {
			checkDongleExists = true
			selectedDongle = nextIndex
		} else {
			checkHeadSetExists = true
			selectedHeadset = nextIndex
		}
		newDevices[nextIndex] = device
		nextIndex++
	}
	if !checkDongleExists {
		stopUpdatePairingList <- struct{}{}
		selectedDongle = -1
	}
	if !checkHeadSetExists {
		stopUpdateBattery <- struct{}{}
		selectedHeadset = -1
	}
	*d = newDevices
	updateStartMenu()
}

func uninitialize() {
	// Pure Go — nothing to uninitialize (no C library).
}

func factoryReset(deviceID uint16) error {
	// TODO: requires GNP command opcode from live capture
	return ErrNotSupported
}

func getJabraSdkVersion() string {
	return "pure-go-1.0.0"
}

// getSupportedFeature queries the device via GNP class=0x04 op=0x2d.
// The response is a 12-byte capability bitmap. The bitmap encoding is:
// pairs of bytes where byte[0] is the feature ID and byte[1] is flags.
// Observed: 11 81 21 81 31 81 41 81 51 81 61 82
// → feature IDs: 0x11, 0x21, 0x31, 0x41, 0x51, 0x61 with flags 0x81/0x82.
// Returns an empty feature set if the exact query cannot be validated. It is
// safer to hide a feature than to expose an unsupported device write.
func getSupportedFeature(deviceID uint16) *featureFlags {
	ff := &featureFlags{}
	dev := deviceForID(deviceID)

	if dev != nil && dev.hidrawPath != "" {
		if h := openDeviceHidraw(dev); h != nil {
			defer h.close()
			src := gnpSrcHost
			if dev.isDongle {
				src = gnpSrcDongle
			}
			payload, err := gnpQueryPayload(h, src, nextSeq(), gnpClassStatus, gnpOpFeatureList)
			if err == nil && len(payload) >= 2 {
				// Parse feature bitmap — pairs of (id, flags)
				for i := 0; i+1 < len(payload); i += 2 {
					featureID := payload[i]
					enabled := payload[i+1]&0x80 != 0
					if !enabled {
						continue
					}
					switch featureID {
					case 0x11:
						ff.busyLight = true
					case 0x21:
						ff.factoryReset = true
					case 0x31:
						ff.pairingList = true
					case 0x41:
						ff.remoteMMI = true
					case 0x51:
						ff.musicEqualizer = true
					case 0x61:
						ff.onHeadDetection = true
					}
				}
				return ff
			}
		}
	}
	return ff
}

func getDeviceEventsMask(deviceID uint16) uint32 {
	return 0
}

/****************************************************************************/
/*                               BLUETOOTH                                  */
/****************************************************************************/

func selectedDongleDevice() (*jabra_DeviceInfo, error) {
	dongle, exists := deviceManager[selectedDongle]
	if !exists || dongle == nil {
		return nil, fmt.Errorf("no dongle found")
	}
	if dongle.hidrawPath == "" {
		return nil, fmt.Errorf("dongle has no GNP hidraw interface")
	}
	return dongle, nil
}

func requireExperimentalDeviceWrite(operation string) error {
	if !experimentalDeviceWritesEnabled() {
		return fmt.Errorf("%s: %w; set %s=%s only after hardware validation", operation, ErrNotSupported, experimentalWritesEnv, experimentalWritesAck)
	}
	return nil
}

func searchForNewDevices() error {
	return fmt.Errorf("search for new devices: %w (scan-event aggregation is not yet integrated)", ErrNotSupported)
}

func setDongleInBTPairing(pairing bool) error {
	if err := requireExperimentalDeviceWrite("Bluetooth pairing write"); err != nil {
		return err
	}
	dongle, err := selectedDongleDevice()
	if err != nil {
		return err
	}
	h := openDeviceHidraw(dongle)
	if h == nil {
		return fmt.Errorf("open dongle GNP interface")
	}
	defer h.close()

	op := gnpOpSearchDisable
	payload := []byte{0x03}
	if pairing {
		op = gnpOpSearchEnable
		payload = []byte{0x07, 60}
	}
	return gnpCommand(h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, op, payload)
}

func connectNewDevice(pairingID uint16) error {
	if err := requireExperimentalDeviceWrite("Bluetooth pair-by-address write"); err != nil {
		return err
	}
	if int(pairingID) >= len(searchDeviceList.pairedDevices) {
		return fmt.Errorf("search result %d does not exist", pairingID)
	}
	dongle, err := selectedDongleDevice()
	if err != nil {
		return err
	}
	h := openDeviceHidraw(dongle)
	if h == nil {
		return fmt.Errorf("open dongle GNP interface")
	}
	defer h.close()
	device := searchDeviceList.pairedDevices[pairingID]
	payload := append([]byte{0x04}, device.deviceBTAddr[:]...)
	return gnpCommand(h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, gnpOpBluetoothPair, payload)
}

func getSearchDeviceList(deviceID uint16) *pairingList {
	// TODO: requires GNP query opcode
	return &pairingList{count: 0, listType: searchResult, pairedDevices: []pairedDevice{}}
}

func getPairingList(deviceID uint16) *pairingList {
	result := &pairingList{listType: pairedDevices, pairedDevices: make([]pairedDevice, 0)}
	dongle, err := selectedDongleDevice()
	if err != nil {
		return result
	}
	h := openDeviceHidraw(dongle)
	if h == nil {
		return result
	}
	defer h.close()

	for _, bluetoothType := range []byte{0x00, 0x01} {
		index := uint16(0)
		for records := 0; records < 256; records++ {
			request := []byte{byte(index), byte(index >> 8), bluetoothType}
			namePayload, readErr := gnpQueryPayloadWithData(h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, gnpOpGetDBName, request)
			if readErr != nil {
				break
			}
			nextIndex, name, parseErr := parsePairingName(namePayload)
			if parseErr != nil {
				break
			}
			if name != "" {
				recordPayload, recordErr := gnpQueryPayloadWithData(h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, gnpOpGetDBRecord, request)
				if recordErr == nil {
					if device, recordParseErr := parsePairingRecord(name, recordPayload); recordParseErr == nil {
						result.pairedDevices = append(result.pairedDevices, device)
					}
				}
			}
			if nextIndex == 0xffff || nextIndex == index {
				break
			}
			index = nextIndex
		}
	}
	result.count = uint16(len(result.pairedDevices))
	return result
}

func parsePairingName(payload []byte) (uint16, string, error) {
	if len(payload) < 2 {
		return 0, "", fmt.Errorf("pairing name response too short: %d", len(payload))
	}
	nextIndex := binary.LittleEndian.Uint16(payload[:2])
	name := strings.Trim(string(payload[2:]), "\x00 \t\r\n")
	return nextIndex, name, nil
}

func parsePairingRecord(name string, payload []byte) (pairedDevice, error) {
	if len(payload) < 12 {
		return pairedDevice{}, fmt.Errorf("pairing record response too short: %d", len(payload))
	}
	device := pairedDevice{deviceName: name, isConnected: payload[2] == 3}
	copy(device.deviceBTAddr[:], payload[6:12])
	return device, nil
}

func clearPairingList() error {
	if _, exists := deviceManager[selectedDongle]; !exists {
		return fmt.Errorf("no dongle found")
	}
	// TODO: requires GNP command opcode
	return ErrNotSupported
}

func removeDeviceFromPairedlist(pairingID uint16) error {
	if _, exists := deviceManager[selectedDongle]; !exists {
		return fmt.Errorf("no dongle found")
	}
	// TODO: requires GNP command opcode
	return ErrNotSupported
}

func connectDeviceFromPairedlist(pairingID uint16) error {
	if err := requireExperimentalDeviceWrite("Bluetooth connect write"); err != nil {
		return err
	}
	dongle, err := selectedDongleDevice()
	if err != nil {
		return err
	}
	if dongle.pairingList == nil || int(pairingID) >= len(dongle.pairingList.pairedDevices) {
		return fmt.Errorf("paired device %d does not exist", pairingID)
	}
	h := openDeviceHidraw(dongle)
	if h == nil {
		return fmt.Errorf("open dongle GNP interface")
	}
	defer h.close()
	device := dongle.pairingList.pairedDevices[pairingID]
	payload := append([]byte{0x00}, device.deviceBTAddr[:]...)
	return gnpCommand(h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, gnpOpBluetoothConnect, payload)
}

func disconnectDeviceFromPairedlist(pairingID uint16) error {
	if err := requireExperimentalDeviceWrite("Bluetooth disconnect write"); err != nil {
		return err
	}
	dongle, err := selectedDongleDevice()
	if err != nil {
		return err
	}
	if dongle.pairingList == nil || int(pairingID) >= len(dongle.pairingList.pairedDevices) {
		return fmt.Errorf("paired device %d does not exist", pairingID)
	}
	h := openDeviceHidraw(dongle)
	if h == nil {
		return fmt.Errorf("open dongle GNP interface")
	}
	defer h.close()
	return gnpCommand(h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, gnpOpDisconnectAll, nil)
}

func reconnectToDevice() error {
	if _, exists := deviceManager[selectedDongle]; !exists {
		return fmt.Errorf("no dongle found")
	}
	// TODO: requires GNP command opcode
	return ErrNotSupported
}

func disconnectBTDeviceFromDongle() error {
	if err := requireExperimentalDeviceWrite("Bluetooth disconnect write"); err != nil {
		return err
	}
	dongle, err := selectedDongleDevice()
	if err != nil {
		return err
	}
	h := openDeviceHidraw(dongle)
	if h == nil {
		return fmt.Errorf("open dongle GNP interface")
	}
	defer h.close()
	return gnpCommand(h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, gnpOpDisconnectAll, nil)
}

func getAutoPairing() (bool, error) {
	dongle, err := selectedDongleDevice()
	if err != nil {
		return false, err
	}
	h := openDeviceHidraw(dongle)
	if h == nil {
		return false, fmt.Errorf("open dongle GNP interface")
	}
	defer h.close()
	payload, err := gnpQueryPayload(h, gnpSrcDongle, nextSeq(), gnpClassConfig, gnpOpAutoPairing)
	if err != nil {
		return false, fmt.Errorf("read auto pairing: %w", err)
	}
	if len(payload) != 1 || payload[0] > 1 {
		return false, fmt.Errorf("invalid auto-pairing response: %x", payload)
	}
	return payload[0] == 1, nil
}

func setAutoPairing(autoPairing bool) error {
	if err := requireExperimentalDeviceWrite("auto-pairing write"); err != nil {
		return err
	}
	dongle, err := selectedDongleDevice()
	if err != nil {
		return err
	}
	h := openDeviceHidraw(dongle)
	if h == nil {
		return fmt.Errorf("open dongle GNP interface")
	}
	defer h.close()
	value := byte(0)
	if autoPairing {
		value = 1
	}
	return gnpCommand(h, gnpSrcDongle, nextSeq(), gnpClassConfig, gnpOpAutoPairing, []byte{value})
}

/****************************************************************************/
/*                             BATTERY STATUS                               */
/****************************************************************************/

// getBatteryStatus queries the device via GNP class=0x04 op=0x01.
// Discovered from live probe (2026-04-12): response is 4 bytes:
//
//	byte[0] = RSSI/signal quality (fluctuates, informational)
//	byte[1] = flags (bit7=charging?, bit6=connected?)
//	byte[2] = component (0=unknown/main, 1=headband, etc.)
//	byte[3] = battery level percentage (0-100)
//
// Falls back to power_supply sysfs if GNP fails.
func getBatteryStatus(deviceID uint16) (*batteryStatus, error) {
	var dev *jabra_DeviceInfo
	for _, d := range deviceManager {
		if !d.isDongle {
			dev = d
			break
		}
	}
	if dev == nil {
		return nil, ErrNotSupported
	}

	// Try GNP query first
	if h := openDeviceHidraw(dev); h != nil {
		defer h.close()
		payload, err := gnpQueryPayload(h, gnpSrcHost, nextSeq(), gnpClassStatus, gnpOpBattery)
		if err == nil && len(payload) >= 4 {
			bs := &batteryStatus{
				levelInPercent: payload[3],
				charging:       payload[1]&0x80 != 0,
				batteryLow:     payload[3] <= 10,
				component:      batteryComponent(payload[2]),
			}
			return bs, nil
		}
	}

	// Fallback: power_supply sysfs
	psPath := dev.powerSupply
	if psPath == "" {
		return nil, fmt.Errorf("battery: no GNP or power_supply for %s", dev.deviceName)
	}
	bs := &batteryStatus{component: headband}
	if cap, err := readIntFile(filepath.Join(psPath, "capacity")); err == nil {
		bs.levelInPercent = uint8(cap)
	}
	if status, err := os.ReadFile(filepath.Join(psPath, "status")); err == nil {
		bs.charging = strings.TrimSpace(string(status)) == "Charging"
	}
	bs.batteryLow = bs.levelInPercent <= 10
	return bs, nil
}

func readIntFile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func updatePairingList() {
	for {
		select {
		case <-stopUpdatePairingList:
			return
		default:
			if dongle, exists := deviceManager[selectedDongle]; exists {
				update := getPairingList(dongle.deviceID)
				if dongle.pairingList != nil && update != nil {
					dongle.pairingList.count = update.count
					dongle.pairingList.listType = update.listType
					dongle.pairingList.pairedDevices = update.pairedDevices
					requestUIRedraw()
				}
			}
			time.Sleep(time.Second)
		}
	}
}

func batteryStatusUpdate() {
	for {
		select {
		case <-stopUpdateBattery:
			return
		default:
			if device, exists := deviceManager[selectedHeadset]; exists {
				battery, err := getBatteryStatus(device.deviceID)
				if err != nil {
					time.Sleep(time.Second)
					continue
				}
				device.batteryStatus.levelInPercent = battery.levelInPercent
				device.batteryStatus.charging = battery.charging
				device.batteryStatus.batteryLow = battery.batteryLow
				device.batteryStatus.component = battery.component
				device.batteryStatus.extraUnitsCount = battery.extraUnitsCount
				device.batteryStatus.extraUnits = battery.extraUnits
				requestUIRedraw()
			}
			time.Sleep(time.Second)
		}
	}
}

/****************************************************************************/
/*                               FIRMWARE                                   */
/****************************************************************************/

// getFirmwareVersion queries the device via GNP class=0x02, op=0x03.
// Confirmed from jfwu capture: TX 05 08 00 03 46 02 03 → RX has
// byte[6]=length, byte[7..]=ASCII version string.
func getFirmwareVersion(deviceID uint16) string {
	dev := deviceForID(deviceID)
	if dev == nil || dev.hidrawPath == "" {
		return ""
	}
	version, err := readFirmwareVersion(dev)
	if err != nil {
		return err.Error()
	}
	return version
}

func readFirmwareVersion(dev *jabra_DeviceInfo) (string, error) {
	h, err := openHidraw(dev.hidrawPath)
	if err != nil {
		return "", fmt.Errorf("open hidraw: %w", err)
	}
	defer h.close()

	src := gnpSrcHost
	if dev.isDongle {
		src = gnpSrcDongle
	}
	resp, err := gnpQuery(h, src, nextSeq(), gnpClassDevInfo, gnpOpFirmwareVer)
	if err != nil {
		return "", fmt.Errorf("read firmware: %w", err)
	}
	if len(resp) < 8 || resp[0] != 0x00 {
		return "", errors.New("invalid firmware reply")
	}
	if resp[4] == 0xFE {
		return "", fmt.Errorf("device returned error 0x%02x", resp[5])
	}
	strLen := int(resp[6])
	if len(resp) < 7+strLen {
		return "", errors.New("short firmware reply")
	}
	return string(resp[7 : 7+strLen]), nil
}

type deviceReleases struct {
	DeviceName string    `json:"deviceName"`
	Status     string    `json:"status"`
	Releases   []release `json:"releases"`
}

type release struct {
	Version      string     `json:"version"`
	ReleaseDate  customTime `json:"releaseDate"`
	DownloadUrl  string     `json:"downloadUrl"`
	Stage        string     `json:"stage"`
	FileName     string     `json:"fileName"`
	FileSize     string     `json:"fileSize"`
	ReleaseNotes string     `json:"releaseNotes"`
	RegionGroups []string   `json:"regionGroups"`
	Languages    []language `json:"languages"`
}

type language struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

type customTime struct {
	time.Time
}

func (ct *customTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	t, err := time.Parse("2006-01-02T15:04:05", s)
	if err != nil {
		return err
	}
	ct.Time = t
	return nil
}

// ── Busylight GNP control ────────────────────────────────────────────

// jabraBusylightSender sends busylight commands via GNP.
// Implements daemon.BusylightSender interface.
type jabraBusylightSender struct{}

func (s *jabraBusylightSender) SetBusylight(on bool) error {
	if !experimentalDeviceWritesEnabled() {
		return fmt.Errorf("busylight device write disabled; set %s=1 only after hardware validation", experimentalWritesEnv)
	}
	// Find device with hidraw (prefer dongle for busylight LED)
	var dev *jabra_DeviceInfo
	for _, d := range deviceManager {
		if d.hidrawPath != "" {
			dev = d
			break
		}
	}
	if dev == nil {
		return fmt.Errorf("busylight: no device with hidraw")
	}

	h, err := openHidraw(dev.hidrawPath)
	if err != nil {
		return fmt.Errorf("busylight: %w", err)
	}
	defer h.close()

	payload := byte(0x00)
	if on {
		payload = 0x01
	}

	// Busylight: class=0x04, op=0x22, src=dongle(0x01) or host(0x08)
	// Try dongle src first, fall back to host
	for _, src := range []byte{gnpSrcDongle, gnpSrcHost} {
		err := gnpCommand(h, src, nextSeq(), gnpClassStatus, gnpOpBusylight, []byte{payload})
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("busylight: no src address accepted")
}

func checkForFirmwareUpdate(deviceInfo *jabra_DeviceInfo) (latestVersion, downloadURL, fileName, releaseDate string, err error) {
	productIdHex := fmt.Sprintf("%x", deviceInfo.productID)
	vendorIdHex := fmt.Sprintf("%x", deviceInfo.vendorID)
	url := fmt.Sprintf("https://sdkbackend.jabra.com/v4/Firmware/%s?VendorId=%s&VariantType=%s", productIdHex, vendorIdHex, deviceInfo.variant)

	resp, err := http.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("bad status: %s", resp.Status)
		return
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	var deviceReleases deviceReleases
	err = json.Unmarshal(data, &deviceReleases)
	if err != nil {
		err = fmt.Errorf("error unmarshaling JSON: %w", err)
		return
	}

	if len(deviceReleases.Releases) == 0 {
		err = fmt.Errorf("no releases found")
		return
	}

	latestRelease := deviceReleases.Releases[0]
	for _, release := range deviceReleases.Releases[1:] {
		if release.ReleaseDate.After(latestRelease.ReleaseDate.Time) {
			latestRelease = release
		}
	}

	latestVersion = latestRelease.Version
	downloadURL = fmt.Sprintf("https://sdkbackend.jabra.com%s", latestRelease.DownloadUrl)
	fileName = latestRelease.FileName
	releaseDate = latestRelease.ReleaseDate.Format("2006-01-02")
	return
}

func downloadFirmware(url, output string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(output)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

/****************************************************************************/
/*                           DeviceSettings                                 */
/****************************************************************************/

type DataType int

const (
	SettingByte   DataType = 0
	SettingString DataType = 1
)

type ControlType int

const (
	CntrlRadio      ControlType = 0
	CntrlToggle     ControlType = 1
	CntrlComboBox   ControlType = 2
	CntrlDrpDown    ControlType = 3
	CntrlLabel      ControlType = 4
	CntrlTextBox    ControlType = 5
	CntrlButton     ControlType = 6
	CntrlEditButton ControlType = 7
	CntrlHorzRuler  ControlType = 8
	CntrlPwdTextBox ControlType = 9
	CntrlUnknown    ControlType = 10
)

type ValidationRule struct {
	MinLength    int
	MaxLength    int
	RegExp       string
	ErrorMessage string
}

type DependencySetting struct {
	GUID       string
	EnableFlag bool
}

type ListKeyValue struct {
	Key            uint16
	Value          string
	DependentCount int
	Dependents     []DependencySetting
}

type SettingInfo struct {
	GUID                       string
	Name                       string
	HelpText                   string
	CurrValue                  interface{}
	ListSize                   int
	ListKeyValue               []ListKeyValue
	IsValidationSupport        bool
	ValidationRule             *ValidationRule
	IsDeviceRestart            bool
	IsSettingProtected         bool
	IsSettingProtectionEnabled bool
	IsWirelessConnect          bool
	CntrlType                  ControlType
	SettingDataType            DataType
	GroupName                  string
	GroupHelpText              string
	IsDepedentsetting          bool
	DependentDefaultValue      interface{}
	IsPCsetting                bool
	IsChildDeviceSetting       bool
}

type DeviceSettings struct {
	SettingCount uint
	Settings     []SettingInfo
	ErrStatus    int
}

func GetSettings(deviceID uint16) *DeviceSettings {
	// TODO: requires GNP query opcode for device settings.
	// libjabra.so fetches settings schema from Jabra cloud
	// and reads values via GNP. Needs live capture to decode.
	return nil
}

func printSettingOptions(settings *DeviceSettings) {
	if settings == nil {
		fmt.Println("No settings available")
		return
	}
	fmt.Printf("Device has %d settings groups:\n", settings.SettingCount)
	for _, setting := range settings.Settings {
		fmt.Printf("\n==[ %s ]==\n", setting.Name)
		fmt.Printf("Group: %s\n", setting.GroupName)
		fmt.Printf("Current Value: %v\n", setting.CurrValue)
		if len(setting.ListKeyValue) > 0 {
			fmt.Println("Available Options:")
			for _, option := range setting.ListKeyValue {
				fmt.Printf("  %d: %s\n", option.Key, option.Value)
			}
		}
	}
}

// unused import guard
var _ = binary.LittleEndian
