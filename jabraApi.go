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
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	firmwaretool "github.com/Watchdog0x/jabridge/internal/firmware"
	"golang.org/x/sys/unix"
)

// ── Types (unchanged from original) ──────────────────────────────────

type jabra_DeviceInfo struct {
	deviceID         uint16
	productID        uint16
	vendorID         uint16
	deviceName       string
	usbDevicePath    string
	isDongle         bool
	serialNumber     string
	deviceConnection deviceConnectionType
	parentDeviceID   uint16
	featureFlags     *featureFlags
	batteryStatus    *batteryStatus
	pairingList      *pairingList
	// pure-Go additions
	hidrawPath  string // /dev/hidrawN for GNP
	powerSupply string // /sys/class/power_supply/... path
}

type batteryComponent int

const batteryHeadband batteryComponent = 1

type batteryStatus struct {
	levelInPercent uint8
	charging       bool
	batteryLow     bool
	component      batteryComponent
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

type featureFlags struct {
	busyLight       bool
	factoryReset    bool
	pairingList     bool
	remoteMMI       bool
	musicEqualizer  bool
	onHeadDetection bool
}

type devices map[int]*jabra_DeviceInfo
type deviceConnectionType int

const (
	deviceConnectionType_USB deviceConnectionType = iota
	deviceConnectionType_BT
)

type menuItem struct {
	id    int
	label string
}

var (
	deviceStateMu     sync.RWMutex
	deviceManager     devices
	selectedHeadset   = -1
	selectedDongle    = -1
	dongleChildMisses int

	startMenu           = []menuItem{}
	dongleSettingsLines = []menuItem{}

	searchDeviceList *pairingList = &pairingList{
		count:         0,
		listType:      searchResult,
		pairedDevices: make([]pairedDevice, 0),
	}

	firstScanComplete atomic.Bool
)

func cloneDeviceInfo(device *jabra_DeviceInfo) *jabra_DeviceInfo {
	if device == nil {
		return nil
	}
	clone := *device
	if device.featureFlags != nil {
		features := *device.featureFlags
		clone.featureFlags = &features
	}
	if device.batteryStatus != nil {
		battery := *device.batteryStatus
		clone.batteryStatus = &battery
	}
	if device.pairingList != nil {
		pairings := *device.pairingList
		pairings.pairedDevices = append([]pairedDevice(nil), device.pairingList.pairedDevices...)
		clone.pairingList = &pairings
	}
	return &clone
}

func deviceSnapshots() devices {
	deviceStateMu.RLock()
	defer deviceStateMu.RUnlock()
	snapshot := make(devices, len(deviceManager))
	for id, device := range deviceManager {
		snapshot[id] = cloneDeviceInfo(device)
	}
	return snapshot
}

func deviceAt(index int) (*jabra_DeviceInfo, bool) {
	deviceStateMu.RLock()
	defer deviceStateMu.RUnlock()
	device, exists := deviceManager[index]
	return cloneDeviceInfo(device), exists && device != nil
}

func selectedDongleSnapshot() (*jabra_DeviceInfo, bool) {
	deviceStateMu.RLock()
	defer deviceStateMu.RUnlock()
	device, exists := deviceManager[selectedDongle]
	return cloneDeviceInfo(device), exists && device != nil
}

func selectedHeadsetSnapshot() (*jabra_DeviceInfo, bool) {
	deviceStateMu.RLock()
	defer deviceStateMu.RUnlock()
	device, exists := deviceManager[selectedHeadset]
	return cloneDeviceInfo(device), exists && device != nil
}

func updateDeviceByID(deviceID uint16, update func(*jabra_DeviceInfo)) bool {
	deviceStateMu.Lock()
	defer deviceStateMu.Unlock()
	for _, device := range deviceManager {
		if device != nil && device.deviceID == deviceID {
			update(device)
			return true
		}
	}
	return false
}

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
		_ = h.f.Close()
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
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 256)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("hidraw read timeout")
		}
		milliseconds := int((remaining + time.Millisecond - 1) / time.Millisecond)
		pollFDs := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		ready, err := unix.Poll(pollFDs, milliseconds)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("hidraw poll: %w", err)
		}
		if ready == 0 {
			return nil, fmt.Errorf("hidraw read timeout")
		}
		if pollFDs[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return nil, fmt.Errorf("hidraw poll failed: revents=0x%x", pollFDs[0].Revents)
		}
		n, err := unix.Read(fd, buf)
		if err == nil && n > 0 {
			if buf[0] != gnpReportID {
				continue // skip non-GNP reports
			}
			return append([]byte(nil), buf[:n]...), nil
		}
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
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
	gnpIOMu.Lock()
	defer gnpIOMu.Unlock()
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
	gnpIOMu.Lock()
	defer gnpIOMu.Unlock()
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
	gnpIOMu       sync.Mutex
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
	if serial != "" {
		for _, entry := range entries {
			path := filepath.Join("/sys/class/power_supply", entry.Name())
			uevent, err := os.ReadFile(filepath.Join(path, "uevent"))
			if err == nil && strings.Contains(string(uevent), serial) {
				return path
			}
		}
	}
	for _, entry := range entries {
		p := filepath.Join("/sys/class/power_supply", entry.Name())
		if strings.Contains(strings.ToUpper(entry.Name()), vidPid) {
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
	deviceStateMu.RLock()
	defer deviceStateMu.RUnlock()
	for _, dev := range deviceManager {
		if dev.deviceID == deviceID {
			return cloneDeviceInfo(dev)
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
	present := make(map[string]bool, len(usbDevs))
	for _, device := range usbDevs {
		if !isAccessoryName(device.product) {
			present[device.sysPath] = true
		}
	}
	removeMissingUSBDevices(present)

	for _, ud := range usbDevs {
		if isAccessoryName(ud.product) {
			continue
		}
		// Skip if already known
		alreadyKnown := false
		for _, existing := range deviceSnapshots() {
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
		}

		if newDevice := isNewDevice(dev); newDevice {
			addDevice(dev)
			if dev.isDongle && supportsValidatedPairingReads(dev.productID) {
				if pairings, err := getPairingList(dev.deviceID); err == nil {
					updateDeviceByID(dev.deviceID, func(stored *jabra_DeviceInfo) {
						stored.pairingList = pairings
						stored.featureFlags.pairingList = true
					})
				}
			} else if !dev.isDongle {
				if battery, err := getBatteryStatus(dev.deviceID); err == nil {
					updateDeviceByID(dev.deviceID, func(stored *jabra_DeviceInfo) {
						stored.batteryStatus = battery
					})
				}
			}
			requestUIRedraw()
		}
	}
}

// pollDevices runs the device scan loop (replaces udev monitoring).
func pollDevices(ctx context.Context) {
	// Initial scan
	scanAndAttachDevices()
	refreshSelectedDeviceData()
	firstScanComplete.Store(true)
	requestUIRedraw()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scanAndAttachDevices()
			refreshSelectedDeviceData()
		}
	}
}

func removeMissingUSBDevices(present map[string]bool) {
	deviceStateMu.Lock()
	changed := false
	for id, device := range deviceManager {
		if device == nil || device.deviceConnection != deviceConnectionType_USB || device.usbDevicePath == "" {
			continue
		}
		if !present[device.usbDevicePath] {
			delete(deviceManager, id)
			changed = true
		}
	}
	if changed {
		selectedDongle = firstDeviceIndexLocked(true)
		selectedHeadset = firstDeviceIndexLocked(false)
	}
	deviceStateMu.Unlock()
	if changed {
		requestUIRedraw()
	}
}

func firstDeviceIndexLocked(wantDongle bool) int {
	selected := -1
	for id, device := range deviceManager {
		if device != nil && device.isDongle == wantDongle && (selected == -1 || id < selected) {
			selected = id
		}
	}
	return selected
}

func refreshSelectedDeviceData() {
	if dongle, exists := selectedDongleSnapshot(); exists && supportsValidatedPairingReads(dongle.productID) {
		updated, err := getPairingList(dongle.deviceID)
		if err != nil {
			updated = nil
		}
		changed := false
		if updated != nil {
			updateDeviceByID(dongle.deviceID, func(stored *jabra_DeviceInfo) {
				if !pairingListsEqual(stored.pairingList, updated) {
					stored.pairingList = updated
					changed = true
				}
				if stored.featureFlags != nil {
					stored.featureFlags.pairingList = true
				}
			})
		}
		if changed {
			requestUIRedraw()
		}
	}
	refreshDongleChildDevice()

	if headset, exists := selectedHeadsetSnapshot(); exists && headset.deviceConnection == deviceConnectionType_USB {
		updated, err := getBatteryStatus(headset.deviceID)
		if err == nil {
			changed := false
			updateDeviceByID(headset.deviceID, func(stored *jabra_DeviceInfo) {
				if !batteryStatusesEqual(stored.batteryStatus, updated) {
					stored.batteryStatus = updated
					changed = true
				}
			})
			if changed {
				requestUIRedraw()
			}
		} else {
			changed := false
			updateDeviceByID(headset.deviceID, func(stored *jabra_DeviceInfo) {
				if stored.batteryStatus != nil {
					stored.batteryStatus = nil
					changed = true
				}
			})
			if changed {
				requestUIRedraw()
			}
		}
	}
}

func refreshDongleChildDevice() {
	dongle, exists := selectedDongleSnapshot()
	if !exists || dongle.hidrawPath == "" {
		removeDongleChildAfterMiss()
		return
	}

	gnpIOMu.Lock()
	transport, err := firmwaretool.OpenHidraw(dongle.hidrawPath)
	if err == nil {
		defer func() { _ = transport.Close() }()
	}
	var productID uint16
	var name string
	if err == nil {
		productID, err = firmwaretool.QueryChildProductID(transport, nextSeq(), 750*time.Millisecond)
	}
	if err == nil && productID != 0 {
		name, err = firmwaretool.QueryChildName(transport, nextSeq(), 750*time.Millisecond)
	}
	gnpIOMu.Unlock()
	if err != nil || productID == 0 {
		removeDongleChildAfterMiss()
		return
	}
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("Jabra headset (PID %04x)", productID)
	}
	if upsertDongleChild(dongle.deviceID, productID, name) {
		requestUIRedraw()
	}
}

