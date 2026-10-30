package firmware

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func TestCSRLanguageCrashJournal(t *testing.T) {
	if point := os.Getenv("JABRIDGE_LANGUAGE_CRASH_TEST"); point != "" {
		backend, plan := languageDriverFixture()
		kill := func() { _ = syscall.Kill(os.Getpid(), syscall.SIGKILL); os.Exit(79) }
		if point == "after-commit-write" {
			backend.peer.afterCommitWrite = kill
		}
		state := firmwareRecoveryState{FormatVersion: 1, ArchiveSHA256: strings.Repeat("a", 64), ProductName: "Jabra Evolve3 65", FirmwareVersion: "1.0.1", TargetUSBPIDs: []string{"0x2549"}, Attempt: 1, RuntimePID: 0x2549, Protocol: 17, Language: 0x407, USBPort: "isolated-usb", TargetIdentitySHA256: extendedRecoveryIdentity(backend.before)}
		err := runCSRLanguageUpdate(context.Background(), backend, plan, "", "", "", func(phase string, connection csrLanguageConnection) error {
			state.Phase = phase
			state.CSRLanguage = &csrLanguageRecovery{SourceAttachment: connection.Identity.Attachment, SourceLanguage: connection.Identity.Language, SourceTunesVersion: connection.TunesVersion, TuneVersion: "0.2.1"}
			if err := saveFirmwareRecoveryState(state); err != nil {
				return err
			}
			if point == phase {
				kill()
			}
			return nil
		}, nil)
		t.Fatal("crash hook did not terminate the process", err)
	}
	for _, point := range []string{csrLanguageTransferring, csrLanguageCommitting, "after-commit-write"} {
		t.Run(point, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", directory)
			command := exec.Command(os.Args[0], "-test.run=^TestCSRLanguageCrashJournal$")
			command.Env = append(os.Environ(), "JABRIDGE_LANGUAGE_CRASH_TEST="+point)
			output, err := command.CombinedOutput()
			var exited *exec.ExitError
			if !errors.As(err, &exited) {
				t.Fatal("child did not crash", err, string(output))
			}
			status, ok := exited.Sys().(syscall.WaitStatus)
			if !ok || status.Signal() != syscall.SIGKILL {
				t.Fatal("unexpected child exit", err, string(output))
			}
			state, err := loadFirmwareRecoveryState()
			if err != nil || state.CSRLanguage == nil {
				t.Fatal("durable crash record missing", err)
			}
			wanted := point
			if point == "after-commit-write" {
				wanted = csrLanguageCommitting
			}
			if state.Phase != wanted {
				t.Fatal("wrong crash phase", state.Phase, wanted)
			}
			if state.Phase == csrLanguageCommitting {
				backend, plan := languageDriverFixture()
				err := runCSRLanguageUpdate(context.Background(), backend, plan, state.Phase, state.CSRLanguage.SourceAttachment, state.TargetIdentitySHA256, func(string, csrLanguageConnection) error {
					t.Fatal("crash recovery tried to save/replay transfer")
					return nil
				}, nil)
				if err == nil || len(backend.peer.commands) != 0 {
					t.Fatal("unconfirmed crash recovery wrote commands", err, backend.peer.commands)
				}
			}
		})
	}
}

func TestCSRLanguageMalformedRecordIsRejectedByLoader(t *testing.T) {
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	backend, _ := languageDriverFixture()
	valid := firmwareRecoveryState{FormatVersion: 1, ArchiveSHA256: strings.Repeat("a", 64), ProductName: "Jabra Evolve3 65", FirmwareVersion: "1.0.1", TargetUSBPIDs: []string{"0x2549"}, Attempt: 1, RuntimePID: 0x2549, Protocol: 17, Language: 0x407, USBPort: "isolated-usb", TargetIdentitySHA256: extendedRecoveryIdentity(backend.before), Phase: csrLanguageCommitting, CSRLanguage: &csrLanguageRecovery{SourceAttachment: "before", SourceLanguage: 0x409, SourceTunesVersion: "0.2.1", TuneVersion: "0.2.1"}}
	for _, damage := range []func(*firmwareRecoveryState){
		func(s *firmwareRecoveryState) { s.Phase = "" },
		func(s *firmwareRecoveryState) { s.TargetIdentitySHA256 = "" },
		func(s *firmwareRecoveryState) { s.Protocol = 7 },
		func(s *firmwareRecoveryState) { s.RuntimePID = 0x2e56 },
		func(s *firmwareRecoveryState) { s.CSRLanguage.SourceAttachment = "" },
		func(s *firmwareRecoveryState) { s.CSRLanguage.SourceTunesVersion = "" },
	} {
		state := valid
		info := *valid.CSRLanguage
		state.CSRLanguage = &info
		damage(&state)
		if err := saveFirmwareRecoveryState(state); err != nil {
			t.Fatal(err)
		}
		if _, err := loadFirmwareRecoveryState(); err == nil {
			t.Fatal("loader accepted a malformed language recovery record")
		}
	}
}
