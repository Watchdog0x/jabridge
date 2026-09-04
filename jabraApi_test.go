package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func withDeviceState(t *testing.T, manager devices, headset, dongle int) {
	t.Helper()
	deviceStateMu.Lock()
	oldManager := deviceManager
	oldHeadset := selectedHeadset
	oldDongle := selectedDongle
	oldChildMisses := dongleChildMisses
	deviceManager = manager
	selectedHeadset = headset
	selectedDongle = dongle
	dongleChildMisses = 0
	deviceStateMu.Unlock()
	oldStartMenu := startMenu
	oldDongleMenu := dongleSettingsLines
	t.Cleanup(func() {
		deviceStateMu.Lock()
		deviceManager = oldManager
		selectedHeadset = oldHeadset
		selectedDongle = oldDongle
		dongleChildMisses = oldChildMisses
		deviceStateMu.Unlock()
		startMenu = oldStartMenu
		dongleSettingsLines = oldDongleMenu
	})
}

func TestKnownDonglePIDs(t *testing.T) {
	for _, pid := range []uint16{0x24c7, 0x24c8, 0x0a17, 0x2483, 0x2484} {
		if !isKnownDonglePID(pid) {
			t.Errorf("PID 0x%04x should be recognized as a dongle", pid)
		}
	}
	if isKnownDonglePID(0x24b9) {
		t.Fatal("Evolve2 85 headset PID was classified as a dongle")
	}
}

func TestValidatedPairingReadsAreNarrowlyAllowlisted(t *testing.T) {
	if !supportsValidatedPairingReads(0x24c7) || !supportsValidatedPairingReads(0x24c8) {
		t.Fatal("Link 380 variants should allow read-only pairing-list queries")
	}
	if supportsValidatedPairingReads(0x0a17) {
		t.Fatal("untested Link 370 was allowlisted")
	}
}

func TestExperimentalDongleWritesAreNarrowlyAllowlisted(t *testing.T) {
	for _, pid := range []uint16{0x24c7, 0x24c8} {
		if !supportsExperimentalDongleWrites(pid) {
			t.Errorf("Link 380 PID 0x%04x should be allowlisted", pid)
		}
	}
	for _, pid := range []uint16{0x0a17, 0x2483, 0x2484, 0x24b9} {
		if supportsExperimentalDongleWrites(pid) {
			t.Errorf("unvalidated PID 0x%04x was allowlisted for writes", pid)
		}
	}
}

func TestAccessoryNames(t *testing.T) {
	for _, name := range []string{"Jabra Evolve2 75 Deskstand", "Charging Cradle", "Jabra Busy Light"} {
		if !isAccessoryName(name) {
			t.Errorf("%q should be classified as an accessory", name)
		}
	}
	if isAccessoryName("Jabra Evolve2 85") {
		t.Fatal("headset was classified as an accessory")
	}
}

func TestDecodeLengthPrefixedString(t *testing.T) {
	got, ok := decodeLengthPrefixedString([]byte{4, 't', 'e', 's', 't'})
	if !ok || got != "test" {
		t.Fatalf("decode = %q, %v", got, ok)
	}
	for _, invalid := range [][]byte{nil, {0}, {4, 'x'}} {
		if got, ok := decodeLengthPrefixedString(invalid); ok {
			t.Fatalf("invalid payload decoded as %q", got)
		}
	}
}

func TestFirmwareReadDestinations(t *testing.T) {
	if got := firmwareReadDestinations(&jabra_DeviceInfo{isDongle: true}); len(got) != 1 || got[0] != gnpSrcDongle {
		t.Fatalf("dongle destinations = %v", got)
	}
	got := firmwareReadDestinations(&jabra_DeviceInfo{})
	if len(got) != 5 || got[0] != gnpSrcHost || got[1] != 3 || got[2] != 1 || got[3] != 0 || got[4] != 2 {
		t.Fatalf("headset/controller destinations = %v", got)
	}
	got = firmwareReadDestinations(&jabra_DeviceInfo{gnpDestination: 1, gnpDestinationKnown: true})
	if len(got) != 5 || got[0] != 1 {
		t.Fatalf("discovered destination was not first or was duplicated: %v", got)
	}
}

