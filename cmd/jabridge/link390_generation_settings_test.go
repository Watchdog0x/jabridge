package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func preferredGenerationSetting(t *testing.T) choiceSettingValue {
	t.Helper()
	for _, definition := range dongleChoiceSettingDefinitions {
		if definition.Key == "preferred-connection-mode" {
			definition.ExpectedFirmware = "3.2.0"
			return choiceSettingValue{Definition: definition, ChoiceIndex: 0, Raw: 1, Editable: true}
		}
	}
	t.Fatal("missing preferred connection definition")
	return choiceSettingValue{}
}
func TestLink390ConfigRejectsCachedGenerationMismatch(t *testing.T) {
	for _, key := range []string{"wireless-range", "softphone-integration"} {
		t.Run(key, func(t *testing.T) {
			sim := newLink390Sim(t, "stale-config")
			installLink390Sim(t, sim)
			device := link390SimRegistry(t, sim)
			sim.restart(sim.hid)
			definition, _ := link390ConfigDefinitionForKey(key)
			wanted := byte(2)
			if key == "softphone-integration" {
				wanted = 0
			}
			result, err := changeLink390Config(device, definition, wanted, nil)
			if !errors.Is(err, errLink390Changed) || result.Result.Status != link390NotApplied || result.Result.Attempted || sim.handles != 0 || sim.list() != "" || deviceForID(7) != nil || deviceForID(8) != nil {
				t.Fatal("stale config target reached the device", result.Result, err, sim.handles, sim.list())
			}
			assertLink390Idle(t)
		})
	}
}
func TestLink390ScalarRejectsReplugBeforeOpen(t *testing.T) {
	sim := newLink390Sim(t, "stale-scalar")
	installLink390Sim(t, sim)
	device := link390SimRegistry(t, sim)
	sim.restart(sim.hid)
	err := writeChoiceSetting(device, preferredGenerationSetting(t), 1)
	if !errors.Is(err, errLink390Changed) || sim.handles != 0 || sim.list() != "" || deviceForID(7) != nil || deviceForID(8) != nil {
		t.Fatal("stale scalar reached the device", err, sim.handles, sim.list())
	}
	assertLink390Idle(t)
}

