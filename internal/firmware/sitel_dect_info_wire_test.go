package firmware

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDECTImageInfoUsesActual33ByteManagementAdapter(t *testing.T) {
	for _, length := range []int{17, 19} {
		sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		host := os.NewFile(uintptr(sockets[0]), "info-management-fixture")
		remoteFile := os.NewFile(uintptr(sockets[1]), "info-device-fixture")
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
		peer := &dectInfoPeer{length: length}
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
				written = append(written, bytes.Clone(frame))
				mu.Unlock()
				request := make([]byte, 64)
				copy(request, frame)
				if peer.Write(context.Background(), request) != nil {
					return
				}
				reply, err := peer.Read(context.Background())
				if err != nil {
					return
				}
				physical := make([]byte, 33)
				copy(physical, reply)
				if _, err := remote.Write(physical); err != nil {
					return
				}
			}
		}()
		_, _, device := newReporterDECT(false)
		runtime := sitelRuntimeFromManagement(&ControlHidraw{file: host, Layout: layout})
		checks, checkErr := inspectDECTImageInfo(context.Background(), device, runtime.io, func() error { return nil }, time.Second, 3*time.Second)
		_ = host.Close()
		_ = remote.Close()
		<-done
		if checkErr != nil || len(checks) != 3 {
			t.Fatal(checks, checkErr)
		}
		mu.Lock()
		if len(written) != 3 {
			t.Fatalf("expected exactly3 physical requests, got%d", len(written))
		}
		for i, address := range []byte{1, 10, 3} {
			want := make([]byte, 33)
			copy(want, []byte{5, address, 0, byte(i + 1), 0x46, 15, 0})
			if !bytes.Equal(written[i], want) || checks[i].State != "INFO" {
				t.Fatalf("bad physical request or decode (%d bytes): %x %+v", length, written[i], checks[i])
			}
		}
		mu.Unlock()
	}
}
