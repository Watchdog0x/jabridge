package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/history"
)

func editorFixture(t *testing.T, text bool) deviceSettingValue {
	t.Helper()
	withMenuState(t)
	old := activeSettingEditor
	t.Cleanup(func() { activeSettingEditor = old })
	width, height = 100, 30
	remote := &remoteSettingValue{Device: "headset", Key: "headset-name", Label: "Headset name", Value: "Original", Editable: true, Kind: "text", MaxBytes: 32,
		Target: &ipc.SettingTarget{ID: 1, Instance: strings.Repeat("a", 32)}}
	if !text {
		remote.Kind = "choice"
		remote.Key = "call-button"
		remote.Value = "Mute"
		remote.Choices = []string{"Busylight", "Call handling", "Mute", "Push to talk", "Speed dial", "None"}
	}
	return deviceSettingValue{Remote: remote}
}

func feedEditor(t *testing.T, decoder *keyDecoder, text []byte, results chan<- actionResult) {
	t.Helper()
	for _, event := range decoder.feed(text) {
		if handleKeyEvent(event, results) {
			t.Fatal("editor input quit the TUI")
		}
	}
}

func TestTextEditorKeepsQDigitsUnicodeAndDoesNotWriteOnCancel(t *testing.T) {
	setting := editorFixture(t, true)
	results := make(chan actionResult, 1)
	if err := openSettingEditor(settingScopeHeadset, setting, &jabra_DeviceInfo{deviceName: "Test"}); err != nil {
		t.Fatal(err)
	}
	decoder := &keyDecoder{rawText: true}
	feedEditor(t, decoder, []byte("Work q123 ws \xc3"), results)
	feedEditor(t, decoder, []byte("\xa6"), results)
	if activeSettingEditor.text != "Work q123 ws æ" {
		t.Fatal(activeSettingEditor.text)
	}
	handleKeyEvent(keyBackspace, results)
	if !strings.HasSuffix(activeSettingEditor.text, " ") {
		t.Fatal("UTF-8 backspace split a codepoint")
	}
	handleKeyEvent(keyEscape, results)
	if activeSettingEditor != nil || len(results) != 0 || setting.Remote.Value != "Original" {
		t.Fatal("cancel changed a setting")
	}
}

func TestTextSettingListPreservesCase(t *testing.T) {
	setting := editorFixture(t, true)
	setting.Remote.Value = "Office q123 æ"
	if got := formatDeviceSetting(setting); got != "Headset name: Office q123 æ" {
		t.Fatal(got)
	}
}

func TestTextEditorHistoryPrivacy(t *testing.T) {
	if os.Getenv("JABRIDGE_EDITOR_HISTORY_CHILD") != "1" {
		root := t.TempDir()
		command := exec.Command(os.Args[0], "-test.run=^TestTextEditorHistoryPrivacy$")
		command.Env = append(os.Environ(), "JABRIDGE_EDITOR_HISTORY_CHILD=1", "JABRIDGE_HISTORY=on", "STATE_DIRECTORY=", "XDG_STATE_HOME="+root)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatal(string(output), err)
		}
		files, _ := filepath.Glob(filepath.Join(root, "jabridge", "history", "events-*.jsonl"))
		if len(files) == 0 {
			t.Fatal("privacy test did not record its control event")
		}
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "Private Qw123") {
				t.Fatal("typed text leaked into history")
			}
		}
		return
	}
	history.Configure("1.0.0-rc.23")
	history.Record(history.Event{Component: "tui", Action: "connect", Phase: "observed"})
	setting := editorFixture(t, true)
	results := make(chan actionResult, 1)
	if err := openSettingEditor(settingScopeHeadset, setting, &jabra_DeviceInfo{}); err != nil {
		t.Fatal(err)
	}
	feedEditor(t, &keyDecoder{rawText: true}, []byte("Private Qw123"), results)
	handleKeyEvent(keyEscape, results)
}

