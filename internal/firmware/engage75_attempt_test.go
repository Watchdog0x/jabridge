package firmware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func originalEngage75Pending(t *testing.T, w *engage75PreviewWorld) engage75Pending {
	t.Helper()
	_, err := w.runtime(context.Background(), w.runtimeDevice, w.archive)
	var partial *dectRuntimeAssessmentError
	if !errors.As(err, &partial) {
		t.Fatal(err)
	}
	selected, err := engage75Selection(partial.Assessment, w.runtimeDevice)
	if err != nil {
		t.Fatal(err)
	}
	w.assessments = 0
	return engage75Pending{Version: 1, Purpose: "engage75-pending-inspection", ArchiveSHA256: strings.Repeat("a", 64), Phase: "unresolved", Selected: selected, Entry: "replied", HeadReturn: "not-sent", BaseReturn: "not-sent", Unresolved: true}
}
func snapshotEngage75State(t *testing.T) map[string]string {
	t.Helper()
	path, err := engage75PendingPath()
	if err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(filepath.Dir(path), file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[file.Name()] = string(data)
	}
	return result
}
func runEngage75NewAttemptModel(w *engage75PreviewWorld, previous engage75Pending, digest string) error {
	store := w.store()
	modelPending := store.pending
	store.previous = &previous
	store.pending = func(p engage75Pending) error {
		if err := saveEngage75Pending(p); err != nil {
			return err
		}
		return modelPending(p)
	}
	store.supersede = func(old, next engage75Pending) error {
		if err := supersedeEngage75Pending(old, next); err != nil {
			return err
		}
		return modelPending(next)
	}
	return runEngage75Preview(context.Background(), w, w.runtimeDevice, w.archive, digest, store, func(target byte, _, _ int) {
		if len(w.stages) == 0 || w.stages[len(w.stages)-1] != target {
			w.stages = append(w.stages, target)
		}
	})
}
func TestEngage75NewAttemptRequalifiesLegacyRecordWithoutDeletion(t *testing.T) {
	enableEngage75PreviewTest(t)
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
	previous := originalEngage75Pending(t, w)
	w.bootWithoutSerial = true
	if err := saveEngage75Pending(previous); err != nil {
		t.Fatal(err)
	}
	before := snapshotEngage75State(t)
	if _, err := loadEngage75PreviewStart(false); err == nil {
		t.Fatal("implicit retry accepted")
	}
	if !reflect.DeepEqual(before, snapshotEngage75State(t)) {
		t.Fatal("refusal changed original record")
	}
	loaded, err := loadEngage75PreviewStart(true)
	if err != nil || loaded == nil || *loaded != previous {
		t.Fatal(err)
	}
	if err := runEngage75NewAttemptModel(w, *loaded, previous.ArchiveSHA256); err != nil {
		t.Fatal(err, w.phases)
	}
	if w.entryCalls != 1 || w.assessments != 2 || !slices.Equal(w.stages, engage75TargetOrder) || w.pending.Version != 2 || w.pending.Attempt != 2 || w.pending.BootSerial != "absent" || w.pending.LastPhase != "final-verified" {
		t.Fatal("fresh attempt did not complete", w.entryCalls, w.pending)
	}
	after := snapshotEngage75State(t)
	archived := 0
	for name, body := range after {
		if strings.Contains(name, ".superseded-") {
			archived++
			if body != before["engage75-pending-inspection.json"] {
				t.Fatal("audit record differs")
			}
		}
	}
	if archived != 1 {
		t.Fatal("original record not preserved", archived)
	}
}
func TestEngage75NewAttemptRefusalsPreserveOriginalFiles(t *testing.T) {
	enableEngage75PreviewTest(t)
	for _, fault := range []string{"archive", "selection", "second-swap", "record", "malformed", "unknown-field", "late-phase", "return", "fourth", "unknown-version"} {
		t.Run(fault, func(t *testing.T) {
			t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
			w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
			old := originalEngage75Pending(t, w)
			switch fault {
			case "selection":
				old.Selected.HeadSerialSHA256 = engage75Digest("another-private-head")
			case "second-swap":
				w.fault = "head-swap"
			case "late-phase":
				old.Version, old.Attempt, old.LastPhase, old.BootSerial = 2, 1, "images-verified", "matched"
			case "return":
				old.HeadReturn = "intent"
			case "fourth":
				old.Version, old.Attempt, old.LastPhase, old.BootSerial = 2, 3, "entry-intent", "not-observed"
			}
			if err := saveEngage75Pending(old); err != nil {
				t.Fatal(err)
			}
			pending, _ := engage75PendingPath()
			switch fault {
			case "record":
				path, _ := firmwareRecoveryStatePath()
				if err := os.WriteFile(path, []byte("malformed recovery authority"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(pending, []byte("broken json"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unknown-field", "unknown-version":
				data, err := os.ReadFile(pending)
				if err != nil {
					t.Fatal(err)
				}
				text := string(data)
				if fault == "unknown-field" {
					text = strings.Replace(text, "{", "{\"unknown\":1,", 1)
				} else {
					text = strings.Replace(text, `"version": 1`, `"version": 99`, 1)
				}
				if err := os.WriteFile(pending, []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotEngage75State(t)
			loaded, err := loadEngage75PreviewStart(true)
			if err == nil {
				digest := old.ArchiveSHA256
				if fault == "archive" {
					digest = strings.Repeat("b", 64)
				}
				err = runEngage75NewAttemptModel(w, *loaded, digest)
			}
			if err == nil || w.entryCalls != 0 || !reflect.DeepEqual(before, snapshotEngage75State(t)) {
				t.Fatal("refusal did not preserve saved evidence", err, w.entryCalls)
			}
		})
	}
}
func TestEngage75NewAttemptPersistenceFailureCannotEnterMode(t *testing.T) {
	enableEngage75PreviewTest(t)
	for _, point := range []int{1, 2} {
		t.Run(fmt.Sprint(point), func(t *testing.T) {
			t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
			w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
			old := originalEngage75Pending(t, w)
			if err := saveEngage75Pending(old); err != nil {
				t.Fatal(err)
			}
			before := snapshotEngage75State(t)
			store := w.store()
			store.previous = &old
			renames := 0
			store.supersede = func(previous, next engage75Pending) error {
				return supersedeEngage75PendingWithFS(previous, next, engage75AttemptFilesystem{rename: func(from, to string) error {
					renames++
					if renames == point {
						return errors.New("injected rename failure")
					}
					return os.Rename(from, to)
				}, sync: syncFirmwareStateDirectory})
			}
			err := runEngage75Preview(context.Background(), w, w.runtimeDevice, w.archive, old.ArchiveSHA256, store, nil)
			if err == nil || w.entryCalls != 0 || renames != point {
				t.Fatal("failure did not reach persistence boundary", err, w.entryCalls, renames)
			}
			after := snapshotEngage75State(t)
			if point == 1 {
				if !reflect.DeepEqual(before, after) {
					t.Fatal("failed first rename changed old files")
				}
			} else {
				if _, err := loadEngage75PreviewStart(true); err == nil {
					t.Fatal("partial transaction permitted a new entry")
				}
				found := false
				for name, body := range after {
					if strings.Contains(name, "superseded-") && body == before["engage75-pending-inspection.json"] {
						found = true
					}
				}
				if !found {
					t.Fatal("partial transaction lost old evidence")
				}
			}
		})
	}
}
func TestEngage75TransitionReasonsAndStrictFinalSerial(t *testing.T) {
	w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
	p := originalEngage75Pending(t, w)
	previous := w.runtimeDevice
	for _, fault := range []string{"empty-serial", "stale", "missing", "port", "resolved-port", "bus", "pid", "vendor", "dongle", "serial"} {
		t.Run(fault, func(t *testing.T) {
			next := previous
			copyAttachment := *previous.attachment
			copyAttachment.fingerprint = "PRIVATE_NEW_FINGERPRINT"
			next.attachment = &copyAttachment
			next.ProductID = 0x1111
			expected := ""
			switch fault {
			case "empty-serial":
				next.Serial = ""
			case "stale":
				next.attachment = previous.attachment
				expected = "new USB"
			case "missing":
				next.attachment = nil
				expected = "could not be identified"
			case "port":
				next.SysPath += "-other"
				expected = "different USB port"
			case "resolved-port":
				next.attachment.realPath = "PRIVATE_OTHER_CONTROLLER"
				expected = "different USB port"
			case "bus":
				next.attachment.bus++
				expected = "different USB port"
			case "pid":
				next.ProductID++
				expected = "different USB device"
			case "vendor":
				next.VendorID++
				expected = "different USB device"
			case "dongle":
				next.ViaDongle = true
				expected = "different USB device"
			case "serial":
				next.Serial = "PRIVATE_WRONG_SERIAL"
				expected = "different USB serial"
			}
			err := bindEngage75Transition(previous, next, p.Selected, 0x1111)
			if expected == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), expected) || !strings.Contains(err.Error(), "update-mode reconnect") || strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatal("missing or unredacted refusal reason", err)
			}
		})
	}
	for _, serial := range []string{"", "PRIVATE_OTHER_RUNTIME"} {
		boot := previous
		b := *previous.attachment
		b.fingerprint = "new-boot"
		boot.attachment = &b
		boot.ProductID = 0x1111
		boot.Serial = ""
		next := previous
		a := *previous.attachment
		a.fingerprint = "new-return"
		next.attachment = &a
		next.Serial = serial
		err := bindEngage75Transition(boot, next, p.Selected, 0x1113)
		if err == nil || !strings.Contains(err.Error(), "normal-mode reconnect") || strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatal("runtime return accepted unknown serial", err)
		}
	}
}
func TestEngage75PendingV2HistoryAndNewAttemptOptions(t *testing.T) {
	enableEngage75PreviewTest(t)
	w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
	old := originalEngage75Pending(t, w)
	p := old
	p.Version, p.Attempt, p.LastPhase, p.BootSerial = 2, 2, "entry-intent", "not-observed"
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"backwards", "serial", "attempt", "unsent-late"} {
		bad := p
		switch fault {
		case "backwards":
			bad.Phase = "pending-inspection"
			bad.Unresolved = false
		case "serial":
			bad.BootSerial = "matched"
		case "attempt":
			bad.Attempt = 4
		case "unsent-late":
			bad.Entry = "not-sent"
			bad.LastPhase = "bound-boot-attachment"
			bad.BootSerial = "absent"
		}
		if bad.validate() == nil {
			t.Fatal("inconsistent phase history accepted", fault)
		}
	}
	_, options, err := extractEngage75Options([]string{"firmware.zip", engage75PreviewFlag, HardwareWriteFlag, engage75NewAttemptFlag})
	if err != nil || !options.preview || !options.newAttempt {
		t.Fatal("new attempt flags rejected", err)
	}
	for _, args := range [][]string{{"firmware.zip", engage75NewAttemptFlag, HardwareWriteFlag}, {"firmware.zip", engage75PreviewFlag, engage75NewAttemptFlag}, {"firmware.zip", engage75PreviewFlag, HardwareWriteFlag, engage75NewAttemptFlag, engage75NewAttemptFlag}} {
		if _, _, err := extractEngage75Options(args); err == nil {
			t.Fatal("incomplete new-attempt consent accepted", args)
		}
	}
}

func TestEngage75MissingBootSerialStillRequiresDisplayAndFinalIdentity(t *testing.T) {
	enableEngage75PreviewTest(t)
	for _, fault := range []string{"display", "runtime-empty", "runtime-wrong"} {
		t.Run(fault, func(t *testing.T) {
			w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
			w.bootWithoutSerial = true
			switch fault {
			case "display":
				w.fault = "display-wrong"
			case "runtime-empty":
				w.returnWithoutSerial = true
			case "runtime-wrong":
				w.returnWrongSerial = true
			}
			err := w.run(context.Background())
			if err == nil {
				t.Fatal("missing boot serial bypassed later identity checks")
			}
			if fault == "display" {
				displayRead := false
				for _, p := range w.packets {
					if p[0] == 3 && p[5] == 0 {
						displayRead = true
					}
					if p[5] != 0 {
						t.Fatalf("effect before all image identities: %x", p)
					}
				}
				if !displayRead || w.recovery != nil || w.head.erases+w.base.erases+w.mmi.erases != 0 {
					t.Fatal("did not reach display guard safely")
				}
			} else if !slices.Equal(w.stages, engage75TargetOrder) || w.finalChecks != 0 || !strings.Contains(err.Error(), "normal-mode reconnect") {
				t.Fatal("final serial refusal did not reach runtime return", err, w.stages)
			}
		})
	}
}

func TestEngage75NewAttemptMissingEvidenceDoesNotStartFresh(t *testing.T) {
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	if _, err := loadEngage75PreviewStart(true); err == nil {
		t.Fatal("explicit new attempt accepted missing evidence")
	}
	if old, err := loadEngage75PreviewStart(false); err != nil || old != nil {
		t.Fatal("ordinary first run rejected", err)
	}
	path, _ := engage75PendingPath()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("preflight created a pending file", err)
	}
}
func TestEngage75PendingV2RejectsHiddenContradictions(t *testing.T) {
	w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
	p := originalEngage75Pending(t, w)
	p.Version, p.Attempt, p.LastPhase, p.BootSerial = 2, 1, "entry-intent", "not-observed"
	p.Entry, p.Unresolved = "not-sent", false
	if p.validate() == nil {
		t.Fatal("compatibility conversion hid contradictory unresolved status")
	}
	p.Entry, p.Unresolved, p.LastPhase, p.BootSerial = "replied", true, "final-verified", "matched"
	if p.validate() == nil {
		t.Fatal("unresolved history skipped final return verification")
	}
}
func TestEngage75SelectionCountsRuntimeAndUpdateDevices(t *testing.T) {
	w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
	old := originalEngage75Pending(t, w)
	boot := w.runtimeDevice
	boot.ProductID = 0x1111
	boot.Serial = ""
	if _, err := selectEngage75PreviewDevice([]USBDevice{w.runtimeDevice, boot}, &old, true, 0x1113); err == nil || !strings.Contains(err.Error(), "found 2") {
		t.Fatal("runtime plus boot device selected ambiguously", err)
	}
	if _, err := selectEngage75PreviewDevice([]USBDevice{boot}, &old, true, 0); err == nil || !strings.Contains(err.Error(), "normal screen") {
		t.Fatal("did not identify saved base in update mode", err)
	}
	if _, err := selectEngage75PreviewDevice([]USBDevice{w.runtimeDevice}, &old, true, 0); err != nil {
		t.Fatal(err)
	}
}
func TestEngage75AttemptSyncFailureBlocksFurtherEntry(t *testing.T) {
	for _, point := range []int{1, 2} {
		t.Run(fmt.Sprint(point), func(t *testing.T) {
			t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
			w := newEngage75PreviewWorld(syntheticEngage75PreviewArchive(), 0x35)
			old := originalEngage75Pending(t, w)
			if err := saveEngage75Pending(old); err != nil {
				t.Fatal(err)
			}
			next := old
			next.Version, next.Attempt, next.Phase, next.LastPhase, next.BootSerial, next.Entry, next.Unresolved = 2, 2, "pending-inspection", "pending-inspection", "not-observed", "not-sent", false
			calls := 0
			err := supersedeEngage75PendingWithFS(old, next, engage75AttemptFilesystem{rename: os.Rename, sync: func(dir string) error {
				calls++
				if calls == point {
					return errors.New("injected directory sync failure")
				}
				return syncFirmwareStateDirectory(dir)
			}})
			if err == nil || calls != point {
				t.Fatal("did not reach sync failure", err, calls)
			}
			if _, err := loadEngage75PreviewStart(false); err == nil {
				t.Fatal("sync failure allowed implicit restart")
			}
			found := false
			for name := range snapshotEngage75State(t) {
				if strings.Contains(name, "superseded-") {
					found = true
				}
			}
			if !found {
				t.Fatal("sync failure lost original audit record")
			}
		})
	}
}
