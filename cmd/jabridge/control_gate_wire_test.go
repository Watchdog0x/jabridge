package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestControlGateOwnershipBlocksUnknownGNPBeforePacket(t *testing.T) {
	owner, err := link390ControlGate.exclusive(context.Background(), "/usb/simulated-owned", "")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	h := &hidrawConn{f: os.NewFile(uintptr(fds[0]), "unresolved-control"), path: "unresolved-control"}
	peer := os.NewFile(uintptr(fds[1]), "simulated-endpoint")
	defer h.close()
	defer func() { _ = peer.Close() }()
	if err := gnpCommand(h, gnpSrcDongle, nextSeq(), gnpClassConfig, 0x4c, []byte{1}); !errors.Is(err, errControlBusy) {
		t.Fatal("command bypassed ownership", err)
	}
	if _, err := gnpQueryPayload(h, gnpSrcDongle, nextSeq(), gnpClassDevInfo, gnpOpFirmwareVer); !errors.Is(err, errControlBusy) {
		t.Fatal("query bypassed ownership", err)
	}
	poll := []unix.PollFd{{Fd: int32(peer.Fd()), Events: unix.POLLIN}}
	if n, err := unix.Poll(poll, 0); err != nil || n != 0 {
		t.Fatal("blocked operation emitted a packet", n, err)
	}
}
