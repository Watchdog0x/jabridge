package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

func resetNativeFixture(t *testing.T) (*link390Sim, *jabra_DeviceInfo) {
	t.Helper()
	sim := newLink390Sim(t, "reset")
	sim.fw = "1.3.0"
	installLink390Sim(t, sim)
	link390SimRegistry(t, sim)
	identity, err := link390USBIdentity(sim.port, sim.hid)
	if err != nil {
		t.Fatal(err)
	}
	updateDeviceByID(7, func(device *jabra_DeviceInfo) {
		device.firmwareVersion = "1.3.0"
		device.usbGeneration = identity.Generation
		device.instance = newDeviceInstance()
		device.pairingList = &pairingList{count: 1, pairedDevices: []pairedDevice{{deviceName: "cached test name"}}}
	})
	t.Cleanup(func() { resetAttempts.Lock(); delete(resetAttempts.ports, sim.port); resetAttempts.Unlock() })
	return sim, deviceForID(7)
}

func TestLink390ResetNativeWireAndRecovery(t *testing.T) {
	for _, scenario := range []struct {
		name, mode    string
		wrongFirmware bool
		want          link390ResetOutcome
		returned      bool
	}{
		{name: "ACK and restart", mode: "ack", want: resetRestartVerified, returned: true},
		{name: "lost ACK and restart", mode: "lost", want: resetUnknown, returned: true},
		{name: "NAK", mode: "nak", want: resetRejected},
		{name: "firmware changed", mode: "ack", wrongFirmware: true, want: resetUnknown},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			sim, device := resetNativeFixture(t)
			sim.extraCommand = func(flags, class, op byte, payload []byte, reply func(byte, byte, []byte)) bool {
				if flags != gnpFlagCmd || class != gnpClassPairingDevice || op != gnpOpFactoryDefaultBT {
					return false
				}
				sim.frame(fmt.Sprintf("reset:%x", payload))
				if scenario.mode == "nak" {
					reply(0xfe, 9, nil)
					return true
				}
				if scenario.mode == "ack" {
					reply(0xff, 0, nil)
				}
				if scenario.wrongFirmware {
					sim.mu.Lock()
					sim.fw = "3.2.0"
					sim.mu.Unlock()
				}
				sim.restart("/dev/simhidraw-reset-returned")
				return true
			}
			result, err := (&jabraAPIBridge{}).FactoryResetBound(*settingTarget(device))
			if err != nil || result.Outcome != string(scenario.want) || !result.Attempted || result.Reconnected != scenario.returned {
				t.Fatal(result, err)
			}
			if got := sim.list(); got != "reset:" {
				t.Fatal("reset used extra commands or a nonempty payload", got)
			}
			current := deviceForID(7)
			if scenario.returned {
				live, identityErr := link390USBIdentity(sim.port, sim.hid)
				if identityErr != nil || current == nil || current.instance == device.instance || current.usbGeneration != live.Generation || current.hidrawPath != "/dev/simhidraw-reset-returned" || current.pairingList != nil || deviceForID(8) != nil {
					t.Fatal("registry was not recovered", current)
				}
			} else if scenario.want == resetRejected {
				if current == nil || current.instance != device.instance || current.usbGeneration != device.usbGeneration || current.pairingList == nil || deviceForID(8) == nil {
					t.Fatal("NAK changed cached state", current)
				}
				retry, err := (&jabraAPIBridge{}).FactoryResetBound(*settingTarget(device))
				if err != nil || retry.Outcome != string(resetRefused) || retry.Attempted || sim.list() != "reset:" {
					t.Fatal("same generation replayed reset", retry, err, sim.list())
				}
			} else if current != nil || deviceForID(8) != nil {
				t.Fatal("unverified slot was retained", current)
			}
			assertLink390Idle(t)
		})
	}
}

