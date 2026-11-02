package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func generationUSB(sim *link390Sim, pid uint16, serial string) usbDev {
	return usbDev{sysPath: sim.port, vendorID: jabraVendorID, productID: pid, serial: serial, product: "Jabra Link"}
}
func TestUSBRegistrationCapturesOnlyScopedKnownGeneration(t *testing.T) {
	for _, scenario := range []struct {
		name           string
		pid            uint16
		serial, devnum string
		want           bool
	}{
		{"scoped", 0x2e57, "SERIAL-A", "2", true},
		{"serial mismatch", 0x2e57, "OTHER", "2", false},
		{"broken address", 0x2e57, "SERIAL-A", "broken", false},
		{"non-scoped", 0x24c7, "SERIAL-A", "2", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			sim := newLink390Sim(t, "registration")
			if err := os.WriteFile(filepath.Join(sim.port, "idProduct"), []byte(fmt.Sprintf("%04x", scenario.pid)), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sim.port, "devnum"), []byte(scenario.devnum), 0600); err != nil {
				t.Fatal(err)
			}
			withDeviceState(t, devices{}, -1, -1)
			registered, added := registerUSBDevice(generationUSB(sim, scenario.pid, scenario.serial))
			if !added || (registered.usbGeneration != "") != scenario.want {
				t.Fatal(registered, added)
			}
			registered.firmwareVersion, registered.variantType = "1.3.0", "04-10"
			if !scenario.want && eligibleLink390Reset(registered) {
				t.Fatal("missing generation enabled reset")
			}
			if sim.handles != 0 {
				t.Fatal("registration opened HID")
			}
		})
	}
}
func TestUSBGenerationPublicationRotatesWithInstance(t *testing.T) {
	target := newConfigFlowFake().id
	target.Generation = "G1"
	device := &jabra_DeviceInfo{deviceID: 7, instance: "original", productID: target.ProductID, serialNumber: target.USBSerial, usbDevicePath: target.Port, hidrawPath: target.HIDPath, usbGeneration: "G0", isDongle: true}
	withDeviceState(t, devices{7: cloneDeviceInfo(device)}, -1, 7)
	result := link390ConfigResult{Status: link390Applied, Identity: target}
	verified, err := publishLink390ConfigResult(device, target, result, target, nil)
	if err != nil || verified.Device.instance != "original" || verified.Device.usbGeneration != "G0" {
		t.Fatal("generation refreshed without rotation", verified, err)
	}
	result.Identity.Generation = "G2"
	current := result.Identity
	verified, err = publishLink390ConfigResult(device, target, result, current, nil)
	if err != nil || verified.Device.instance == "original" || verified.Device.usbGeneration != "G2" {
		t.Fatal("rotation was incoherent", verified, err)
	}
}
func TestMissingUSBGenerationRetiresInsteadOfRefreshing(t *testing.T) {
	sim, device := resetNativeFixture(t)
	now := time.Now()
	updateDeviceByID(7, func(current *jabra_DeviceInfo) { current.usbGeneration = ""; current.metadataProbeAt = now })
	snapshot := deviceForID(7)
	if retireSlotMissingGeneration(snapshot, now.Add(time.Second)) {
		t.Fatal("retirement was not throttled")
	}
	owner, err := link390ControlGate.exclusive(context.Background(), sim.port, sim.port)
	if err != nil {
		t.Fatal(err)
	}
	retired := retireSlotMissingGeneration(snapshot, now.Add(usbMetadataRetryInterval))
	owner.release()
	if retired || deviceForID(7) == nil {
		t.Fatal("busy slot retired")
	}
	if err := os.WriteFile(filepath.Join(sim.port, "devnum"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if retireSlotMissingGeneration(snapshot, now.Add(usbMetadataRetryInterval)) {
		t.Fatal("failed capture retired slot")
	}
	if err := os.WriteFile(filepath.Join(sim.port, "devnum"), []byte("2"), 0600); err != nil {
		t.Fatal(err)
	}
	if !retireSlotMissingGeneration(snapshot, now.Add(usbMetadataRetryInterval)) || deviceForID(7) != nil || deviceForID(8) != nil {
		t.Fatal("recoverable missing-generation slot retained")
	}
	registered, added := registerUSBDevice(generationUSB(sim, 0x2e57, "SERIAL-A"))
	if !added || registered.instance == device.instance || registered.usbGeneration == "" {
		t.Fatal("new registration missing its binding", registered, added)
	}
	if snapshot.usbGeneration != "" {
		t.Fatal("old snapshot was filled in place")
	}
	legacy := metadataTestDongle()
	withDeviceState(t, devices{0: legacy}, -1, 0)
	if retireSlotMissingGeneration(legacy, now.Add(time.Hour)) || deviceForID(0) == nil {
		t.Fatal("legacy slot retired")
	}
}