func upsertDongleChild(parentID, productID uint16, name string) bool {
	deviceStateMu.Lock()
	defer deviceStateMu.Unlock()
	dongleChildMisses = 0
	for _, device := range deviceManager {
		if device != nil && device.deviceConnection == deviceConnectionType_BT && device.parentDeviceID == parentID {
			changed := device.productID != productID || device.deviceName != name
			device.productID = productID
			device.deviceName = name
			return changed
		}
	}
	id := 0
	for {
		if _, exists := deviceManager[id]; !exists {
			break
		}
		id++
	}
	deviceManager[id] = &jabra_DeviceInfo{
		deviceID:         uint16(id),
		productID:        productID,
		vendorID:         jabraVendorID,
		deviceName:       name,
		parentDeviceID:   parentID,
		deviceConnection: deviceConnectionType_BT,
		featureFlags:     &featureFlags{},
	}
	if selectedHeadset == -1 {
		selectedHeadset = id
	}
	return true
}

func removeDongleChildAfterMiss() {
	deviceStateMu.Lock()
	dongleChildMisses++
	if dongleChildMisses < 2 {
		deviceStateMu.Unlock()
		return
	}
	changed := false
	for id, device := range deviceManager {
		if device != nil && device.deviceConnection == deviceConnectionType_BT {
			delete(deviceManager, id)
			changed = true
		}
	}
	if changed {
		selectedHeadset = firstDeviceIndexLocked(false)
	}
	deviceStateMu.Unlock()
	if changed {
		requestUIRedraw()
	}
}

