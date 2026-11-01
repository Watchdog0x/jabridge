package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLink390ConfigSessionWireAndCleanup(t *testing.T) {
	for _, key := range []string{"wireless-range", "softphone-integration"} {
		for _, scenario := range []string{"match", "denied", "write-NAK", "END-NAK", "END-lost", "reboot-on-write"} {
			t.Run(key+"/"+scenario, func(t *testing.T) {
				d, _ := link390ConfigDefinitionForKey(key)
				wanted := d.Choices[len(d.Choices)-1].Raw
				fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
				if err != nil {
					t.Fatal(err)
				}
				var gate controlGate
				owner, err := gate.exclusive(context.Background(), "/usb/config-simulated", "")
				if err != nil {
					t.Fatal(err)
				}
				defer owner.release()
				h := &hidrawConn{f: os.NewFile(uintptr(fds[0]), "config-control"), controlPort: owner.port, controlOwner: owner}
				peer := os.NewFile(uintptr(fds[1]), "config-endpoint")
				defer h.close()
				defer func() { _ = peer.Close() }()
				release := make(chan struct{})
				defer close(release)
				var detached atomic.Bool
				var writes, ends atomic.Int32
				done := make(chan error, 1)
				go func() {
					defer func() {
						if scenario != "END-lost" {
							<-release
						}
						_ = peer.Close()
					}()
					steps := 3
					if scenario == "denied" {
						steps = 1
					}
					if scenario == "reboot-on-write" {
						steps = 2
					}
					for step := 0; step < steps; step++ {
						buf := make([]byte, 64)
						n, err := peer.Read(buf)
						if err != nil {
							done <- err
							return
						}
						if n < 7 || buf[1] != gnpSrcDongle {
							done <- fmt.Errorf("wrong target: %x", buf[:n])
							return
						}
						flags, class, op := buf[4]&0xc0, buf[5], buf[6]
						replyClass, replyOp, data := byte(0xff), byte(0), []byte(nil)
						switch step {
						case 0:
							if flags != gnpFlagQuery || class != gnpClassPairingDevice || op != 0x11 || buf[4]&0x3f != 6 {
								done <- fmt.Errorf("wrong permission: %x", buf[:n])
								return
							}
							replyClass, replyOp, data = class, op, []byte{1}
							if scenario == "denied" {
								data[0] = 0
							}
						case 1:
							writes.Add(1)
							if flags != gnpFlagCmd || class != d.Class || op != d.Op || buf[4]&0x3f != 7 || buf[7] != wanted {
								done <- fmt.Errorf("wrong config write: %x", buf[:n])
								return
							}
							if scenario == "write-NAK" {
								replyClass, replyOp = 0xfe, 9
							}
							if scenario == "reboot-on-write" {
								detached.Store(true)
							}
						case 2:
							ends.Add(1)
							if flags != gnpFlagCmd || class != gnpClassPairingDevice || op != 0x12 || buf[4]&0x3f != 6 {
								done <- fmt.Errorf("wrong END: %x", buf[:n])
								return
							}
							if scenario == "END-lost" {
								done <- nil
								return
							}
							if scenario == "END-NAK" {
								replyClass, replyOp = 0xfe, 9
							}
						}
						reply, err := buildGNPReport(0, buf[3], 0xc0, replyClass, replyOp, data)
						if err == nil {
							reply[2] = gnpSrcDongle
							_, err = peer.Write(reply)
						}
						if err != nil {
							done <- err
							return
						}
					}
					done <- nil
				}()
				s := &link390HIDSession{h: h, source: &link390HIDSource{owner: owner, definition: d}, generation: "original", attachmentProbe: func() (string, error) {
					if detached.Load() {
						return "reconnected", nil
					}
					return "original", nil
				}}
				opened, enterErr := s.Enter()
				if scenario == "denied" {
					if opened || enterErr == nil {
						t.Fatal("permission denial ignored")
					}
				} else {
					if !opened || enterErr != nil {
						t.Fatal("permission failed", enterErr)
					}
					writeErr := s.Write(wanted)
					if (writeErr != nil) != (scenario == "write-NAK") {
						t.Fatal("write response classification", writeErr)
					}
					endErr := s.End()
					switch scenario {
					case "END-lost":
						if !errors.Is(endErr, errLink390Uncertain) {
							t.Fatal("lost END not uncertain", endErr)
						}
					case "END-NAK":
						if endErr == nil || errors.Is(endErr, errLink390Uncertain) || !strings.Contains(endErr.Error(), "GNP NAK") {
							t.Fatal("NAK was swallowed", endErr)
						}
					case "reboot-on-write":
						if !errors.Is(endErr, errLink390Changed) {
							t.Fatal("END reached replacement generation", endErr)
						}
					default:
						if endErr != nil {
							t.Fatal(endErr)
						}
					}
				}
				wantWrites, wantEnds := int32(1), int32(1)
				if scenario == "denied" {
					wantWrites, wantEnds = 0, 0
				}
				if scenario == "reboot-on-write" {
					wantEnds = 0
				}
				if writes.Load() != wantWrites || ends.Load() != wantEnds {
					t.Fatal("write/END replay or wrong cleanup", writes.Load(), ends.Load())
				}
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("simulator did not finish")
				}
			})
		}
	}
}

func TestLink390ConfigPublicationRebindsOnlyVerifiedAttachment(t *testing.T) {
	f := newConfigFlowFake()
	target := f.id
	device := &jabra_DeviceInfo{deviceID: 7, instance: "before", productID: 0x2e57, isDongle: true, serialNumber: target.USBSerial, usbDevicePath: target.Port, hidrawPath: target.HIDPath}
	stored := *device
	withDeviceState(t, devices{7: &stored}, -1, 7)
	identity := target
	identity.Generation = "new"
	identity.HIDPath = "/dev/hidraw9"
	result := link390ConfigResult{Key: "wireless-range", Status: link390Applied, Identity: identity}
	verified, err := publishLink390ConfigResult(device, target, result, identity, nil)
	if err != nil || verified.Device.hidrawPath != identity.HIDPath || verified.Device.instance == "before" {
		t.Fatal("verified rebind was not published", verified, err)
	}
	current := deviceForID(7)
	invalidateLink390Registry(device)
	if deviceForID(7) == nil {
		t.Fatal("stale failure deleted a newer generation")
	}
	invalidateLink390Registry(current)
	if deviceForID(7) != nil {
		t.Fatal("unverified node path remained reusable")
	}
}
