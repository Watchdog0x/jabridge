package main

// This fixture also compiles on base a5fb525. It records the existing canonical
// packets and writable results without depending on the new component API.
import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
	"golang.org/x/sys/unix"
)

type dectBackgroundFixture struct {
	mu                 sync.Mutex
	packets            [][]byte
	failDisplayVersion bool
	handlers           sync.WaitGroup
}

func withDECTBackgroundFixture(t *testing.T) *dectBackgroundFixture {
	t.Helper()
	port := filepath.Join(t.TempDir(), "usb")
	if err := os.Mkdir(port, 0700); err != nil {
		t.Fatal(err)
	}
	for n, v := range map[string]string{"idVendor": "0b0e", "idProduct": "1113", "busnum": "1", "devnum": "2", "serial": "PRIVATE_USB_SERIAL"} {
		if err := os.WriteFile(filepath.Join(port, n), []byte(v), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixture := &dectBackgroundFixture{}
	oldParent, oldOpen, oldFind := controlUSBParent, openHidrawRaw, findHidrawPathsForDevice
	oldCache := deviceModelCache
	oldSeq := gnpSeqCounter
	gnpSeqCounter = 0x20
	deviceModelCache = map[string]modelCacheEntry{"1113:03-04:4.4.1": {capabilities: &modelcatalog.Capabilities{Firmware: "4.4.1", DeviceFirmware: "4.4.1", Properties: map[string]modelcatalog.Property{}}}, "1113:03-04:5.20.1": {capabilities: &modelcatalog.Capabilities{Firmware: "5.20.1", DeviceFirmware: "5.20.1", Properties: map[string]modelcatalog.Property{}}}}
	t.Cleanup(func() {
		fixture.handlers.Wait()
		controlUSBParent, openHidrawRaw, findHidrawPathsForDevice = oldParent, oldOpen, oldFind
		deviceModelCache = oldCache
		gnpSeqCounter = oldSeq
	})
	controlUSBParent = func(string) (string, error) { return port, nil }
	findHidrawPathsForDevice = func(*jabra_DeviceInfo) []string { return []string{"fixture"} }
	openHidrawRaw = func(path string, _ *controlPermit) (*hidrawConn, error) {
		sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_NONBLOCK, 0)
		if err != nil {
			return nil, err
		}
		host := os.NewFile(uintptr(sockets[0]), "host-fixture")
		remoteFile := os.NewFile(uintptr(sockets[1]), "device-fixture")
		remote, err := net.FileConn(remoteFile)
		_ = remoteFile.Close()
		if err != nil {
			_ = host.Close()
			return nil, err
		}
		fixture.handlers.Add(1)
		go func() {
			defer fixture.handlers.Done()
			defer func() { _ = remote.Close() }()
			for {
				request := make([]byte, 64)
				if _, err := io.ReadFull(remote, request); err != nil {
					return
				}
				fixture.mu.Lock()
				fixture.packets = append(fixture.packets, append([]byte(nil), request...))
				failDisplay := fixture.failDisplayVersion
				fixture.mu.Unlock()
				address, class, op := request[1], request[5], request[6]
				var payload []byte
				if class == 2 && address == 3 {
					switch op {
					case 2:
						payload = []byte{2, 3, 4}
					case 3:
						if !failDisplay {
							payload = append([]byte{5}, []byte("4.4.1")...)
						}
					}
				}
				if class == 2 && address == 1 && op == 3 {
					payload = append([]byte{6}, []byte("5.20.1")...)
				}
				reply := make([]byte, 64)
				copy(reply, []byte{5, 0, address, request[3], 0xc5, 0xfe})
				if payload != nil {
					copy(reply, []byte{5, 0, address, request[3], 0xc0 | byte(6+len(payload)), class, op})
					copy(reply[7:], payload)
				}
				if _, err := remote.Write(reply); err != nil {
					return
				}
			}
		}()
		return &hidrawConn{f: host, path: path, reportSize: 64}, nil
	}
	d := &jabra_DeviceInfo{deviceID: 0, instance: "fixture", vendorID: jabraVendorID, productID: 0x1113, deviceName: "Engage 75", deviceConnection: deviceConnectionType_USB, usbDevicePath: port, hidrawPath: "fixture", serialNumber: "PRIVATE_USB_SERIAL", featureFlags: &featureFlags{}}
	withDeviceState(t, devices{0: d}, -1, 0)
	return fixture
}
func (f *dectBackgroundFixture) transcript() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.packets {
		out = append(out, base64.StdEncoding.EncodeToString(p))
	}
	return out
}
func TestDECTIdleAndDiagnosticPacketCharacterization(t *testing.T) {
	fixture := withDECTBackgroundFixture(t)
	updateDeviceByID(0, func(d *jabra_DeviceInfo) {
		d.gnpDestination = 3
		d.gnpDestinationKnown = true
		d.variantType = "03-04"
		d.firmwareVersion = "4.4.1"
	})
	d := deviceForID(0)
	refreshKnownUSBMetadata(usbDev{sysPath: d.usbDevicePath, vendorID: d.vendorID, productID: d.productID}, time.Now())
	if len(fixture.transcript()) != 0 {
		t.Fatal("complete idle metadata unexpectedly probed")
	}
	_, _ = readGNPBatteryStatus(d)
	d = deviceForID(0)
	if d.gnpDestination != 3 || d.variantType != "03-04" || d.firmwareVersion != "4.4.1" {
		t.Fatal("primary discovery changed", d.gnpDestination, d.variantType, d.firmwareVersion)
	}
	var writable []string
	for _, value := range readBoolSettingsSnapshot(d, settingScopeHeadset, nil, os.ErrNotExist, func(*jabra_DeviceInfo, boolSettingDefinition) (bool, error) { return false, nil }) {
		writable = append(writable, value.Definition.Key+":"+map[bool]string{true: "writable", false: "read-only"}[value.Editable])
	}
	if _, err := (&jabraAPIBridge{}).DiagnoseDevice(0); err != nil {
		t.Fatal(err)
	}
	actual := struct{ Packets, Writable []string }{fixture.transcript(), writable}
	encoded, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("CHARACTERIZATION=" + base64.StdEncoding.EncodeToString(encoded))
	path := "testdata/dect-background-characterization.json"
	expected, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Fatal("required baseline characterization fixture is missing")
	}
	if err != nil {
		t.Fatal(err)
	}
	var want struct{ Packets, Writable []string }
	if err := json.Unmarshal(expected, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatal("idle/diagnostic packets or writable results changed", actual, want)
	}
}

func TestDECTFallbackFirmwareIsNotAttributedToConfiguredDisplay(t *testing.T) {
	fixture := withDECTBackgroundFixture(t)
	fixture.failDisplayVersion = true
	updateDeviceByID(0, func(d *jabra_DeviceInfo) {
		d.gnpDestination = 3
		d.gnpDestinationKnown = true
		d.variantType = "03-04"
		d.firmwareVersion = "4.4.1"
	})
	checks, err := (&jabraAPIBridge{}).DiagnoseDevice(0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range checks {
		if check.Feature == "native installed firmware" {
			found = true
			if !strings.HasPrefix(check.Detail, "5.20.1") || !strings.Contains(check.Detail, "configured endpoint") || !strings.Contains(check.Detail, "may use a fallback address") {
				t.Fatal("fallback version misattributed", check)
			}
		}
	}
	if !found {
		t.Fatal("firmware observation missing")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.packets) < 3 {
		t.Fatal("fallback not exercised")
	}
	for i, address := range []byte{3, 8, 1} {
		p := fixture.packets[i]
		if p[1] != address || p[5] != 2 || p[6] != 3 {
			t.Fatal("existing fallback packet order changed", i, p[:7])
		}
	}
}
