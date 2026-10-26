package firmware

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func csr7CrashState(identity csrExtendedIdentity, phase string) firmwareRecoveryState {
	return firmwareRecoveryState{FormatVersion: 1, ArchiveSHA256: strings.Repeat("0", 64), ProductName: "Jabra Link 390", FirmwareVersion: "3.2.0", TargetUSBPIDs: []string{"0x2e50"}, Attempt: 1, USBPort: "isolated-port", RuntimePID: 0x2e56, Protocol: 7, Language: 0x409, Phase: phase, TargetIdentitySHA256: extendedRecoveryIdentity(identity)}
}

// The child exits abruptly, without deferred cleanup, at the two uncertain
// commit boundaries. All transport operations use the in-memory peer.
func TestCSR7CommitCrashHelper(t *testing.T) {
	point := os.Getenv("JABRIDGE_TEST_CSR7_CRASH")
	if point == "" {
		t.Skip("subprocess helper")
	}
	backend, image := csr7Fixture()
	if point == "after-commit" {
		backend.peer.onCommit = func() { _ = syscall.Kill(os.Getpid(), syscall.SIGKILL); os.Exit(79) }
	}
	err := runLink390CSR7Update(context.Background(), backend, image, "", "", func(id csrExtendedIdentity, phase string) error {
		if err := saveFirmwareRecoveryState(csr7CrashState(id, phase)); err != nil {
			return err
		}
		if point == "before-commit" && phase == csr7Committing {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			os.Exit(79)
		}
		return nil
	}, nil)
	t.Fatalf("crash point not reached: %v", err)
}

func TestCSR7CrashRecoveryDoesNotEraseOrCommitAgain(t *testing.T) {
	for _, point := range []string{"before-commit", "after-commit"} {
		t.Run(point, func(t *testing.T) {
			t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.Command(binary, "-test.run=^TestCSR7CommitCrashHelper$")
			child.Env = append(os.Environ(), "JABRIDGE_TEST_CSR7_CRASH="+point)
			output, err := child.CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok || !exit.ProcessState.Sys().(syscall.WaitStatus).Signaled() || exit.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("child exit=%v output=%s", err, output)
			}
			state, err := loadFirmwareRecoveryState()
			if err != nil {
				t.Fatal(err)
			}
			if state.Phase != csr7Committing {
				t.Fatal("commit-started record was not durable", state.Phase)
			}
			backend, image := csr7Fixture()
			if point == "before-commit" {
				backend.after.Version = backend.before.Version
			}
			err = runLink390CSR7Update(context.Background(), backend, image, state.Phase, state.TargetIdentitySHA256, func(csrExtendedIdentity, string) error { return fmt.Errorf("unexpected write checkpoint") }, nil)
			if (err == nil) != (point == "after-commit") || len(backend.peer.writes) != 0 {
				t.Fatal("unsafe recovery", err, backend.peer.writes)
			}
			if _, err := loadFirmwareRecoveryState(); err != nil {
				t.Fatal("driver cleared state before caller confirmed success", err)
			}
		})
	}
}
