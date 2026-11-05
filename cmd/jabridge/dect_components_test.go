package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	firmwaretool "github.com/Watchdog0x/jabridge/internal/firmware"
)

func dectTestRegistry(t *testing.T) *jabra_DeviceInfo {
	t.Helper()
	port := filepath.Join(t.TempDir(), "usb")
	if err := os.Mkdir(port, 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"idVendor": "0b0e", "idProduct": "1113", "busnum": "1", "devnum": "2", "serial": "PRIVATE_USB_SERIAL"} {
		if err := os.WriteFile(filepath.Join(port, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	d := &jabra_DeviceInfo{deviceID: 0, instance: "registered", vendorID: jabraVendorID, productID: 0x1113, deviceConnection: deviceConnectionType_USB, usbDevicePath: port, hidrawPath: filepath.Join(port, "missing-hidraw"), serialNumber: "PRIVATE_USB_SERIAL", gnpDestination: 3, gnpDestinationKnown: true, featureFlags: &featureFlags{}}
	withDeviceState(t, devices{0: d}, -1, 0)
	return deviceForID(0)
}
func TestDECTComponentServicePinsIdentityAndDiscardsChanges(t *testing.T) {
	for _, change := range []string{"none", "instance", "serial", "generation", "transient"} {
		t.Run(change, func(t *testing.T) {
			d := dectTestRegistry(t)
			old := inspectDECTComponents
			t.Cleanup(func() { inspectDECTComponents = old })
			called := 0
			inspectDECTComponents = func(_ context.Context, b *firmwaretool.ControlBinding) (firmwaretool.DECTComponentReport, error) {
				called++
				if !controlDeviceBusy(d) || !b.MatchesSerial(d.serialNumber) {
					t.Error("inspection has no bound exclusive ownership")
				}
				switch change {
				case "instance":
					updateDeviceByID(0, func(current *jabra_DeviceInfo) { current.instance = "replacement" })
				case "serial":
					if err := os.WriteFile(filepath.Join(d.usbDevicePath, "serial"), []byte("REPLACEMENT"), 0600); err != nil {
						t.Fatal(err)
					}
				case "generation":
					if err := os.WriteFile(filepath.Join(d.usbDevicePath, "devnum"), []byte("3"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if change == "transient" {
					return firmwaretool.DECTComponentReport{}, firmwaretool.ErrDECTInspectionChanged
				}
				return firmwaretool.DECTComponentReport{IdentityPassed: true, InstallerIdentity: "one installer identity attempt would pass with these replies"}, nil
			}
			checks, err := (&jabraAPIBridge{}).DECTComponents(0)
			if err != nil || called != 1 {
				t.Fatal(checks, err, called)
			}
			text := fmtChecks(checks)
			if strings.Contains(text, "PRIVATE") {
				t.Fatal("serial leaked", text)
			}
			if change != "none" && (!strings.Contains(text, "device changed") || strings.Contains(text, "would pass")) {
				t.Fatal("stale verdict retained", text)
			}
			if change == "none" && !strings.Contains(text, "would pass") {
				t.Fatal("fresh verdict missing", text)
			}
			if controlDeviceBusy(d) {
				t.Fatal("inspection leaked ownership")
			}
		})
	}
}
func fmtChecks(checks []ipc.DiagnosticCheck) string {
	var out strings.Builder
	for _, c := range checks {
		out.WriteString(c.Feature + " " + c.State + " " + c.Detail + "\n")
	}
	return out.String()
}

func TestDECTComponentServiceRefusesUnsupportedBeforeIO(t *testing.T) {
	old := inspectDECTComponents
	t.Cleanup(func() { inspectDECTComponents = old })
	calls := 0
	inspectDECTComponents = func(context.Context, *firmwaretool.ControlBinding) (firmwaretool.DECTComponentReport, error) {
		calls++
		return firmwaretool.DECTComponentReport{}, nil
	}
	for _, tc := range []struct {
		pid              uint16
		dongle, wireless bool
	}{{0x1111, false, false}, {0x2e57, true, false}, {0x1113, true, false}, {0x1113, false, true}} {
		d := &jabra_DeviceInfo{deviceID: 0, instance: "fixture", vendorID: jabraVendorID, productID: tc.pid, isDongle: tc.dongle, deviceConnection: deviceConnectionType_USB}
		if tc.wireless {
			d.deviceConnection = deviceConnectionType_BT
		}
		withDeviceState(t, devices{0: d}, -1, 0)
		if _, err := (&jabraAPIBridge{}).DECTComponents(0); err == nil {
			t.Fatal("unsupported inspection accepted", tc)
		}
	}
	if calls != 0 {
		t.Fatal("unsupported target reached IO", calls)
	}
}

func TestDECTComponentLeaseDrainsExistingHandle(t *testing.T) {
	d := dectTestRegistry(t)
	h, err := openHidrawCoordinated("synthetic", nil, func(string) (string, error) { return d.usbDevicePath, nil }, func(string) (*hidrawConn, error) { return &hidrawConn{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	old := inspectDECTComponents
	t.Cleanup(func() { inspectDECTComponents = old })
	called := 0
	inspectDECTComponents = func(context.Context, *firmwaretool.ControlBinding) (firmwaretool.DECTComponentReport, error) {
		called++
		return firmwaretool.DECTComponentReport{}, nil
	}
	start := time.Now()
	checks, err := (&jabraAPIBridge{}).DECTComponents(0)
	if err != nil || called != 0 || !strings.Contains(fmtChecks(checks), "BUSY") || time.Since(start) > 6*time.Second {
		t.Fatal("reader was not drained safely", checks, err, called, time.Since(start))
	}
}

func TestDECTInspectionOwnershipBlocksOnlyItsPort(t *testing.T) {
	d := dectTestRegistry(t)
	owner, err := link390ControlGate.exclusive(context.Background(), d.usbDevicePath, d.usbDevicePath)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if _, err := readGNPBatteryStatus(d); !errors.Is(err, errControlBusy) {
		t.Fatal("battery bypassed inspection", err)
	}
	oldFind := findHidrawPathsForDevice
	t.Cleanup(func() { findHidrawPathsForDevice = oldFind })
	calls := 0
	findHidrawPathsForDevice = func(*jabra_DeviceInfo) []string { calls++; return nil }
	enrichUSBDevice(d)
	if calls != 0 {
		t.Fatal("enrichment bypassed inspection")
	}
	other := t.TempDir()
	for name, value := range map[string]string{"idVendor": "0b0e", "idProduct": "24c7", "busnum": "1", "devnum": "9"} {
		if err := os.WriteFile(filepath.Join(other, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	opened := false
	h, err := openHidrawCoordinated("other", nil, func(string) (string, error) { return other, nil }, func(string) (*hidrawConn, error) { opened = true; return &hidrawConn{}, nil })
	if err != nil || !opened {
		t.Fatal("unrelated port blocked", err)
	}
	h.close()
}

func TestDECTPortScopeDoesNotGrantOtherPermissions(t *testing.T) {
	for _, pid := range []uint16{0x1110, 0x1112, 0x1113, 0x111d, 0x111e, 0x111f, 0x1120, 0x1140, 0x1141, 0x1142, 0x114a, 0x114b, 0x114c, 0x114d} {
		if !controlPortScopedPID(pid) || scopedControlPID(pid) || supportsExperimentalDongleWrites(pid) || supportsValidatedFactoryReset(pid) || supportsValidatedPairingReads(pid) {
			t.Fatal("inspection scope changed another permission", pid)
		}
	}
	for _, pid := range []uint16{0x1111, 0x4052, 0x24c7, 0xffff} {
		if controlPortScopedPID(pid) {
			t.Fatal("unrelated port scope changed", pid)
		}
	}
}

type dectReportAPI struct {
	jabraAPIBridge
	calls int
}

func (a *dectReportAPI) DECTComponents(uint16) ([]ipc.DiagnosticCheck, error) {
	a.calls++
	return []ipc.DiagnosticCheck{{Feature: "DECT installer identity", State: "INFO", Detail: "one installer identity attempt would pass with these replies"}}, nil
}
func TestDECTDebugUsesMatchingCandidateService(t *testing.T) {
	api := &dectReportAPI{}
	backend := newTUIServiceTestBackend(t, api)
	var out bytes.Buffer
	writeDECTComponentDiagnostic(context.Background(), &out, backend.client, ipc.DeviceInfo{ID: 0, PID: 0x1113, Connection: "usb"}, true)
	if api.calls != 1 || !strings.Contains(out.String(), "DECT components (read-only") || !strings.Contains(out.String(), "Bootloader, erase and transfer steps were not started") {
		t.Fatal(api.calls, out.String())
	}
	out.Reset()
	writeDECTComponentDiagnostic(context.Background(), &out, backend.client, ipc.DeviceInfo{PID: 0x1113}, false)
	if api.calls != 1 || !strings.Contains(out.String(), "needs the candidate service") {
		t.Fatal("version mismatch was not explicit", api.calls, out.String())
	}
	out.Reset()
	writeDECTComponentDiagnostic(context.Background(), &out, backend.client, ipc.DeviceInfo{PID: 0x24c7}, true)
	if api.calls != 1 || out.Len() != 0 {
		t.Fatal("unrelated device got a component check")
	}
}

type dectOldServiceAPI struct{ ipc.API }

func TestDECTDebugOldServiceIsExplicitlyNotTested(t *testing.T) {
	backend := newTUIServiceTestBackend(t, &dectOldServiceAPI{})
	var out bytes.Buffer
	writeDECTComponentDiagnostic(context.Background(), &out, backend.client, ipc.DeviceInfo{ID: 0, PID: 0x1113, Connection: "usb"}, true)
	if !strings.Contains(out.String(), "needs the candidate service") || strings.Contains(out.String(), "would pass") {
		t.Fatal(out.String())
	}
}
func TestDECTPrimaryDiscoveryRemainsDeferred(t *testing.T) {
	d := &jabra_DeviceInfo{productID: 0x1113, deviceConnection: deviceConnectionType_USB}
	var seen []byte
	_, address, ok := firstResponsiveGNPEndpoint([]string{"fixture"}, firmwareReadDestinations(d), func(_ string, a byte) bool { seen = append(seen, a); return a == 3 })
	if !ok || address != 3 || len(seen) != 2 || seen[0] != 8 || seen[1] != 3 {
		t.Fatal("stage1 changed primary discovery", address, seen)
	}
}
func TestDECTPortClassUsesExistingRuntimeMap(t *testing.T) {
	port := t.TempDir()
	if err := os.WriteFile(filepath.Join(port, "idVendor"), []byte("0b0e"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pid   string
		scope bool
	}{{"1110", true}, {"1112", true}, {"1113", true}, {"111d", true}, {"111e", true}, {"111f", true}, {"1120", true}, {"1140", true}, {"1141", true}, {"1142", true}, {"114a", true}, {"114b", true}, {"114c", true}, {"114d", true}, {"1111", false}, {"24c7", false}, {"4052", false}, {"2e57", true}} {
		if err := os.WriteFile(filepath.Join(port, "idProduct"), []byte(tc.pid), 0600); err != nil {
			t.Fatal(err)
		}
		scoped, known := controlPortClass(port)
		if !known || scoped != tc.scope {
			t.Fatal(tc.pid, scoped, known)
		}
	}
}

func TestDECTComponentCachedSerialMismatchDoesNotInspect(t *testing.T) {
	d := dectTestRegistry(t)
	updateDeviceByID(d.deviceID, func(current *jabra_DeviceInfo) { current.serialNumber = "STALE_PRIVATE_SERIAL" })
	old := inspectDECTComponents
	t.Cleanup(func() { inspectDECTComponents = old })
	calls := 0
	inspectDECTComponents = func(context.Context, *firmwaretool.ControlBinding) (firmwaretool.DECTComponentReport, error) {
		calls++
		return firmwaretool.DECTComponentReport{}, nil
	}
	checks, err := (&jabraAPIBridge{}).DECTComponents(0)
	if err != nil || calls != 0 || !strings.Contains(fmtChecks(checks), "device changed") || strings.Contains(fmtChecks(checks), "PRIVATE") {
		t.Fatal(checks, err, calls)
	}
}

func TestDECTDebugHonorsParentCancellation(t *testing.T) {
	api := &dectReportAPI{}
	backend := newTUIServiceTestBackend(t, api)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	writeDECTComponentDiagnostic(ctx, &out, backend.client, ipc.DeviceInfo{PID: 0x1113}, true)
	if api.calls != 0 || !strings.Contains(out.String(), "time budget ended") {
		t.Fatal(api.calls, out.String())
	}
}
