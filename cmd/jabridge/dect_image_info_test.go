package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	firmwaretool "github.com/Watchdog0x/jabridge/internal/firmware"
)

func TestDECTImageInfoServiceBindingAndPrivacy(t *testing.T) {
	for _, change := range []string{"none", "instance", "serial", "generation", "transient"} {
		t.Run(change, func(t *testing.T) {
			d := dectTestRegistry(t)
			old := inspectDECTImageInfo
			t.Cleanup(func() { inspectDECTImageInfo = old })
			calls := 0
			inspectDECTImageInfo = func(_ context.Context, binding *firmwaretool.ControlBinding) ([]firmwaretool.DECTComponentCheck, error) {
				calls++
				if !controlDeviceBusy(d) || !binding.MatchesSerial(d.serialNumber) {
					t.Error("information read has no pinned ownership")
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
				case "transient":
					return nil, firmwaretool.ErrDECTInspectionChanged
				}
				return []firmwaretool.DECTComponentCheck{{Role: "headset", Address: 10, Query: "runtime GetInfo", State: "INFO", Detail: "measured image ID fixture"}}, nil
			}
			checks, err := (&jabraAPIBridge{}).DECTImageInfo(context.Background(), 0)
			if err != nil || calls != 1 {
				t.Fatal(checks, err, calls)
			}
			text := checks[0].Detail
			if change == "none" {
				if len(checks) != 2 || !strings.Contains(checks[1].Detail, "measured image ID") || strings.Contains(text, "PRIVATE") {
					t.Fatal("observation missing or private identity leaked", checks)
				}
			} else if len(checks) != 1 || checks[0].State != "NOT TESTED" || !strings.Contains(text, "changed") {
				t.Fatal("stale observation escaped", checks)
			}
		})
	}
}

func TestDECTImageInfoClientRequiresMatchingServiceAndBudget(t *testing.T) {
	d := dectTestRegistry(t)
	old := inspectDECTImageInfo
	t.Cleanup(func() { inspectDECTImageInfo = old })
	calls := 0
	inspectDECTImageInfo = func(context.Context, *firmwaretool.ControlBinding) ([]firmwaretool.DECTComponentCheck, error) {
		calls++
		return []firmwaretool.DECTComponentCheck{{Role: "base", Address: 1, State: "INFO", Query: "runtime GetInfo", Detail: "observation only"}}, nil
	}
	backend := newTUIServiceTestBackend(t, &jabraAPIBridge{})
	device := ipc.DeviceInfo{ID: d.deviceID, PID: d.productID, Connection: "usb"}
	for _, mode := range []string{"mismatch", "cancelled", "supported"} {
		ctx, cancel := context.WithCancel(context.Background())
		if mode == "cancelled" {
			cancel()
		}
		var out bytes.Buffer
		writeDECTImageInfoDiagnostic(ctx, &out, backend.client, device, mode != "mismatch")
		cancel()
		if mode == "supported" {
			if calls != 1 || !strings.Contains(out.String(), "observation only") || !strings.Contains(out.String(), "No restart or firmware update was requested") {
				t.Fatal(out.String(), calls)
			}
		} else if calls != 0 || !strings.Contains(out.String(), "NOT TESTED") {
			t.Fatal("unavailable check sent requests", mode, out.String(), calls)
		}
	}
}

func TestDECTImageInfoOldServiceAndUnsupportedNoIO(t *testing.T) {
	backend := newTUIServiceTestBackend(t, &dectOldServiceAPI{})
	var out bytes.Buffer
	writeDECTImageInfoDiagnostic(context.Background(), &out, backend.client, ipc.DeviceInfo{PID: 0x1113}, true)
	if !strings.Contains(out.String(), "NOT TESTED") || strings.Contains(out.String(), "measured runtime") {
		t.Fatal(out.String())
	}
	dectTestRegistry(t)
	old := inspectDECTImageInfo
	t.Cleanup(func() { inspectDECTImageInfo = old })
	calls := 0
	inspectDECTImageInfo = func(context.Context, *firmwaretool.ControlBinding) ([]firmwaretool.DECTComponentCheck, error) {
		calls++
		return nil, nil
	}
	updateDeviceByID(0, func(d *jabra_DeviceInfo) { d.productID = 0x1110 })
	if _, err := (&jabraAPIBridge{}).DECTImageInfo(context.Background(), 0); err == nil || calls != 0 {
		t.Fatal("unqualified variant queried", err, calls)
	}
	updateDeviceByID(0, func(d *jabra_DeviceInfo) { d.productID = 0x1113; d.isDongle = true })
	if _, err := (&jabraAPIBridge{}).DECTImageInfo(context.Background(), 0); err == nil || calls != 0 {
		t.Fatal("dongle queried", err, calls)
	}
}
