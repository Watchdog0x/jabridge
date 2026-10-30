package main

import (
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/internal/btsearch"
	"github.com/Watchdog0x/jabridge/internal/firmware"
	"golang.org/x/sys/unix"
)

func TestLink380CClassicWireTrace(t *testing.T) {
	found := btsearch.Device{Address: [6]byte{1, 2, 3, 4, 5, 6}, BluetoothType: 1}
	pair, err := btsearch.LegacyPairCommand(found)
	if err != nil {
		t.Fatal(err)
	}
	commands := [][]byte{btsearch.StartCommand(), btsearch.StopCommand(), pair}
	want := [][]byte{{0x20, 5, 20}, {0x21, 1}, {0x30, 4, 1, 2, 3, 4, 5, 6}}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	h := &hidrawConn{f: os.NewFile(uintptr(fds[0]), "link380c-sim"), path: "synthetic", reportSize: 64}
	peer := os.NewFile(uintptr(fds[1]), "link380c-device")
	t.Cleanup(func() { h.close(); _ = peer.Close() })
	done := make(chan error, 1)
	go func() {
		for _, command := range want {
			packet := make([]byte, 64)
			n, err := peer.Read(packet)
			if err != nil {
				done <- err
				return
			}
			end := 6 + len(command)
			if n != 64 || packet[0] != 5 || packet[1] != 1 || packet[4] != gnpFlagCmd|byte(end-1) || packet[5] != gnpClassPairingDevice || !bytes.Equal(packet[6:end], command) {
				done <- fmt.Errorf("wrong Classic GNP frame: %x", packet[:n])
				return
			}
			reply, err := buildGNPReport(0, packet[3], 0xc0, 0xff, 0, nil)
			if err == nil {
				reply[2] = 1
				_, err = peer.Write(reply)
			}
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for _, command := range commands {
		if err := gnpCommand(h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, command[0], command[1:]); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("simulated Classic trace did not finish")
	}
}

func TestDiscoveryWireSelectionIsIndependentOfUSBBinding(t *testing.T) {
	for _, test := range []struct {
		pid         uint16
		start, stop []byte
	}{
		{0x24c7, []byte{0x20, 5, 20}, []byte{0x21, 1}},
		{0x24c8, []byte{0x20, 5, 20}, []byte{0x21, 1}},
		{0x24c9, []byte{0x20, 5, 20}, []byte{0x21, 1}},
		{0x2e56, []byte{0x20, 7, 20}, []byte{0x21, 3}},
		{0x2e57, []byte{0x20, 7, 20}, []byte{0x21, 3}},
	} {
		for _, binding := range []*firmware.ControlBinding{nil, {}} {
			link := &searchLink{device: &jabra_DeviceInfo{isDongle: true, productID: test.pid}, binding: binding}
			if !bytes.Equal(link.discoveryCommand(true), test.start) || !bytes.Equal(link.discoveryCommand(false), test.stop) {
				t.Fatal("binding changed the search wire format", test.pid, binding != nil)
			}
		}
	}
}
