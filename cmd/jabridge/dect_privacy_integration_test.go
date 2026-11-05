package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	firmwaretool "github.com/Watchdog0x/jabridge/internal/firmware"
	"github.com/Watchdog0x/jabridge/internal/history"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

// The firmware test independently checks this fixture against an actual
// inspection. Here that exact public result traverses the real service bridge,
// RPC encoder/decoder, full debug renderer and enabled history recorder.
func TestDECTRealResultPrivacyThroughRPCDebugAndHistory(t *testing.T) {
	if os.Getenv("JABRIDGE_DECT_PRIVACY_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDECTRealResultPrivacyThroughRPCDebugAndHistory$", "-test.v")
		cmd.Env = append(os.Environ(), "JABRIDGE_DECT_PRIVACY_CHILD=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated privacy test failed: %v\n%s", err, output)
		}
		return
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("JABRIDGE_HISTORY", "on")
	history.Configure("1.6.2-issue62.1")
	history.Record(history.Event{Component: "app", Action: "run", Phase: "start", Command: "debug"})
	d := dectTestRegistry(t)
	updateDeviceByID(d.deviceID, func(current *jabra_DeviceInfo) {
		current.firmwareVersion = "4.4.1"
		current.variantType = "03-04"
		current.deviceName = "PRIVATE_DEVICE_NAME"
	})
	oldCache := deviceModelCache
	t.Cleanup(func() { deviceModelCache = oldCache })
	deviceModelCache = map[string]modelCacheEntry{"1113:03-04:4.4.1": {capabilities: &modelcatalog.Capabilities{Firmware: "4.4.1", DeviceFirmware: "4.4.1", Properties: map[string]modelcatalog.Property{}}}}
	data, err := os.ReadFile("../../internal/firmware/testdata/dect-component-public-result.json")
	if err != nil {
		t.Fatal(err)
	}
	var actual firmwaretool.DECTComponentReport
	if err := json.Unmarshal(data, &actual); err != nil {
		t.Fatal(err)
	}
	old := inspectDECTComponents
	t.Cleanup(func() { inspectDECTComponents = old })
	calls := 0
	inspectDECTComponents = func(context.Context, *firmwaretool.ControlBinding) (firmwaretool.DECTComponentReport, error) {
		calls++
		return actual, nil
	}
	backend := newTUIServiceTestBackend(t, &jabraAPIBridge{})
	var debug bytes.Buffer
	writeNativeDiagnostic(&debug, backend.client)
	if calls != 1 || !strings.Contains(debug.String(), "one installer identity attempt would pass") || strings.Contains(debug.String(), "PRIVATE") {
		t.Fatal("real result was lost or leaked", calls, debug.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var result []ipc.DiagnosticCheck
	if err := backend.client.Call(ctx, "diagnostics.dect_components", map[string]uint16{"id": 99}, &result); err == nil {
		t.Fatal("error history control did not fail")
	}
	directory, err := history.Directory()
	if err != nil {
		t.Fatal(err)
	}
	var recorded strings.Builder
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range files {
		if !entry.IsDir() {
			data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			recorded.Write(data)
		}
	}
	if !strings.Contains(recorded.String(), `"method":"other"`) || !strings.Contains(recorded.String(), `"rpcCode":-32603`) || strings.Contains(recorded.String(), "PRIVATE") {
		t.Fatal("history control missing or private values leaked", recorded.String())
	}
}