func TestLink390ConfigRefusesMissingCachedGeneration(t *testing.T) {
	sim := newLink390Sim(t, "missing-config-generation")
	installLink390Sim(t, sim)
	link390SimRegistry(t, sim)
	updateDeviceByID(7, func(device *jabra_DeviceInfo) { device.usbGeneration = "" })
	definition, _ := link390ConfigDefinitionForKey("wireless-range")
	result, err := changeLink390Config(deviceForID(7), definition, 2, nil)
	if !errors.Is(err, errLink390Changed) || result.Result.Attempted || result.Result.Status != link390NotApplied || sim.handles != 0 || sim.list() != "" || deviceForID(7) != nil || deviceForID(8) != nil {
		t.Fatal(result.Result, err, sim.handles, sim.list())
	}
	assertLink390Idle(t)
}
func TestLink390ScalarHeldCheckRefusesReplugAfterEarlyCheck(t *testing.T) {
	sim := newLink390Sim(t, "held-scalar")
	installLink390Sim(t, sim)
	device := link390SimRegistry(t, sim)
	locate := controlUSBParent
	bumped := false
	controlUSBParent = func(path string) (string, error) {
		if !bumped {
			bumped = true
			sim.advanceGeneration()
		}
		return locate(path)
	}
	t.Cleanup(func() { controlUSBParent = locate })
	err := writeChoiceSetting(device, preferredGenerationSetting(t), 1)
	if !errors.Is(err, errLink390Changed) || sim.handles == 0 || sim.list() != "" || deviceForID(7) != nil || deviceForID(8) != nil {
		t.Fatal("held guard missed replug", err, sim.handles, sim.list())
	}
	assertLink390Idle(t)
}
func TestLink390ScalarPostACKChangeFailsVerificationWithoutReplay(t *testing.T) {
	sim := newLink390Sim(t, "scalar-after-ack")
	installLink390Sim(t, sim)
	device := link390SimRegistry(t, sim)
	sim.onRead = func(n int) {
		if n == 1 {
			sim.advanceGeneration()
		}
	}
	err := writeChoiceSetting(device, preferredGenerationSetting(t), 1)
	if !errors.Is(err, errLink390Changed) || !strings.Contains(err.Error(), "verify") || strings.Count(sim.list(), "write:") != 1 || strings.Contains(sim.list(), "end@") || deviceForID(7) != nil {
		t.Fatal("post-ACK change replayed or passed", err, sim.list())
	}
	assertLink390Idle(t)
}
func TestLink390ScalarMatchKeepsBinding(t *testing.T) {
	sim := newLink390Sim(t, "scalar-match")
	installLink390Sim(t, sim)
	device := link390SimRegistry(t, sim)
	if err := writeChoiceSetting(device, preferredGenerationSetting(t), 1); err != nil {
		t.Fatal(err)
	}
	current := deviceForID(7)
	if current == nil || current.instance != device.instance || current.usbGeneration != device.usbGeneration || strings.Count(sim.list(), "write:") != 1 {
		t.Fatal(current, sim.list())
	}
	assertLink390Idle(t)
}
func TestLink390ScalarMissingPortKeepsRegistry(t *testing.T) {
	sim := newLink390Sim(t, "scalar-missing-port")
	installLink390Sim(t, sim)
	device := link390SimRegistry(t, sim)
	if err := os.Rename(sim.port, sim.port+".gone"); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Rename(sim.port+".gone", sim.port) }()
	err := writeChoiceSetting(device, preferredGenerationSetting(t), 1)
	if !errors.Is(err, errLink390Identity) || sim.handles != 0 || sim.list() != "" || deviceForID(7) == nil {
		t.Fatal(err, sim.handles, sim.list())
	}
	assertLink390Idle(t)
}
func TestLink390ScalarBusyGuardDoesNotRetireOwnedSlot(t *testing.T) {
	sim := newLink390Sim(t, "scalar-busy")
	installLink390Sim(t, sim)
	device := link390SimRegistry(t, sim)
	owner, err := link390ControlGate.exclusive(context.Background(), sim.port, sim.port)
	if err != nil {
		t.Fatal(err)
	}
	sim.advanceGeneration()
	err = writeChoiceSetting(device, preferredGenerationSetting(t), 1)
	owner.release()
	if !errors.Is(err, errControlBusy) || deviceForID(7) == nil || deviceForID(8) == nil || sim.handles != 0 {
		t.Fatal("busy guard retired a slot", err, sim.handles)
	}
	err = writeChoiceSetting(device, preferredGenerationSetting(t), 1)
	if !errors.Is(err, errLink390Changed) || deviceForID(7) != nil || sim.handles != 0 {
		t.Fatal("stale slot survived owner release", err, sim.handles)
	}
	assertLink390Idle(t)
}
func TestExactDongleAttachmentHelpersLeaveOtherScopesAlone(t *testing.T) {
	for _, change := range []func(*jabra_DeviceInfo){func(d *jabra_DeviceInfo) { d.productID = 0x24c7 }, func(d *jabra_DeviceInfo) { d.firmwareVersion = "1.3.0" }, func(d *jabra_DeviceInfo) { d.isDongle = false }} {
		device := dongleDeviceForTest(0x2e57)
		device.usbDevicePath = "/missing/no-generation-probe"
		change(device)
		if err := checkExactDongleBeforeOpen(device); err != nil {
			t.Fatal("non-exact scope was probed", err)
		}
		if err := checkExactDongleHandle(device, nil); err != nil {
			t.Fatal("non-exact handle was probed", err)
		}
	}
	exact := dongleDeviceForTest(0x2e57)
	exact.usbDevicePath = t.TempDir()
	if err := checkExactDongleHandle(exact, &hidrawConn{}); !errors.Is(err, errLink390Identity) {
		t.Fatal("unbound handle accepted", err)
	}
}
