package firmware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/history"
)

func TestHIDOpenFailureSurvivesTimeoutWithoutPrivateData(t *testing.T) {
	for _, test := range []struct {
		stage string
		cause error
		want  string
	}{
		{"hid-open", os.ErrPermission, "hid-open-permission"},
		{"hid-open", os.ErrNotExist, "hid-open-missing"},
		{"hid-layout", errors.New("PRIVATE descriptor"), "hid-layout"},
		{"hid-identity", errors.New("PRIVATE serial"), "hid-identity"},
		{"hid-handshake", context.DeadlineExceeded, "hid-handshake-timeout"},
	} {
		err := fmt.Errorf("bootloader did not become ready: %w: %w", hidAccessFailure(test.stage, test.cause), context.DeadlineExceeded)
		if !errors.Is(err, context.DeadlineExceeded) || HIDAccessFailureCode(err) != test.want || history.Classify(err) != test.want {
			t.Fatal(test, err, history.Classify(err))
		}
		recorder := &history.Recorder{Dir: filepath.Join(t.TempDir(), "history")}
		if err := recorder.Append(history.Event{Component: "firmware", Action: "sitel-open-boot", Phase: "error", Error: history.Classify(err)}); err != nil {
			t.Fatal(err)
		}
		events, _, err := recorder.Read(1)
		if err != nil || len(events) != 1 || events[0].Error != test.want || strings.Contains(fmt.Sprint(events), "PRIVATE") {
			t.Fatal(events, err)
		}
	}
}

func TestSitelOpenRejectsChangedBindingBeforeAccess(t *testing.T) {
	d := testBoundUSB(t)
	if err := os.WriteFile(filepath.Join(d.SysPath, "devnum"), []byte("3"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := openSitelFirmwareHIDAt(d, hidrawPaths{class: "/does-not-exist"})
	if HIDAccessFailureCode(err) != "hid-usb-binding" {
		t.Fatal(err)
	}
}

func TestHIDOpenCancellationRemainsCancellation(t *testing.T) {
	err := fmt.Errorf("open stopped: %w: %w", hidAccessFailure("hid-layout", errors.New("unmatched report")), context.Canceled)
	if history.Classify(err) != "cancelled" || HIDAccessFailureCode(err) != "hid-layout" {
		t.Fatal(err)
	}
}
