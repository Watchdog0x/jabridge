package history

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestControlHistoryRoundtripAndPrivacy(t *testing.T) {
	r := &Recorder{Dir: filepath.Join(t.TempDir(), "history")}
	event := Event{Component: "device", Action: "button", Phase: "observed", Control: "microphone-mute", ControlState: "active", USBProduct: 0x0422, HIDPage: 0xb, HIDUsage: 0x2f, HIDReport: 3}
	if err := r.Append(event); err != nil {
		t.Fatal(err)
	}
	entries, _, err := r.Read(10)
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	line := Describe(entries[0])
	if !strings.Contains(line, "control=microphone-mute state=active") || !strings.Contains(line, "usage=002f") {
		t.Fatal(line)
	}
	unsafe := sanitize(Event{Component: "device", Action: "button", Control: "PRIVATE_TYPED_TEXT", ControlState: "PASSWORD", HIDPage: 7, HIDUsage: 65, HIDReport: 1})
	if strings.Contains(Describe(unsafe), "PRIVATE") || unsafe.HIDPage != 0 || unsafe.HIDUsage != 0 {
		t.Fatal(unsafe)
	}
}