func TestLink390ResetNativeBindingAndScopeBeforeOpen(t *testing.T) {
	for _, mutate := range []func(*jabra_DeviceInfo){
		func(d *jabra_DeviceInfo) { d.productID = 0x2e56 }, func(d *jabra_DeviceInfo) { d.productID = 0x24c7 },
		func(d *jabra_DeviceInfo) { d.firmwareVersion = "3.2.0" }, func(d *jabra_DeviceInfo) { d.firmwareVersion = "1.4.2" },
		func(d *jabra_DeviceInfo) { d.variantType = "04-11" }, func(d *jabra_DeviceInfo) { d.serialNumber = "" },
		func(d *jabra_DeviceInfo) { d.usbGeneration = "" },
		func(d *jabra_DeviceInfo) { d.instance = "short" }, func(d *jabra_DeviceInfo) { d.isDongle = false },
		func(d *jabra_DeviceInfo) { d.deviceConnection = deviceConnectionType_BT }, func(d *jabra_DeviceInfo) { d.parentDeviceID = 8 },
	} {
		sim, _ := resetNativeFixture(t)
		updateDeviceByID(7, mutate)
		device := deviceForID(7)
		target := settingTarget(device)
		if target == nil {
			target = &ipc.SettingTarget{ID: 7}
		}
		if _, err := (&jabraAPIBridge{}).FactoryResetBound(*target); err == nil {
			t.Fatal("ineligible reset was accepted", device)
		}
		if sim.handles != 0 || sim.list() != "" {
			t.Fatal("ineligible target reached the device", sim.handles, sim.list())
		}
	}
	sim, device := resetNativeFixture(t)
	target := *settingTarget(device)
	target.Instance = strings.Repeat("b", 32)
	if _, err := (&jabraAPIBridge{}).FactoryResetBound(target); err == nil || sim.handles != 0 {
		t.Fatal("stale target reached transport", err, sim.handles)
	}
}

func TestLink390ResetNativeSearchAndLeaseRefuseWithoutPacket(t *testing.T) {
	sim, device := resetNativeFixture(t)
	search, err := link390ControlGate.shared(sim.port, true)
	if err != nil {
		t.Fatal(err)
	}
	result := resetLink390Dongle(device)
	search.release()
	if result.Attempted || !errors.Is(result.cause, errSearchActive) || sim.handles != 0 {
		t.Fatal(result, sim.handles)
	}
	release, err := beginSettingTransaction()
	if err != nil {
		t.Fatal(err)
	}
	result = resetLink390Dongle(device)
	release()
	if result.Attempted || sim.handles != 0 {
		t.Fatal("reset ignored settings lease", result, sim.handles)
	}
	assertLink390Idle(t)
}

func TestLink390ResetAttemptLedgerPinsGeneration(t *testing.T) {
	target := newResetFlowFake().target
	target.Port = t.TempDir()
	source := &link390ResetHIDSource{}
	t.Cleanup(func() { resetAttempts.Lock(); delete(resetAttempts.ports, target.Port); resetAttempts.Unlock() })
	if err := source.BeginResetAttempt(target); err != nil {
		t.Fatal(err)
	}
	source.SetResetOutcome(target, resetUnknown)
	if err := source.BeginResetAttempt(target); !errors.Is(err, errResetAlreadyAttempted) {
		t.Fatal(err)
	}
	target.Generation = "new-generation"
	if err := source.BeginResetAttempt(target); err != nil {
		t.Fatal("new attachment refused", err)
	}
}

func TestLink390ResetCachedPIDMustMatchPort(t *testing.T) {
	sim, device := resetNativeFixture(t)
	if err := os.WriteFile(filepath.Join(sim.port, "idProduct"), []byte("2e56"), 0600); err != nil {
		t.Fatal(err)
	}
	result := resetLink390Dongle(device)
	if result.Attempted || !errors.Is(result.cause, errLink390Identity) || sim.handles != 0 {
		t.Fatal(result, sim.handles)
	}
}

func TestLink390ResetProfileNeverAllowsConfigWrites(t *testing.T) {
	fake := newConfigFlowFake()
	fake.id.Firmware = "1.3.0"
	if !fake.id.valid() {
		t.Fatal("reset identity was not recognized")
	}
	result, err := runLink390ConfigChange(context.Background(), fake, fake.id, "wireless-range", 2, nil)
	if err == nil || result.Attempted || fake.opens != 0 {
		t.Fatal("reset qualification enabled config writes", result, err, fake.opens)
	}
}

