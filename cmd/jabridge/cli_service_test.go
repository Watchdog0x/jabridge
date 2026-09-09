package main

import (
	"bytes"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

type cliOwnerTestAPI struct {
	jabraAPIBridge
	writes chan string
}

func (a *cliOwnerTestAPI) ListDevices() []ipc.DeviceInfo {
	return []ipc.DeviceInfo{{ID: 7, PID: 0x0422, Name: "Speaker", Selected: true, Connection: "usb", Firmware: "2.32.8"}}
}
func (a *cliOwnerTestAPI) SetSetting(device, key, value string) (ipc.SettingInfo, error) {
	a.writes <- device + "." + key + "=" + value
	return ipc.SettingInfo{Key: key, Value: value}, nil
}

func TestCLIUsesExistingSocketForStatusAndSettingWrites(t *testing.T) {
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "service.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	api := &cliOwnerTestAPI{writes: make(chan string, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			ipc.HandleConnection(conn, api)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := ipc.Dial(ctx, listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close(); <-done }()
	var out bytes.Buffer
	if err := runServiceCLI(client, []string{"status"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2.32.8") || !strings.Contains(out.String(), "0422") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := runServiceCLI(client, []string{"settings", "set", "headset.speed-dial", ""}, &out); err != nil {
		t.Fatal(err)
	}
	if got := <-api.writes; got != "headset.speed-dial=" {
		t.Fatal(got)
	}
}
