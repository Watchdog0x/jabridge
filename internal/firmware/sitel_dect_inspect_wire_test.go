package firmware

import (
	"bytes"
	"context"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDECTInspectionUsesActual33ByteManagementAdapter(t *testing.T) {
	for _, extra := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "extra-PID-byte"}[extra], func(t *testing.T) {
			sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_NONBLOCK, 0)
			if err != nil {
				t.Fatal(err)
			}
			host := os.NewFile(uintptr(sockets[0]), "management-fixture")
			remoteFile := os.NewFile(uintptr(sockets[1]), "device-fixture")
			remote, err := net.FileConn(remoteFile)
			_ = remoteFile.Close()
			if err != nil {
				_ = host.Close()
				t.Fatal(err)
			}
			layout, err := SelectControlLayout(managementReports(5, 33))
			if err != nil {
				t.Fatal(err)
			}
			w := newEngage75World(syntheticEngage75Archive(), 0x35)
			w.headSerial = strings.Repeat("PRIVATE_SERIAL_", 3)
			peer := &engage75ManagementPeer{world: w}
			var device csrStageIO = peer
			if extra {
				device = &dectIdentityFaultIO{peer: peer, address: 1, op: 0x11, occurrence: 2, fault: "extra-byte"}
			}
			var mu sync.Mutex
			var written [][]byte
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					frame := make([]byte, 33)
					if _, err := io.ReadFull(remote, frame); err != nil {
						return
					}
					mu.Lock()
					written = append(written, append([]byte(nil), frame...))
					mu.Unlock()
					request := make([]byte, 64)
					request[0] = 5
					copy(request[1:], frame[1:])
					if device.Write(context.Background(), request) != nil {
						return
					}
					reply, err := device.Read(context.Background())
					if err != nil {
						return
					}
					remaining := reply[1:]
					for len(remaining) > 0 {
						frame := make([]byte, 33)
						frame[0] = 5
						n := copy(frame[1:], remaining)
						remaining = remaining[n:]
						if _, err := remote.Write(frame); err != nil {
							return
						}
					}
				}
			}()
			t.Cleanup(func() { _ = host.Close(); _ = remote.Close(); <-done })
			runtime := sitelRuntimeFromManagement(&ControlHidraw{file: host, Layout: layout})
			report, err := inspectDECTComponents(context.Background(), w.base.device(), runtime.io, func() error { return nil }, time.Second, 3*time.Second)
			if err != nil || report.IdentityPassed == extra {
				t.Fatalf("unexpected wire verdict extra=%t: %+v %v", extra, report, err)
			}
			if strings.Contains(report.InstallerIdentity, "PRIVATE") {
				t.Fatal("private identity leaked")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(written) == 0 || !bytes.Equal(written[0][:7], []byte{5, 1, 0, 1, 0x46, 2, 0x11}) {
				t.Fatal("wrong physical PID query", written)
			}
			for _, frame := range written {
				if len(frame) != 33 || frame[0] != 5 || frame[4] != 0x46 || frame[5] != 2 {
					t.Fatal("invalid physical management frame", frame)
				}
			}
			if extra && !strings.Contains(report.InstallerIdentity, "3 bytes, expected 2") {
				t.Fatal("extra PID byte accepted", report.InstallerIdentity)
			}
			if w.entries != 0 || len(w.boots) != 0 || w.saved != nil {
				t.Fatal("wire inspection changed firmware")
			}
		})
	}
}
