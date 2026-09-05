package history

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func testRecorder(t *testing.T) (*Recorder, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	return &Recorder{Dir: filepath.Join(t.TempDir(), "history"), clock: func() time.Time { return now }}, &now
}

func TestHistorySurvivesFreshRecorderAndScrubsPrivateStrings(t *testing.T) {
	r, _ := testRecorder(t)
	event := Event{Component: "tui", Action: "key", Phase: "observed", Screen: "headset-settings", USBProduct: 0x4052, Connection: "usb", Method: "PRIVATE_SERIAL", Setting: "PRIVATE_NAME", Error: "PRIVATE_ERROR", Command: "PRIVATE_TOKEN", Input: "PRIVATE_KEY"}
	if err := r.Append(event); err != nil {
		t.Fatal(err)
	}
	fresh := &Recorder{Dir: r.Dir, clock: r.clock}
	events, skipped, err := fresh.Read(200)
	if err != nil || skipped != 0 || len(events) != 1 {
		t.Fatal(events, skipped, err)
	}
	if events[0].USBProduct != 0x4052 || events[0].Screen != "headset-settings" {
		t.Fatal(events[0])
	}
	files, _ := os.ReadDir(r.Dir)
	for _, file := range files {
		path := filepath.Join(r.Dir, file.Name())
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatal(info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "PRIVATE") {
			t.Fatal("private data reached disk")
		}
	}
}

func TestRotationAndAgeRetentionAreBounded(t *testing.T) {
	r, now := testRecorder(t)
	r.limit = 512
	for day := 0; day < 10; day++ {
		for index := 0; index < 20; index++ {
			if err := r.Append(Event{Component: "tui", Action: "key", Phase: "observed", Selection: index}); err != nil {
				t.Fatal(err)
			}
		}
		if day < 9 {
			*now = now.AddDate(0, 0, 1)
		}
	}
	files, err := os.ReadDir(r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range files {
		if !logFileName.MatchString(entry.Name()) {
			continue
		}
		count++
		if !retainedName(entry.Name(), *now) {
			t.Fatalf("expired log retained: %s", entry.Name())
		}
		info, _ := entry.Info()
		if info.Size() > 512 {
			t.Fatal(info.Size())
		}
	}
	if count > RetentionDays*2 {
		t.Fatal(count)
	}
	events, _, err := r.Read(1)
	if err != nil || len(events) != 1 || events[0].Selection != 19 {
		t.Fatal(events, err)
	}
}

func TestSymlinkAndHardlinkTargetsAreRejected(t *testing.T) {
	for _, hard := range []bool{false, true} {
		t.Run(fmt.Sprint(hard), func(t *testing.T) {
			r, now := testRecorder(t)
			if err := os.MkdirAll(r.Dir, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "keep")
			if err := os.WriteFile(target, []byte("keep unchanged"), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(r.Dir, "events-"+now.Format("2006-01-02")+".jsonl")
			var err error
			if hard {
				err = os.Link(target, path)
			} else {
				err = os.Symlink(target, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Append(Event{Action: "run"}); err == nil {
				t.Fatal("linked log accepted")
			}
			data, _ := os.ReadFile(target)
			if string(data) != "keep unchanged" {
				t.Fatal("target changed")
			}
		})
	}
}

func TestLockContentionDoesNotHangCaller(t *testing.T) {
	r, _ := testRecorder(t)
	fd, err := r.openDirectory(true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	unlock, err := r.lock(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	started := time.Now()
	err = r.Append(Event{Component: "tui", Action: "run"})
	if !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("history blocked the caller")
	}
}

func TestClearLeavesUnrelatedFilesAlone(t *testing.T) {
	r, _ := testRecorder(t)
	if err := r.Append(Event{Action: "run"}); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(r.Dir, "notes.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Clear(); err != nil {
		t.Fatal(err)
	}
	events, _, err := r.Read(200)
	if err != nil || len(events) != 0 {
		t.Fatal(events, err)
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "keep" {
		t.Fatal("unrelated file removed")
	}
}

func TestTruncatedTailDoesNotSwallowNextRecord(t *testing.T) {
	r, now := testRecorder(t)
	if err := r.Append(Event{Action: "run", Phase: "start"}); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(r.Dir, "events-"+now.Format("2006-01-02")+".jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"time":`); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if err := r.Append(Event{Action: "run", Phase: "ok"}); err != nil {
		t.Fatal(err)
	}
	events, skipped, err := r.Read(200)
	if err != nil || len(events) != 2 || skipped != 1 {
		t.Fatal(events, skipped, err)
	}
}

func TestDeferredPanicIsNotRecordedAsSuccess(t *testing.T) {
	var completed error
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		var result error
		defer EndDeferred(func(err error) { completed = err }, &result)
		panic("synthetic")
	}()
	if recovered != "synthetic" || !errors.Is(completed, ErrPanic) {
		t.Fatal(recovered, completed)
	}
}

func TestExistingOutputFileHasUsefulPrivateErrorCategory(t *testing.T) {
	if got := Classify(os.ErrExist); got != "already-exists" {
		t.Fatalf("category = %q", got)
	}
}

func TestHistoryChild(t *testing.T) {
	mode := os.Getenv("JABRIDGE_TEST_HISTORY_CHILD")
	if mode == "" {
		return
	}
	r := &Recorder{Dir: os.Getenv("JABRIDGE_TEST_HISTORY_DIR")}
	if mode == "interrupted" {
		if err := r.Append(Event{Component: "tui", Action: "action", Phase: "start", Operation: 7}); err != nil {
			t.Fatal(err)
		}
		os.Exit(17)
	}
	for i := 0; i < 20; i++ {
		for retry := 0; ; retry++ {
			err := r.Append(Event{Component: "ipc-client", Action: "request", Phase: "ok", Operation: uint64(i + 1)})
			if err == nil {
				break
			}
			if !errors.Is(err, unix.EWOULDBLOCK) || retry >= 100 {
				t.Fatal(err)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func childCommand(dir, mode string) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=^TestHistoryChild$")
	command.Env = append(os.Environ(), "JABRIDGE_TEST_HISTORY_CHILD="+mode, "JABRIDGE_TEST_HISTORY_DIR="+dir)
	return command
}

func TestConcurrentProcessesProduceCompleteRecords(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	a, b := childCommand(dir, "write"), childCommand(dir, "write")
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	if err := a.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := b.Wait(); err != nil {
		t.Fatal(err)
	}
	events, skipped, err := (&Recorder{Dir: dir}).Read(200)
	if err != nil || skipped != 0 || len(events) != 40 {
		t.Fatal(len(events), skipped, err)
	}
}

func TestInterruptedProcessLeavesStartForLaterDebug(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	err := childCommand(dir, "interrupted").Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 17 {
		t.Fatal(err)
	}
	events, _, err := (&Recorder{Dir: dir}).Read(200)
	if err != nil || len(events) != 1 || events[0].Phase != "start" || events[0].Operation != 7 {
		t.Fatal(events, err)
	}
}
