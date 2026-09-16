package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"golang.org/x/sys/unix"
)

type terminalProbe struct {
	NonblockingOutput bool
	ExpectedBytes     int
}

func TestTUIInteractiveProbe(t *testing.T) {
	if os.Getenv("JABRIDGE_TEST_INTERACTIVE_PROBE") != "1" {
		t.Skip("interactive PTY helper")
	}
	configureHistory()
	if os.Getenv("JABRIDGE_TEST_SEARCH_SCREEN") == "1" {
		menuState = screenSearch
		clearSearchResults()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	client, err := ipc.Dial(ctx, os.Getenv("JABRIDGE_TEST_SOCKET"))
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := runTUIWithBackend(&tuiIPCBackend{client: client}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	os.Exit(0)
}

func TestTUITerminalProbe(t *testing.T) {
	if os.Getenv("JABRIDGE_TEST_TERMINAL_PROBE") != "1" {
		t.Skip("PTY subprocess helper")
	}
	settings, err := enableRawMode()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, cancel := context.WithCancel(context.Background())
	keys := make(chan keyEvent, 4)
	done := make(chan struct{})
	go func() { defer close(done); startKeysPressedListener(ctx, keys) }()
	ready := os.NewFile(3, "pty-ready")
	if _, err := ready.Write([]byte{1}); err != nil {
		os.Exit(6)
	}
	_ = ready.Close()
	select {
	case <-keys:
	case <-time.After(3 * time.Second):
		os.Exit(3)
	}
	flags, err := unix.FcntlInt(os.Stdout.Fd(), unix.F_GETFL, 0)
	if err != nil {
		os.Exit(4)
	}
	report := terminalProbe{NonblockingOutput: flags&unix.O_NONBLOCK != 0}
	for index := 0; index < 3; index++ {
		f := newFrame(191, 51)
		for row := 1; row <= 51; row++ {
			f.setText(row, 1, strings.Repeat("x", 191), styleText)
		}
		f.setText(50, 5, fmt.Sprintf("FRAME_END_%d Q Back", index), styleAction)
		report.ExpectedBytes += len(f.render())
		if err := flushFrame(f); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(5)
		}
	}
	cancel()
	<-done
	restoreTerminal(settings)
	_ = json.NewEncoder(os.Stderr).Encode(report)
	os.Exit(0)
}

func TestTUIInputDoesNotTruncateTerminalFrames(t *testing.T) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "pty-master")
	defer func() { _ = master.Close() }()
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slave.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTUITerminalProbe$")
	command.Env = append(os.Environ(), "JABRIDGE_TEST_TERMINAL_PROBE=1")
	command.Stdin, command.Stdout = slave, slave
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readyReader.Close(); _ = readyWriter.Close() }()
	command.ExtraFiles = []*os.File{readyWriter}
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close()
	_ = readyWriter.Close()
	// The child reads this key first; delay reading its output to model a
	// terminal that has not yet drained its output buffer.
	if err := readyReader.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var readyByte [1]byte
	if _, err := io.ReadFull(readyReader, readyByte[:]); err != nil {
		t.Fatal("terminal probe did not become ready", err)
	}
	if _, err := master.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	_ = master.SetReadDeadline(time.Now().Add(4 * time.Second))
	output, readErr := io.ReadAll(master)
	if err := command.Wait(); err != nil {
		t.Fatalf("probe failed: %v %s", err, diagnostic.String())
	}
	if readErr != nil && !strings.Contains(readErr.Error(), "input/output error") {
		t.Fatal(readErr)
	}
	var report terminalProbe
	if err := json.Unmarshal(diagnostic.Bytes(), &report); err != nil {
		t.Fatal(err, diagnostic.String())
	}
	t.Logf("PTY output: %d/%d bytes; shared stdout nonblocking=%t", len(output), report.ExpectedBytes, report.NonblockingOutput)
	if report.NonblockingOutput {
		t.Error("keyboard reader made the shared terminal output nonblocking")
	}
	for index := 0; index < 3; index++ {
		if !bytes.Contains(output, []byte(fmt.Sprintf("FRAME_END_%d Q Back", index))) {
			t.Errorf("frame %d lost its key hints under output backpressure", index)
		}
	}
	if len(output) != report.ExpectedBytes {
		t.Errorf("terminal output truncated: %d/%d", len(output), report.ExpectedBytes)
	}
}
