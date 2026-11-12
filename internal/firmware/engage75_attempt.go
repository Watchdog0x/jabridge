package firmware

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func engage75PhaseRank(phase string) int {
	for n, value := range []string{"pending-inspection", "entry-intent", "bound-boot-attachment", "images-verified", "recovery-bound", "transfer", "returning", "final-verified"} {
		if phase == value {
			return n
		}
	}
	return -1
}

func (p *engage75Pending) advance(phase string) {
	p.Phase = phase
	if phase != "unresolved" && engage75PhaseRank(phase) > engage75PhaseRank(p.LastPhase) {
		p.LastPhase = phase
	}
}
func (p engage75Pending) attemptNumber() int {
	if p.Version == 1 {
		return 1
	}
	return p.Attempt
}

func (p engage75Pending) validate() error {
	legacy := p
	switch p.Version {
	case 1:
		if p.Attempt != 0 || p.LastPhase != "" || p.BootSerial != "" {
			return errors.New("unexpected fields in version 1 Engage 75 inspection")
		}
	case 2:
		if p.Unresolved != (p.Phase == "unresolved") {
			return errors.New("contradictory Engage 75 pending status")
		}
		last := engage75PhaseRank(p.LastPhase)
		if p.Attempt < 1 || p.Attempt > 3 || last < 0 || p.Phase != "unresolved" && p.Phase != p.LastPhase {
			return errors.New("invalid Engage 75 attempt or phase history")
		}
		switch p.BootSerial {
		case "not-observed":
			if last >= 2 {
				return errors.New("missing Engage 75 update connection evidence")
			}
		case "absent", "matched":
			if last < 2 {
				return errors.New("premature Engage 75 update connection evidence")
			}
		default:
			return errors.New("invalid Engage 75 update serial evidence")
		}
		if last >= 2 && p.Entry != "replied" && p.Entry != "possibly-sent" {
			return errors.New("update connection has no confirmed entry observation")
		}
		if last < 6 && (p.HeadReturn != "not-sent" || p.BaseReturn != "not-sent" || p.HeadMainVerified) {
			return errors.New("premature Engage 75 return observation")
		}
		if last == 7 && (p.HeadReturn != "replied" || !p.HeadMainVerified || p.BaseReturn != "replied" && p.BaseReturn != "possibly-sent") {
			return errors.New("incomplete final Engage 75 phase history")
		}
		if last == 0 && p.Entry != "not-sent" {
			return errors.New("premature Engage 75 entry observation")
		}
		legacy.Version, legacy.Attempt, legacy.LastPhase, legacy.BootSerial = 1, 0, "", ""
		if p.Phase == "unresolved" && p.Entry == "not-sent" {
			if last > 1 {
				return errors.New("unsent entry contradicts Engage 75 phase history")
			}
			legacy.Phase, legacy.Unresolved = "pending-inspection", false
		}
	default:
		return errors.New("unknown Engage 75 inspection version")
	}
	return legacy.validateV1()
}

func engage75AttemptTransactionPath() (string, error) {
	path, err := engage75PendingPath()
	return strings.TrimSuffix(path, ".json") + ".new-attempt.json", err
}
func refuseEngage75AttemptTransaction() error {
	path, err := engage75AttemptTransactionPath()
	if err != nil {
		return err
	}
	_, err = os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("an earlier attempt preparation did not finish; keep the result file for checking")
}
func noEngage75RecoveryAuthority() error {
	path, err := firmwareRecoveryStatePath()
	if err != nil {
		return err
	}
	_, err = os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("an existing firmware recovery record must be checked before another attempt")
}