func TestFirstResponsiveGNPEndpointSkipsWrongInterface(t *testing.T) {
	var attempts []string
	path, destination, found := firstResponsiveGNPEndpoint(
		[]string{"/dev/hidraw-wrong", "/dev/hidraw-control"},
		[]byte{8, 3, 1},
		func(candidate string, address byte) bool {
			attempts = append(attempts, fmt.Sprintf("%s:%d", candidate, address))
			return candidate == "/dev/hidraw-control" && address == 1
		},
	)
	if !found || path != "/dev/hidraw-control" || destination != 1 {
		t.Fatalf("endpoint = %q address %d found=%v", path, destination, found)
	}
	wantLast := "/dev/hidraw-control:1"
	if len(attempts) != 6 || attempts[len(attempts)-1] != wantLast {
		t.Fatalf("probe attempts = %v, want six ending in %q", attempts, wantLast)
	}
}

func TestDecodeFirmwareVersionPayload(t *testing.T) {
	got, err := decodeFirmwareVersionPayload([]byte{6, '1', '.', '2', '.', '3', '4'})
	if err != nil || got != "1.2.34" {
		t.Fatalf("version = %q, %v", got, err)
	}
	if _, err := decodeFirmwareVersionPayload([]byte{5, '1'}); err == nil {
		t.Fatal("short firmware version was accepted")
	}
}

func TestDecodeDeviceVariant(t *testing.T) {
	if got, ok := decodeDeviceVariant([]byte{1, 0x04, 0x0b}); !ok || got != "04-0B" {
		t.Fatalf("variant = %q, %v", got, ok)
	}
	if _, ok := decodeDeviceVariant([]byte{1, 2}); ok {
		t.Fatal("short variant payload was accepted")
	}
}

func TestHIDUeventMatchHandlesKernelPadding(t *testing.T) {
	data := []byte("DRIVER=jabra\nHID_ID=0003:00000B0E:000024C7\nHID_NAME=Jabra Link 380\n")
	if !hidUeventMatches(data, 0x0b0e, 0x24c7) {
		t.Fatal("padded kernel HID_ID did not match Link 380")
	}
	if hidUeventMatches(data, 0x0b0e, 0x24b9) {
		t.Fatal("wrong product ID matched")
	}
}

func TestDongleOnlyMenuDoesNotAdvertiseUnqualifiedPairing(t *testing.T) {
	dongle := &jabra_DeviceInfo{
		deviceID:     0,
		deviceName:   "Jabra Link 380",
		isDongle:     true,
		featureFlags: &featureFlags{},
		pairingList:  &pairingList{},
	}
	withDeviceState(t, devices{0: dongle}, -1, 0)

	updateStartMenu()
	if len(startMenu) != 3 {
		t.Fatalf("dongle-only menu has %d items, want settings, firmware, and quit", len(startMenu))
	}
	if startMenu[0].id != 2 || startMenu[1].id != 4 || startMenu[2].id != 5 {
		t.Fatalf("unexpected dongle-only menu: %#v", startMenu)
	}
}

func TestUnqualifiedDongleOperationsFailClosed(t *testing.T) {
	dongle := &jabra_DeviceInfo{deviceID: 0, isDongle: true, featureFlags: &featureFlags{}}
	withDeviceState(t, devices{0: dongle}, -1, 0)

	if err := searchForNewDevices(); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("search error = %v, want ErrNotSupported", err)
	}
	if err := setDongleInBTPairing(true); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("pairing error = %v, want ErrNotSupported", err)
	}
	if _, err := getAutoPairing(); err == nil {
		t.Fatal("auto-pair read unexpectedly succeeded without a hidraw interface")
	}
}

func TestFeatureFailureDoesNotInventCapabilities(t *testing.T) {
	withDeviceState(t, nil, -1, -1)
	flags := getSupportedFeature(0)
	if flags.busyLight || flags.factoryReset || flags.pairingList {
		t.Fatalf("invented feature flags after unavailable query: %#v", flags)
	}
}

func TestBusylightWritesDisabledByDefault(t *testing.T) {
	t.Setenv(experimentalWritesEnv, "")
	err := (&jabraBusylightSender{}).SetBusylight(true)
	if err == nil {
		t.Fatal("busylight write unexpectedly enabled")
	}
}