func TestTextEditorPasteCannotSubmitAndRespectsByteLimit(t *testing.T) {
	setting := editorFixture(t, true)
	setting.Remote.MaxBytes = 8
	results := make(chan actionResult, 1)
	if err := openSettingEditor(settingScopeHeadset, setting, &jabra_DeviceInfo{}); err != nil {
		t.Fatal(err)
	}
	feedEditor(t, &keyDecoder{rawText: true}, []byte("\x1b[200~q123\nwsabcdefghijkl\x1b[201~"), results)
	if activeSettingEditor == nil || len(results) != 0 || len(activeSettingEditor.text) > 8 {
		t.Fatal("paste submitted or exceeded text limit")
	}
	for _, event := range (&keyDecoder{rawText: true}).feed([]byte("\x1b[200~22q\x1b[201~")) {
		if navigationKey(event) != keyNone {
			t.Fatal("paste became a menu action")
		}
	}
}

func TestChoicePickerOnlySavesOnEnter(t *testing.T) {
	setting := editorFixture(t, false)
	results := make(chan actionResult, 1)
	if err := openSettingEditor(settingScopeHeadset, setting, &jabra_DeviceInfo{}); err != nil {
		t.Fatal(err)
	}
	saved := make(chan string, 1)
	activeSettingEditor.save = func(value string) error { saved <- value; return nil }
	handleKeyEvent(keyDown, results)
	if len(saved) != 0 || activeSettingEditor.choice != 3 {
		t.Fatal("moving selection wrote a value")
	}
	handleKeyEvent(keyEnter, results)
	select {
	case got := <-saved:
		if got != "Push to talk" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("save did not run")
	}
	select {
	case result := <-results:
		if result.err != nil {
			t.Fatal(result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("no save result")
	}
	if activeSettingEditor != nil {
		t.Fatal("editor did not close")
	}
}

func TestEscapeAndModifiedCSISequencesDoNotLeakCharacters(t *testing.T) {
	d := &keyDecoder{rawText: true}
	if events := d.feed([]byte{27}); len(events) != 0 {
		t.Fatal(events)
	}
	if got := d.flushEscape(time.Now().Add(time.Second)); got != keyEscape {
		t.Fatal(got)
	}
	for _, event := range d.feed([]byte("\x1b[1;5A\x1b[31m")) {
		if event != keyUp {
			t.Fatal("CSI parameters became text", event)
		}
	}
	if events := d.feed([]byte("\x1b[113;1:3u")); len(events) != 0 {
		t.Fatal("key release repeated an action")
	}
}

func TestSettingTargetRejectsRecycledIDsAndServiceInstances(t *testing.T) {
	withDeviceState(t, make(devices), -1, -1)
	first := &jabra_DeviceInfo{productID: 0x24b7, deviceName: "Test"}
	addDevice(first)
	old := settingTarget(deviceForID(first.deviceID))
	deviceStateMu.Lock()
	delete(deviceManager, int(first.deviceID))
	deviceStateMu.Unlock()
	replacement := &jabra_DeviceInfo{productID: 0x24b7, deviceName: "Test"}
	addDevice(replacement)
	if first.deviceID != replacement.deviceID {
		t.Fatal("test did not reuse numeric ID")
	}
	if err := validateSettingTarget(deviceForID(replacement.deviceID), old); err == nil {
		t.Fatal("accepted a stale editor")
	}
	if err := validateSettingTarget(deviceForID(replacement.deviceID), settingTarget(replacement)); err != nil {
		t.Fatal(err)
	}
}

func TestPickerScrollAndTextHintRender(t *testing.T) {
	setting := editorFixture(t, false)
	setting.Remote.Choices = nil
	for i := range 30 {
		setting.Remote.Choices = append(setting.Remote.Choices, strings.Repeat("x", i+1))
	}
	if err := openSettingEditor(settingScopeHeadset, setting, &jabra_DeviceInfo{}); err != nil {
		t.Fatal(err)
	}
	f := newRenderTarget(t, 100, 30)
	activeSettingEditor.choice = 29
	renderSettingEditor()
	_, _, bottom := panelBounds()
	if !strings.Contains(rowText(f, bottom-2), strings.Repeat("x", 30)) {
		t.Fatal("last choice is not visible")
	}
}
