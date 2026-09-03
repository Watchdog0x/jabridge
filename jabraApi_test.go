package main

import (
	"bytes"
	"errors"
	"testing"
)

func withDeviceState(t *testing.T, manager devices, headset, dongle int) {
	t.Helper()
	oldManager := deviceManager
	oldHeadset := selectedHeadset
	oldDongle := selectedDongle
	oldStartMenu := startMenu
	oldDongleMenu := dongleSettignsMenu
	deviceManager = manager
	selectedHeadset = headset
	selectedDongle = dongle
	t.Cleanup(func() {
		deviceManager = oldManager
		selectedHeadset = oldHeadset
		selectedDongle = oldDongle
		startMenu = oldStartMenu
		dongleSettignsMenu = oldDongleMenu
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
	if len(startMenu) != 2 {
		t.Fatalf("dongle-only menu has %d items, want settings and exit", len(startMenu))
	}
	if startMenu[0].id != 2 || startMenu[1].id != 5 {
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
	device, err := parsePairingRecord(name, record)
	if err != nil {
		t.Fatal(err)
	}
	if !device.isConnected || device.deviceBTAddr != [6]byte{1, 2, 3, 4, 5, 6} {
		t.Fatalf("pairing record parsed incorrectly: %#v", device)
	}
	if _, err := parsePairingRecord(name, record[:11]); err == nil {
		t.Fatal("truncated pairing record was accepted")
	}
}
