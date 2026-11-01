package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The production GNP query, command and firmware guard run against a fake
// endpoint. This does not exercise hidraw ioctl/open or a physical device.
func TestDongleProfiledPacketsAndFailuresDoNotReplay(t *testing.T) {
	for _, definition := range dongleChoiceSettingDefinitions {
		for _, scenario := range []string{"match", "firmware changed", "target changed", "alternate address", "readback mismatch", "read failure"} {
			t.Run(definition.Key+"/"+scenario, func(t *testing.T) {
				fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
				if err != nil {
					t.Fatal(err)
				}
				h := &hidrawConn{f: os.NewFile(uintptr(fds[0]), "simulated-control"), path: "simulated-control"}
				peer := os.NewFile(uintptr(fds[1]), "simulated-device")
				t.Cleanup(func() { h.close(); _ = peer.Close() })
				device := dongleDeviceForTest(0x2e56)
				stored := *device
				withDeviceState(t, devices{7: &stored}, -1, 7)
				wanted := definition.Choices[len(definition.Choices)-1].Raw
				var writes atomic.Int32
				done := make(chan error, 1)
				release := make(chan struct{})
				t.Cleanup(func() { close(release) })
				go func() {
					defer func() {
						if scenario != "read failure" {
							<-release
						}
						_ = peer.Close()
					}()
					readPacket := func() ([]byte, error) {
						buffer := make([]byte, 64)
						n, err := peer.Read(buffer)
						if err == nil && n < 7 {
							err = errors.New("short GNP packet")
						}
						return buffer[:n], err
					}
					respond := func(packet []byte, class, op byte, data []byte, source byte) error {
						reply, err := buildGNPReport(0, packet[3], 0xc0, class, op, data)
						if err == nil {
							reply[2] = source
							_, err = peer.Write(reply)
						}
						return err
					}
					packet, err := readPacket()
					if err != nil {
						done <- err
						return
					}
					if packet[1] != gnpSrcDongle || packet[5] != gnpClassDevInfo || packet[6] != gnpOpFirmwareVer || packet[4]&0xc0 == gnpFlagCmd {
						done <- fmt.Errorf("write preceded held-handle dongle firmware check: %x", packet)
						return
					}
					version, source := "3.2.0", gnpSrcDongle
					if scenario == "firmware changed" {
						version = "3.3.0"
					}
					if scenario == "alternate address" {
						source = gnpSrcHost
					}
					if scenario == "target changed" {
						deviceStateMu.Lock()
						deviceManager[7].instance = "new-attachment"
						deviceStateMu.Unlock()
					}
					if err = respond(packet, gnpClassDevInfo, gnpOpFirmwareVer, append([]byte{byte(len(version))}, []byte(version)...), source); err != nil {
						done <- err
						return
					}
					if scenario == "firmware changed" || scenario == "target changed" || scenario == "alternate address" {
						done <- nil
						return
					}
					packet, err = readPacket()
					if err == nil && (packet[1] != gnpSrcDongle || packet[4]&0xc0 != gnpFlagCmd || packet[5] != definition.Class || packet[6] != definition.Op || int(packet[4]&0x3f) != 7 || packet[7] != wanted) {
						err = fmt.Errorf("wrong scalar packet: %x", packet)
					}
					if err == nil {
						writes.Add(1)
						err = respond(packet, 0xff, 0, nil, gnpSrcDongle)
					}
					if err != nil {
						done <- err
						return
					}
					packet, err = readPacket()
					if err == nil && (packet[5] != definition.Class || packet[6] != definition.Op || packet[4]&0xc0 == gnpFlagCmd) {
						err = fmt.Errorf("readback replayed a write: %x", packet)
					}
					if scenario == "read failure" {
						done <- err
						return
					}
					actual := wanted
					if scenario == "readback mismatch" {
						actual = 0xff
					}
					if err == nil {
						err = respond(packet, definition.Class, definition.Op, []byte{actual}, gnpSrcDongle)
					}
					done <- err
				}()
				err = withProfiledSettingHandle(device, definition.Key, 0, "3.2.0", h, gnpSrcDongle, func(handle *hidrawConn, destination byte) error {
					if err := gnpCommand(handle, destination, nextSeq(), definition.Class, definition.Op, []byte{wanted}); err != nil {
						return err
					}
					payload, err := gnpQueryPayloadWithDataTimeout(handle, destination, nextSeq(), definition.Class, definition.Op, nil, 200*time.Millisecond)
					if err != nil {
						return err
					}
					readback, err := decodeChoiceSettingPayload(definition, payload)
					if err == nil && readback.Raw != wanted {
						return errors.New("setting readback mismatch")
					}
					return err
				})
				if (err == nil) != (scenario == "match") {
					t.Fatal("unexpected verification result", scenario, err)
				}
				if scenario == "firmware changed" && !strings.Contains(err.Error(), "firmware changed") || scenario == "readback mismatch" && !strings.Contains(err.Error(), "readback mismatch") {
					t.Fatal("failure came from the wrong stage", err)
				}
				wantWrites := int32(1)
				if strings.Contains(scenario, "changed") || scenario == "alternate address" {
					wantWrites = 0
				}
				if writes.Load() != wantWrites {
					t.Fatal("write before guard or replay after failure", writes.Load(), wantWrites)
				}
				select {
				case peerErr := <-done:
					if peerErr != nil {
						t.Fatal(peerErr)
					}
				case <-time.After(time.Second):
					t.Fatal("simulated endpoint did not finish")
				}
			})
		}
	}
}