func TestLink390ResetRejectsReplugBeforeRegistryRefresh(t *testing.T) {
	for _, renumber := range []bool{false, true} {
		t.Run(fmt.Sprintf("renumber=%t", renumber), func(t *testing.T) {
			sim, device := resetNativeFixture(t)
			target := *settingTarget(device)
			sim.extraCommand = func(flags, class, op byte, payload []byte, reply func(byte, byte, []byte)) bool {
				if flags != gnpFlagCmd || class != gnpClassPairingDevice || op != gnpOpFactoryDefaultBT {
					return false
				}
				sim.frame("unexpected-reset")
				reply(0xfe, 9, nil)
				return true
			}
			path := sim.hid
			if renumber {
				path = "/dev/simhidraw-fast-replug"
			}
			sim.restart(path) // The scanner has not observed an absent interval.
			result, err := (&jabraAPIBridge{}).FactoryResetBound(target)
			if err != nil || result.Outcome != string(resetRefused) || result.Attempted || sim.handles != 0 || sim.list() != "" || deviceForID(7) != nil || deviceForID(8) != nil {
				t.Fatal("stale attachment reached or remained in the registry", result, err, sim.handles, sim.list())
			}
			resetAttempts.Lock()
			_, attempted := resetAttempts.ports[sim.port]
			resetAttempts.Unlock()
			if attempted {
				t.Fatal("stale request entered reset ledger")
			}
			registered, added := registerUSBDevice(usbDev{sysPath: sim.port, vendorID: jabraVendorID, productID: 0x2e57, serial: "SERIAL-A", product: "Jabra Link 390"})
			live, identityErr := link390USBIdentity(sim.port, sim.hid)
			if !added || identityErr != nil || registered.instance == device.instance || registered.usbGeneration != live.Generation {
				t.Fatal("re-registration did not capture a new binding", registered, added, identityErr)
			}
			// Changing the numeric ID alone cannot revive the old confirmation.
			target.ID = registered.deviceID
			if _, err := (&jabraAPIBridge{}).FactoryResetBound(target); err == nil || sim.handles != 0 || sim.list() != "" {
				t.Fatal("old instance survived re-registration", err, sim.handles, sim.list())
			}
		})
	}
}

func TestLink390ResetStaleRefusalLeavesOtherDongleUntouched(t *testing.T) {
	a, device := resetNativeFixture(t)
	b := newLink390Sim(t, "other-reset")
	if err := os.WriteFile(filepath.Join(b.port, "serial"), []byte("SERIAL-B"), 0600); err != nil {
		t.Fatal(err)
	}
	b.serial = "SERIAL-B"
	b.fw = "1.3.0"
	installLink390Sim(t, a, b)
	identity, err := link390USBIdentity(b.port, b.hid)
	if err != nil {
		t.Fatal(err)
	}
	other := &jabra_DeviceInfo{deviceID: 9, instance: newDeviceInstance(), productID: 0x2e57, vendorID: jabraVendorID, isDongle: true, deviceConnection: deviceConnectionType_USB, usbDevicePath: b.port, hidrawPath: b.hid, serialNumber: "SERIAL-B", firmwareVersion: "1.3.0", variantType: "04-10", usbGeneration: identity.Generation}
	child := &jabra_DeviceInfo{deviceID: 10, instance: "other-child", deviceConnection: deviceConnectionType_BT, parentDeviceID: 9}
	deviceStateMu.Lock()
	deviceManager[9] = cloneDeviceInfo(other)
	deviceManager[10] = child
	deviceStateMu.Unlock()
	a.restart(a.hid)
	result, err := (&jabraAPIBridge{}).FactoryResetBound(*settingTarget(device))
	after := deviceForID(9)
	if err != nil || result.Attempted || after == nil || after.instance != other.instance || after.usbGeneration != other.usbGeneration || deviceForID(10) == nil || a.handles != 0 || b.handles != 0 {
		t.Fatal(result, err, after)
	}
}
