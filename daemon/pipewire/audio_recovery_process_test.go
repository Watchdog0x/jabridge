package pipewire

import (
	"context"
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