func pairingListsEqual(left, right *pairingList) bool {
	if left == nil || right == nil {
		return left == right
	}
	if left.count != right.count || left.listType != right.listType || len(left.pairedDevices) != len(right.pairedDevices) {
		return false
	}
	for index := range left.pairedDevices {
		if left.pairedDevices[index] != right.pairedDevices[index] {
			return false
		}
	}
	return true
}

func batteryStatusesEqual(left, right *batteryStatus) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.levelInPercent == right.levelInPercent && left.charging == right.charging &&
		left.batteryLow == right.batteryLow && left.component == right.component
}

/****************************************************************************/
/*                           GENERAL UTILITIES                              */
/****************************************************************************/

func updateDongleSettings() {
	dongleSettingsLines = []menuItem{}
	if dongle, exists := selectedDongleSnapshot(); exists {
		dongleSettingsLines = append(dongleSettingsLines,
			menuItem{id: -1, label: fmt.Sprintf("Device:             %s", dongle.deviceName)},
			menuItem{id: -1, label: fmt.Sprintf("USB ID:             0b0e:%04x", dongle.productID)},
		)
		firmware := getFirmwareVersion(dongle.deviceID)
		if firmware == "" {
			firmware = "Unknown"
		}
		dongleSettingsLines = append(dongleSettingsLines,
			menuItem{id: -1, label: fmt.Sprintf("Firmware:           %s", firmware)},
		)
		if autoPairing, err := getAutoPairing(); err == nil {
			label := "Off"
			if autoPairing {
				label = "On"
			}
			dongleSettingsLines = append(dongleSettingsLines,
				menuItem{id: -1, label: fmt.Sprintf("Auto pairing:        %s (change locked)", label)},
			)
		}
		remembered := 0
		if dongle.pairingList != nil {
			remembered = len(dongle.pairingList.pairedDevices)
		}
		dongleSettingsLines = append(dongleSettingsLines,
			menuItem{id: -1, label: fmt.Sprintf("Remembered devices: %d", remembered)},
			menuItem{id: -1, label: "Pair a headset:      Locked"},
			menuItem{id: -1, label: "Factory reset:       Not ready"},
		)
	}
	requestUIRedraw()
}

