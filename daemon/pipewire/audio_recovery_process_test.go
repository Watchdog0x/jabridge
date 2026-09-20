package pipewire

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoveryCaptureProcessTargetsOneMicrophoneAndStops(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	t.Setenv("PATH", dir)
	t.Setenv("RECOVERY_TEST_ARGS", argsPath)
	for name, script := range map[string]string{
		"pw-cli": "#!/bin/sh\nexit 0\n",
		"pw-cat": "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RECOVERY_TEST_ARGS\"\nprintf 'simulated microphone data'\nexec /bin/sleep 30\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	capture, err := startRecoveryCapture(ctx, "101", "jabridge-recovery-test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = capture.Close() }()
	deadline := time.Now().Add(time.Second)
	var args []byte
	for time.Now().Before(deadline) {
		args, _ = os.ReadFile(argsPath)
		if len(args) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, want := range []string{"--target\n101\n", "--record", "--raw", `"node.dont-fallback":true`, `"node.dont-reconnect":true`, `"node.dont-move":true`} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("missing %q in %s", want, args)
		}
	}
	if !capture.Alive() {
		t.Fatal("capture exited before cancellation")
	}
	cancel()
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	if capture.Alive() {
		t.Fatal("capture left running after cancellation")
	}
}

func TestRecoveryNodeCommandRequiresPayloadAndChecksStderr(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			sent := filepath.Join(dir, "sent")
			t.Setenv("RECOVERY_NODE_SENT", sent)
			script := "#!/bin/sh\nif [ \"$#\" -ne 4 ] || [ \"$4\" != '{}' ]; then printf 'Error: command-json is required\\n' >&2; exit 0; fi\n"
			if reject {
				script += "printf 'remote 0: error id:1 PRIVATE_PATH\\n' >&2\n"
			}
			script += "printf sent > \"$RECOVERY_NODE_SENT\"\nexit 0\n"
			if err := os.WriteFile(filepath.Join(dir, "pw-cli"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			err := recoveryNodeCommand(context.Background(), 10, "Suspend")
			if _, sentErr := os.Stat(sent); sentErr != nil {
				t.Fatal("node command was not sent", sentErr)
			}
			if (err != nil) != reject {
				t.Fatal("wrong result for zero-exit command", err)
			}
			if err != nil && strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("private stderr exposed", err)
			}
		})
	}
}

func TestRecoveryCommandStderrRemainsBoundedThroughCopy(t *testing.T) {
	var stderr recoveryCommandStderr
	if _, err := io.Copy(&stderr, strings.NewReader(strings.Repeat("x", 2<<20))); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 4096 {
		t.Fatal("stderr capture exceeded its bound", stderr.Len())
	}
}