func TestPairingPacketBuildersMatchCurrentSDK(t *testing.T) {
	read, err := buildGNPReport(gnpSrcDongle, 0x20, gnpFlagQuery, gnpClassConfig, gnpOpAutoPairing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0x05, 0x01, 0x00, 0x20, 0x46, 0x13, 0x40}; !bytes.Equal(read[:len(want)], want) {
		t.Fatalf("auto-pair read = %x, want %x", read[:len(want)], want)
	}

	write, err := buildGNPReport(gnpSrcDongle, 0x21, gnpFlagCmd, gnpClassPairingDevice, gnpOpSearchEnable, []byte{0x07, 60})
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0x05, 0x01, 0x00, 0x21, 0x88, 0x0d, 0x20, 0x07, 0x3c}; !bytes.Equal(write[:len(want)], want) {
		t.Fatalf("search write = %x, want %x", write[:len(want)], want)
	}

	reset, err := buildGNPReport(gnpSrcDongle, 0x22, gnpFlagCmd, gnpClassPairingDevice, gnpOpFactoryDefaultBT, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0x05, 0x01, 0x00, 0x22, 0x86, 0x0d, 0x13}; !bytes.Equal(reset[:len(want)], want) {
		t.Fatalf("factory reset = %x, want %x", reset[:len(want)], want)
	}
}

func TestParseGNPReplyPayloadValidatesIdentity(t *testing.T) {
	reply := []byte{0x05, 0x00, 0x01, 0x20, 0xc7, 0x13, 0x40, 0x01}
	payload, err := parseGNPReplyPayload(reply, 0x20, gnpClassConfig, gnpOpAutoPairing)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, []byte{1}) {
		t.Fatalf("payload = %x, want 01", payload)
	}
	if _, err := parseGNPReplyPayload(reply, 0x21, gnpClassConfig, gnpOpAutoPairing); err == nil {
		t.Fatal("sequence mismatch was accepted")
	}
	if _, err := parseGNPReplyPayload(reply, 0x20, gnpClassPairingDevice, gnpOpAutoPairing); err == nil {
		t.Fatal("command mismatch was accepted")
	}
}