func eligibleEngage75NewAttempt(p engage75Pending) error {
	if err := p.validate(); err != nil {
		return err
	}
	if p.attemptNumber() >= 3 {
		return errors.New("three attempts are already recorded for this firmware; send the result before trying again")
	}
	if p.HeadReturn != "not-sent" || p.BaseReturn != "not-sent" || p.HeadMainVerified {
		return errors.New("the earlier attempt reached device restart; it cannot be restarted automatically")
	}
	phase := p.Phase
	if p.Version == 2 {
		phase = p.LastPhase
	}
	if phase != "unresolved" && phase != "pending-inspection" && phase != "entry-intent" && phase != "bound-boot-attachment" {
		return errors.New("the earlier attempt passed image inspection; its state needs checking")
	}
	return nil
}
func validateEngage75NewAttempt(p engage75Pending, digest string, selected engage75SelectedIdentity) error {
	if err := eligibleEngage75NewAttempt(p); err != nil {
		return err
	}
	if p.ArchiveSHA256 != digest {
		return errors.New("the new attempt needs the exact original firmware archive")
	}
	if p.Selected != selected {
		return errors.New("the base, docked headset or original firmware changed since the earlier attempt")
	}
	return nil
}

func loadEngage75PreviewStart(newAttempt bool) (*engage75Pending, error) {
	if err := refuseEngage75AttemptTransaction(); err != nil {
		return nil, err
	}
	if err := noEngage75RecoveryAuthority(); err != nil {
		return nil, err
	}
	p, err := loadEngage75Pending()
	if errors.Is(err, fs.ErrNotExist) {
		if newAttempt {
			return nil, errors.New("no earlier Engage 75 attempt is saved; a new attempt cannot be verified")
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !newAttempt {
		return nil, errors.New("an earlier Engage 75 attempt is saved; use --new-attempt after the base is back to normal")
	}
	if err := eligibleEngage75NewAttempt(p); err != nil {
		return nil, err
	}
	return &p, nil
}
func checkEngage75PreviewStart() error { _, err := loadEngage75PreviewStart(false); return err }

type engage75AttemptFilesystem struct {
	rename func(string, string) error
	sync   func(string) error
}

func supersedeEngage75Pending(previous, next engage75Pending) error {
	return supersedeEngage75PendingWithFS(previous, next, engage75AttemptFilesystem{
		rename: func(from, to string) error {
			return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
		}, sync: syncFirmwareStateDirectory,
	})
}

// The exclusive firmware lease is held by the caller. Stage the new record
// durably first, preserving a blocking transaction if a crash splits renames.
// Never delete an old record or silently reset its attempt count.
func supersedeEngage75PendingWithFS(previous, next engage75Pending, ops engage75AttemptFilesystem) error {
	if err := validateEngage75NewAttempt(previous, next.ArchiveSHA256, next.Selected); err != nil {
		return err
	}
	if err := next.validate(); err != nil {
		return err
	}
	if next.Version != 2 || next.Phase != "pending-inspection" || next.Attempt != previous.attemptNumber()+1 {
		return errors.New("invalid new Engage 75 attempt")
	}
	if ops.rename == nil || ops.sync == nil {
		return errors.New("missing attempt persistence operations")
	}
	if err := noEngage75RecoveryAuthority(); err != nil {
		return err
	}
	if err := refuseEngage75AttemptTransaction(); err != nil {
		return err
	}
	current, err := loadEngage75Pending()
	if err != nil {
		return err
	}
	if current != previous {
		return errors.New("the saved Engage 75 attempt changed before restart")
	}
	path, err := engage75PendingPath()
	if err != nil {
		return err
	}
	staged, err := engage75AttemptTransactionPath()
	if err != nil {
		return err
	}
	if err := saveFirmwareStateFile(staged, next); err != nil {
		return err
	}
	archived := strings.TrimSuffix(path, ".json") + ".superseded-" + time.Now().UTC().Format("20060102T150405.000000000Z") + ".json"
	if err := ops.rename(path, archived); err != nil {
		cleanup := os.Remove(staged)
		return errors.Join(fmt.Errorf("preserve the earlier attempt: %w", err), cleanup, ops.sync(filepath.Dir(path)))
	}
	if err := ops.sync(filepath.Dir(path)); err != nil {
		return err
	}
	if err := ops.rename(staged, path); err != nil {
		return fmt.Errorf("activate the new attempt: %w", err)
	}
	return ops.sync(filepath.Dir(path))
}