func updateStartMenu() {
	startMenu = []menuItem{}
	if dongle, dongleexists := selectedDongleSnapshot(); dongleexists {
		startMenu = append(startMenu,
			menuItem{id: 2, label: "Dongle settings"},
			menuItem{id: 4, label: "Firmware"},
		)
		if dongle.featureFlags != nil && dongle.featureFlags.pairingList {
			if dongle.pairingList != nil && dongle.pairingList.count != 0 {
				startMenu = append(startMenu, menuItem{id: 1, label: fmt.Sprintf("Remembered devices (%d)", len(dongle.pairingList.pairedDevices))})
			}
		}
	} else if _, headsetExists := selectedHeadsetSnapshot(); headsetExists {
		startMenu = append(startMenu, menuItem{id: 4, label: "Firmware"})
	}
	startMenu = append(startMenu, menuItem{id: 5, label: "Quit"})
}

func isNewDevice(deviceInfo *jabra_DeviceInfo) bool {
	for _, existing := range deviceSnapshots() {
		if existing.productID != deviceInfo.productID || existing.deviceConnection != deviceInfo.deviceConnection {
			continue
		}
		if deviceInfo.serialNumber != "" && existing.serialNumber == deviceInfo.serialNumber {
			return false
		}
		if deviceInfo.usbDevicePath != "" && existing.usbDevicePath == deviceInfo.usbDevicePath {
			return false
		}
	}
	return true
}

func addDevice(deviceInfo *jabra_DeviceInfo) {
	deviceStateMu.Lock()
	defer deviceStateMu.Unlock()
	if deviceManager == nil {
		deviceManager = make(devices)
	}
	id := 0
	for {
		if _, exists := deviceManager[id]; !exists {
			break
		}
		id++
	}
	deviceInfo.deviceID = uint16(id)
	if deviceInfo.isDongle {
		if selectedDongle == -1 {
			selectedDongle = id
		}
	} else {
		if selectedHeadset == -1 {
			selectedHeadset = id
		}
	}
	deviceManager[id] = cloneDeviceInfo(deviceInfo)
}