func TestParsePairingListRecords(t *testing.T) {
	next, name, err := parsePairingName([]byte{0xff, 0xff, 'T', 'e', 's', 't', 0x00})
	if err != nil {
		t.Fatal(err)
	}
	if next != 0xffff || name != "Test" {
		t.Fatalf("pairing name = next 0x%04x name %q", next, name)
	}

	record := []byte{0x00, 0x00, 0x03, 0x00, 0x00, 0x01, 1, 2, 3, 4, 5, 6}
	device, err := parsePairingRecord(name, record, 7, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !device.isConnected || device.deviceBTAddr != [6]byte{1, 2, 3, 4, 5, 6} {
		t.Fatalf("pairing record parsed incorrectly: %#v", device)
	}
	if device.databaseIndex != 7 || device.bluetoothType != 1 {
		t.Fatalf("pairing record metadata = index %d type %d", device.databaseIndex, device.bluetoothType)
	}
	if _, err := parsePairingRecord(name, record[:11], 7, 1); err == nil {
		t.Fatal("truncated pairing record was accepted")
	}
}

func TestPairingListDeduplicatesBluetoothTypes(t *testing.T) {
	address := [6]byte{1, 2, 3, 4, 5, 6}
	devices := []pairedDevice{
		{deviceName: "Headset", deviceBTAddr: address},
		{deviceName: "Headset", deviceBTAddr: address, isConnected: true},
	}
	got := deduplicatePairedDevices(devices)
	if len(got) != 1 {
		t.Fatalf("deduplicated list has %d devices, want 1", len(got))
	}
	if !got[0].isConnected {
		t.Fatal("connected state was lost while deduplicating")
	}
}

func TestMissingUSBDeviceIsRemoved(t *testing.T) {
	dongle := &jabra_DeviceInfo{
		deviceID: 0, productID: 0x24c8, isDongle: true,
		usbDevicePath: "/sys/device/dongle",
	}
	headset := &jabra_DeviceInfo{
		deviceID: 1, productID: 0x24b7,
		usbDevicePath: "/sys/device/headset",
	}
	withDeviceState(t, devices{0: dongle, 1: headset}, 1, 0)

	removeMissingUSBDevices(map[string]bool{"/sys/device/dongle": true})
	if _, exists := deviceAt(1); exists {
		t.Fatal("detached headset remained in the registry")
	}
	if _, exists := selectedHeadsetSnapshot(); exists {
		t.Fatal("detached headset remained selected")
	}
}

func TestUSBDeviceIsRegisteredBeforeProtocolEnrichment(t *testing.T) {
	withDeviceState(t, devices{}, -1, -1)
	device, added := registerUSBDevice(usbDev{
		sysPath: "/sys/device/speak510", vendorID: jabraVendorID,
		productID: 0x0422, product: "Jabra SPEAK 510 USB",
	})
	if !added || device == nil {
		t.Fatal("sysfs device was not registered immediately")
	}
	stored, exists := selectedHeadsetSnapshot()
	if !exists || stored.deviceName != "Jabra SPEAK 510 USB" || stored.productID != 0x0422 {
		t.Fatalf("registered device = %#v, exists=%v", stored, exists)
	}
	if _, duplicate := registerUSBDevice(usbDev{
		sysPath: "/sys/device/speak510", vendorID: jabraVendorID,
		productID: 0x0422, product: "Jabra SPEAK 510 USB",
	}); duplicate {
		t.Fatal("same USB path was registered twice")
	}
}

func TestDongleChildLifecycle(t *testing.T) {
	dongle := &jabra_DeviceInfo{deviceID: 0, productID: 0x24c8, isDongle: true}
	withDeviceState(t, devices{0: dongle}, -1, 0)

	if !upsertDongleChild(0, 0x24b7, "Jabra Evolve2 65") {
		t.Fatal("new dongle child did not change state")
	}
	child, exists := selectedHeadsetSnapshot()
	if !exists || child.productID != 0x24b7 || child.deviceConnection != deviceConnectionType_BT {
		t.Fatalf("unexpected dongle child: %#v", child)
	}
	if upsertDongleChild(0, 0x24b7, "Jabra Evolve2 65") {
		t.Fatal("unchanged dongle child triggered a redraw")
	}
	removeDongleChildAfterMiss()
	if _, exists := selectedHeadsetSnapshot(); !exists {
		t.Fatal("one transient miss removed the dongle child")
	}
	removeDongleChildAfterMiss()
	if _, exists := selectedHeadsetSnapshot(); exists {
		t.Fatal("two misses did not remove the dongle child")
	}
}

func TestDongleChildCleanupKeepsDirectUSBHeadset(t *testing.T) {
	dongle := &jabra_DeviceInfo{deviceID: 0, productID: 0x24c8, isDongle: true}
	direct := &jabra_DeviceInfo{
		deviceID: 1, productID: 0x0422, deviceName: "Jabra SPEAK 510 USB",
		deviceConnection: deviceConnectionType_USB,
	}
	child := &jabra_DeviceInfo{
		deviceID: 2, productID: 0x24b7, deviceName: "Wireless headset",
		deviceConnection: deviceConnectionType_BT, parentDeviceID: 0,
	}
	withDeviceState(t, devices{0: dongle, 1: direct, 2: child}, 1, 0)
	removeDongleChildAfterMiss()
	removeDongleChildAfterMiss()
	if _, exists := deviceAt(2); exists {
		t.Fatal("stale dongle child was not removed")
	}
	stored, exists := deviceAt(1)
	if !exists || stored.deviceName != "Jabra SPEAK 510 USB" {
		t.Fatalf("direct USB device was removed: %#v, exists=%v", stored, exists)
	}
}

func TestIPCDeviceListUsesCachedFirmwareOnly(t *testing.T) {
	withDeviceState(t, devices{
		0: {
			deviceID: 0, productID: 0x0422, vendorID: jabraVendorID,
			deviceName: "Jabra SPEAK 510 USB", hidrawPath: "/dev/does-not-exist",
		},
	}, 0, -1)
	listed := (&jabraAPIBridge{}).ListDevices()
	if len(listed) != 1 || listed[0].Name != "Jabra SPEAK 510 USB" {
		t.Fatalf("IPC devices = %#v", listed)
	}
	if listed[0].Firmware != "" {
		t.Fatalf("uncached firmware was fabricated: %q", listed[0].Firmware)
	}
}

func TestDeviceRegistryConcurrentSnapshotsAndUpdates(t *testing.T) {
	headset := &jabra_DeviceInfo{deviceID: 0, productID: 0x24b7, batteryStatus: &batteryStatus{levelInPercent: 48}}
	withDeviceState(t, devices{0: headset}, 0, -1)

	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for range 1000 {
			_ = deviceSnapshots()
			_, _ = selectedHeadsetSnapshot()
		}
	}()
	go func() {
		defer workers.Done()
		for value := range 1000 {
			updateDeviceByID(0, func(device *jabra_DeviceInfo) {
				device.batteryStatus.levelInPercent = uint8(value % 101)
			})
		}
	}()
	workers.Wait()
}

