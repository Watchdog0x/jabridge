package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fakeControlPort(t *testing.T, pid string) string {
	t.Helper()
	port := t.TempDir()
	for name, value := range map[string]string{"idVendor": "0b0e", "idProduct": pid, "busnum": "1", "devnum": "2", "serial": "simulated"} {
		if err := os.WriteFile(filepath.Join(port, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return port
}

func TestControlGateUnboundExchangesDrainBeforeOwnership(t *testing.T) {
	var gate controlGate
	reader, err := gate.sharedUnbound()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	owned := make(chan *controlPermit, 1)
	failure := make(chan error, 1)
	go func() {
		p, err := gate.exclusive(ctx, "/usb/1-1", "")
		if err != nil {
			failure <- err
		} else {
			owned <- p
		}
	}()
	for !gate.busy("/usb/1-1") {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := gate.sharedUnbound(); !errors.Is(err, errControlBusy) {
		t.Fatal("unresolved exchange bypassed waiting owner", err)
	}
	select {
	case p := <-owned:
		p.release()
		t.Fatal("owner failed to drain unknown exchange")
	default:
	}
	reader.release()
	select {
	case p := <-owned:
		p.release()
	case err := <-failure:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestControlGateIdleNonScopedAndUnreadableMetadataKeepRawOpens(t *testing.T) {
	for _, mode := range []string{"link380", "nonUSB", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			port := fakeControlPort(t, "24c7")
			locate := func(string) (string, error) { return port, nil }
			if mode == "nonUSB" {
				locate = func(string) (string, error) { return "", errNoControlUSBParent }
			}
			if mode == "unreadable" {
				if err := os.Remove(filepath.Join(port, "idVendor")); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			h, err := openHidrawCoordinated("simulated", nil, locate, func(string) (*hidrawConn, error) { calls++; return &hidrawConn{path: "simulated"}, nil })
			if err != nil || h == nil || calls != 1 {
				t.Fatal("idle non-scoped open changed", calls, err)
			}
			h.close()
		})
	}
}

func TestControlGateRejectsNodeReassignmentDuringRawOpen(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		port := fakeControlPort(t, "24c7")
		var owner *controlPermit
		var opened *hidrawConn
		locate := func(string) (string, error) {
			if unknown && owner == nil {
				return "", errNoControlUSBParent
			}
			return port, nil
		}
		h, err := openHidrawCoordinated("simulated", nil, locate, func(string) (*hidrawConn, error) {
			if err := os.WriteFile(filepath.Join(port, "idProduct"), []byte("2e57"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			owner, err = link390ControlGate.exclusive(context.Background(), port, "")
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			opened = &hidrawConn{f: file, path: "simulated"}
			return opened, nil
		})
		if owner != nil {
			owner.release()
		}
		if h != nil || !errors.Is(err, errControlBusy) || opened == nil || opened.f != nil {
			t.Fatal("reassigned node escaped ownership or was not closed", h, err)
		}
	}
}

func TestControlGateBoundSearchLifetimeAndOnceClose(t *testing.T) {
	var gate controlGate
	permit, err := gate.shared("/usb/search", true)
	if err != nil {
		t.Fatal(err)
	}
	link := &searchLink{device: &jabra_DeviceInfo{productID: 0x2e57}, permit: permit}
	if _, err := gate.exclusive(context.Background(), "/usb/search", ""); !errors.Is(err, errSearchActive) {
		t.Fatal("bound search did not retain ownership", err)
	}
	if err := link.Close(); err != nil {
		t.Fatal(err)
	}
	if err := link.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := link.boundExchange(context.Background(), gnpFlagCmd, gnpClassPairingDevice, 0x1f, nil); !errors.Is(err, errControlClosed) {
		t.Fatal("closed bound search reached packet path", err)
	}
	owner, err := gate.exclusive(context.Background(), "/usb/search", "")
	if err != nil {
		t.Fatal(err)
	}
	owner.release()
}

func TestControlGateRetainsOwnedUSBRegistryUntilRelease(t *testing.T) {
	port := fakeControlPort(t, "2e57")
	device := &jabra_DeviceInfo{deviceID: 7, productID: 0x2e57, isDongle: true, deviceConnection: deviceConnectionType_USB, usbDevicePath: port}
	withDeviceState(t, devices{7: device}, -1, 7)
	owner, err := link390ControlGate.exclusive(context.Background(), port, port)
	if err != nil {
		t.Fatal(err)
	}
	removeMissingUSBDevices(map[string]bool{})
	if deviceForID(7) == nil {
		owner.release()
		t.Fatal("owned restart removed the selected device")
	}
	owner.release()
	removeMissingUSBDevices(map[string]bool{})
	if deviceForID(7) != nil {
		t.Fatal("released missing device remained cached")
	}
}
