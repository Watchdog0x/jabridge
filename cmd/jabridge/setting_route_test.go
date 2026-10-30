package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func qualifiedCycleDeviceForTest(t *testing.T) (*jabra_DeviceInfo, choiceSettingValue) {
	t.Helper()
	withDeviceState(t, devices{
		1: {deviceID: 1, instance: "dongle", isDongle: true, productID: 0x2e56, firmwareVersion: "3.2.0", hidrawPath: "mock-hid"},
		2: {deviceID: 2, instance: "headset", productID: 0x2549, parentDeviceID: 1, deviceConnection: deviceConnectionType_BT},
	}, 2, 1)
	device := deviceForID(2)
	definition := cycleDefinitionForTest(t, cycleProfileForTest(), deviceForID(1))
	value, err := decodeChoiceSettingPayload(definition, []byte{3, 2, 1, 0})
	if err != nil {
		t.Fatal(err)
	}
	return device, value
}

func TestRetainedNoiseCycleRejectsChangedParentBeforeLease(t *testing.T) {
	for _, field := range []string{"firmware", "pid", "instance", "path", "child-parent"} {
		for _, wanted := range []string{"hearthrough,anc", "hearthrough,off"} {
			t.Run(field+"/"+wanted, func(t *testing.T) {
				state := t.TempDir()
				t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", state)
				device, value := qualifiedCycleDeviceForTest(t)
				index, ok := findChoiceIndex(value.Definition, wanted)
				if !ok {
					t.Fatal("missing qualified choice")
				}
				deviceStateMu.Lock()
				switch field {
				case "firmware":
					deviceManager[1].firmwareVersion = "3.3.0"
				case "pid":
					deviceManager[1].productID = 0x2e57
				case "instance":
					deviceManager[1].instance = "new-attachment"
				case "path":
					deviceManager[1].hidrawPath = "other-hid"
				case "child-parent":
					deviceManager[2].parentDeviceID = 3
				}
				deviceStateMu.Unlock()
				if err := writeChoiceSetting(device, value, value.ChoiceIndex); err != nil {
					t.Fatal("no-op performed route/device access", err)
				}
				err := writeChoiceSetting(device, value, index)
				if err == nil || !strings.Contains(err.Error(), "dongle connection or firmware changed") {
					t.Fatal("retained setting did not reject its stale parent qualification", err)
				}
				entries, err := os.ReadDir(state)
				if err != nil || len(entries) != 0 {
					t.Fatal("stale route acquired a settings lease", entries, err)
				}
			})
		}
	}
}

func TestQualifiedNoiseCycleHeldRouteGuard(t *testing.T) {
	t.Setenv("JABRIDGE_HISTORY", "off")
	device, value := qualifiedCycleDeviceForTest(t)
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	h := &hidrawConn{f: os.NewFile(uintptr(fds[0]), "mock-hid"), path: "mock-hid"}
	peer := os.NewFile(uintptr(fds[1]), "mock-device")
	t.Cleanup(func() { h.close(); _ = peer.Close() })
	guard := choiceSettingRouteGuard(device, value.Definition)
	if guard == nil || guard(h) != nil {
		t.Fatal("unchanged route did not pass")
	}
	queries := 0
	if err := checkSettingFirmware("1.0.1", func() ([]byte, error) {
		queries++
		deviceStateMu.Lock()
		deviceManager[1].firmwareVersion = "3.3.0"
		deviceStateMu.Unlock()
		return []byte{5, '1', '.', '0', '.', '1'}, nil
	}); err != nil {
		t.Fatal(err)
	}
	err = sendProfiledChoicePacket(device, value.Definition, 0, []byte{2, 2, 1}, h, 4, guard)
	if queries != 1 || err == nil || !strings.Contains(err.Error(), "dongle connection or firmware changed") {
		t.Fatal("late parent change reached command", queries, err)
	}
	poll := []unix.PollFd{{Fd: int32(peer.Fd()), Events: unix.POLLIN}}
	if n, err := unix.Poll(poll, 0); err != nil || n != 0 {
		t.Fatal("route rejection sent a packet", n, err)
	}
	deviceStateMu.Lock()
	deviceManager[1].firmwareVersion = "3.2.0"
	deviceStateMu.Unlock()
	h.path = "other-hid"
	if err := guard(h); err == nil {
		t.Fatal("held handle path was not checked")
	}
	h.path = "mock-hid"
	done := make(chan error, 1)
	go func() {
		packet := make([]byte, 64)
		n, err := peer.Read(packet)
		if err != nil {
			done <- err
			return
		}
		if n < 10 || packet[1] != 4 || packet[4]&0xc0 != gnpFlagCmd || packet[5] != gnpClassConfig || packet[6] != 0xbf || packet[7] != 2 || packet[8] != 2 || packet[9] != 1 {
			done <- fmt.Errorf("unexpected setting command: %x", packet[:n])
			return
		}
		reply, err := buildGNPReport(0, packet[3], 0xc0, 0xff, 0, nil)
		if err == nil {
			reply[2] = 4
			_, err = peer.Write(reply)
		}
		done <- err
	}()
	if err := sendProfiledChoicePacket(device, value.Definition, 0, []byte{2, 2, 1}, h, 4, guard); err != nil {
		t.Fatal("unchanged route command failed", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("mock setting command did not finish")
	}
	deviceStateMu.Lock()
	deviceManager[1].instance = "changed-after-command"
	deviceStateMu.Unlock()
	if err := verifyChoiceRouteReadback(h, guard); err == nil || !strings.Contains(err.Error(), "during verification") {
		t.Fatal("changed route reported verified success", err)
	}
}