func TestHidrawReadHonorsPollTimeout(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	connection := &hidrawConn{f: reader, path: "test-pipe"}
	started := time.Now()
	if _, err := connection.read(25 * time.Millisecond); err == nil {
		t.Fatal("empty HID pipe did not time out")
	}
	elapsed := time.Since(started)
	if elapsed < 15*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("poll timeout took %s", elapsed)
	}
}

func TestBatteryCapacityRejectsSignalLikeValues(t *testing.T) {
	for _, invalid := range []int{-1, 218, 230, 248} {
		if _, err := validatedBatteryCapacity(invalid); err == nil {
			t.Fatalf("battery capacity %d was accepted", invalid)
		}
	}
	if got, err := validatedBatteryCapacity(48); err != nil || got != 48 {
		t.Fatalf("battery capacity 48 = %d, %v", got, err)
	}
}

func TestAggregateBatteryComponentsUsesLowestValidLevel(t *testing.T) {
	components := []batteryComponentStatus{
		{label: "Left", levelInPercent: 72, component: batteryLeft},
		{label: "Right", levelInPercent: 48, charging: true, component: batteryRight},
	}
	battery := aggregateBatteryComponents(components)
	if battery.levelInPercent != 48 || !battery.charging || battery.component != batteryCombined {
		t.Fatalf("aggregate battery = %#v", battery)
	}
	if len(battery.components) != 2 {
		t.Fatalf("component count = %d", len(battery.components))
	}
}

func TestBatteryComponentLabels(t *testing.T) {
	tests := []struct {
		path      string
		index     int
		total     int
		wantLabel string
		wantType  batteryComponent
	}{
		{"/sys/class/power_supply/headset-left-battery", 0, 2, "Left", batteryLeft},
		{"/sys/class/power_supply/headset-right-battery", 1, 2, "Right", batteryRight},
		{"/sys/class/power_supply/charging-case", 2, 3, "Case", batteryCradle},
		{"/sys/class/power_supply/hid-battery", 0, 1, "Headset", batteryHeadband},
		{"/sys/class/power_supply/hid-battery-a", 1, 2, "Battery 2", batteryHeadband},
	}
	for _, test := range tests {
		label, component := batteryLabelForPath(test.path, test.index, test.total)
		if label != test.wantLabel || component != test.wantType {
			t.Errorf("batteryLabelForPath(%q) = %q/%d, want %q/%d", test.path, label, component, test.wantLabel, test.wantType)
		}
	}
}

func TestBatteryUsesValidatedPowerSupply(t *testing.T) {
	powerSupply := t.TempDir()
	if err := os.WriteFile(powerSupply+"/capacity", []byte("48\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(powerSupply+"/status", []byte("Charging\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	headset := &jabra_DeviceInfo{
		deviceID: 0, productID: 0x24b7, powerSupply: powerSupply,
	}
	withDeviceState(t, devices{0: headset}, 0, -1)

	battery, err := getBatteryStatus(0)
	if err != nil {
		t.Fatal(err)
	}
	if battery.levelInPercent != 48 || !battery.charging {
		t.Fatalf("battery = %#v, want 48%% charging", battery)
	}
	if len(battery.components) != 1 || battery.components[0].levelInPercent != 48 {
		t.Fatalf("battery components = %#v", battery.components)
	}
	if err := os.WriteFile(powerSupply+"/capacity", []byte("51\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(powerSupply+"/status", []byte("Discharging\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refreshSelectedDeviceData()
	updated, exists := selectedHeadsetSnapshot()
	if !exists || updated.batteryStatus == nil || updated.batteryStatus.levelInPercent != 51 || updated.batteryStatus.charging {
		t.Fatalf("refreshed battery = %#v", updated)
	}
}