func factoryReset(deviceID uint16) error {
	// TODO: requires GNP command opcode from live capture
	return ErrNotSupported
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

/****************************************************************************/
/*                               BLUETOOTH                                  */
/****************************************************************************/

func selectedDongleDevice() (*jabra_DeviceInfo, error) {
	dongle, exists := selectedDongleSnapshot()
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

func getPairingList(deviceID uint16) (*pairingList, error) {
	result := &pairingList{listType: pairedDevices, pairedDevices: make([]pairedDevice, 0)}
	dongle := deviceForID(deviceID)
	if dongle == nil || !dongle.isDongle || dongle.hidrawPath == "" {
		return nil, fmt.Errorf("pairing list unavailable for device %d", deviceID)
	}
	h := openDeviceHidraw(dongle)
	if h == nil {
		return nil, fmt.Errorf("open dongle GNP interface")
	}
	defer h.close()

	querySucceeded := false
	var lastErr error
	for _, bluetoothType := range []byte{0x00, 0x01} {
		index := uint16(0)
		for records := 0; records < 256; records++ {
			request := []byte{byte(index), byte(index >> 8), bluetoothType}
			namePayload, readErr := gnpQueryPayloadWithData(h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, gnpOpGetDBName, request)
			if readErr != nil {
				lastErr = readErr
				break
			}
			querySucceeded = true
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
	result.pairedDevices = deduplicatePairedDevices(result.pairedDevices)
	result.count = uint16(len(result.pairedDevices))
	if !querySucceeded && lastErr != nil {
		return nil, fmt.Errorf("read pairing list: %w", lastErr)
	}
	return result, nil
}

func deduplicatePairedDevices(devices []pairedDevice) []pairedDevice {
	result := make([]pairedDevice, 0, len(devices))
	indexes := make(map[string]int, len(devices))
	for _, device := range devices {
		key := fmt.Sprintf("addr:%x", device.deviceBTAddr)
		if device.deviceBTAddr == [6]byte{} {
			key = "name:" + strings.ToLower(strings.TrimSpace(device.deviceName))
		}
		if index, exists := indexes[key]; exists {
			if device.isConnected {
				result[index].isConnected = true
			}
			continue
		}
		indexes[key] = len(result)
		result = append(result, device)
	}
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

// getBatteryStatus reads Linux's HID power_supply data. The earlier preview
// treated a fluctuating signal byte as a percentage and produced values above
// 200%. Until the vendor payload layout is proven across real headsets, the
// kernel's validated 0..100 capacity is the only accepted battery source.
func getBatteryStatus(deviceID uint16) (*batteryStatus, error) {
	dev := deviceForID(deviceID)
	if dev == nil || dev.isDongle {
		dev, _ = selectedHeadsetSnapshot()
	}
	if dev == nil {
		return nil, ErrNotSupported
	}

	psPath := dev.powerSupply
	if psPath == "" {
		psPath = findPowerSupplyPath(dev.vendorID, dev.productID, dev.serialNumber)
	}
	if psPath == "" {
		return nil, fmt.Errorf("battery unavailable: no Linux power_supply for %s", dev.deviceName)
	}
	bs := &batteryStatus{component: batteryHeadband}
	capacity, err := readIntFile(filepath.Join(psPath, "capacity"))
	if err != nil {
		return nil, fmt.Errorf("read battery capacity: %w", err)
	}
	level, err := validatedBatteryCapacity(capacity)
	if err != nil {
		return nil, err
	}
	bs.levelInPercent = level
	if status, err := os.ReadFile(filepath.Join(psPath, "status")); err == nil {
		bs.charging = strings.EqualFold(strings.TrimSpace(string(status)), "Charging")
	}
	bs.batteryLow = bs.levelInPercent <= 10
	return bs, nil
}

func validatedBatteryCapacity(capacity int) (uint8, error) {
	if capacity < 0 || capacity > 100 {
		return 0, fmt.Errorf("invalid kernel battery capacity %d", capacity)
	}
	return uint8(capacity), nil
}

func readIntFile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
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
	for _, d := range deviceSnapshots() {
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
