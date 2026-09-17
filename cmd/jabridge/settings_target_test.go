package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/headsetvolume"
)

type failedDeviceEntropy struct{}

func (failedDeviceEntropy) Read([]byte) (int, error) {
	return 0, errors.New("synthetic device identity entropy failure")
}

func TestDeviceIdentityEntropyFailureStopsRegistration(t *testing.T) {
	if os.Getenv("JABRIDGE_TEST_IDENTITY_ENTROPY_CHILD") == "1" {
		rand.Reader = failedDeviceEntropy{}
		addDevice(&jabra_DeviceInfo{})
		fmt.Println("UNEXPECTED_DEVICE_REGISTERED")
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestDeviceIdentityEntropyFailureStopsRegistration$")
	cmd.Env = append(os.Environ(), "JABRIDGE_TEST_IDENTITY_ENTROPY_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err == nil || strings.Contains(string(output), "UNEXPECTED_DEVICE_REGISTERED") {
		t.Fatal("entropy failure registered a device", string(output), err)
	}
	if !strings.Contains(string(output), "crypto/rand: failed to read random data") && !strings.Contains(string(output), "cannot create device attachment identity") {
		t.Fatal("child failed for an unrelated reason", string(output), err)
	}
}

func TestMissingDeviceIdentityBlocksOperations(t *testing.T) {
	for _, device := range []*jabra_DeviceInfo{nil, {deviceID: 2}} {
		if err := currentSettingDevice(device); err == nil {
			t.Error("missing device identity bypassed the shared operation check")
		}
	}
	device := &jabra_DeviceInfo{deviceID: 2, productID: headsetvolume.ProductID, firmwareVersion: headsetvolume.Firmware, deviceConnection: deviceConnectionType_USB}
	withDeviceState(t, devices{2: device}, 2, -1)
	old := openHeadsetVolume
	t.Cleanup(func() { openHeadsetVolume = old })
	calls := 0
	openHeadsetVolume = func(context.Context, *headsetvolume.Attachment, uint16, string) (*headsetvolume.USB, error) {
		calls++
		t.Fatal("missing identity reached device access")
		return nil, nil
	}
	api := &jabraAPIBridge{}
	for _, target := range []ipc.SettingTarget{{ID: 2}, {ID: 2, Instance: strings.Repeat("a", 32)}} {
		if _, err := api.GetHeadsetVolume(target); err == nil {
			t.Error("missing identity accepted for a volume read")
		}
		if _, err := api.SetHeadsetVolume(target, 50); err == nil {
			t.Error("missing identity accepted for a volume write")
		}
	}
	if calls != 0 {
		t.Fatal("device access occurred", calls)
	}
}
