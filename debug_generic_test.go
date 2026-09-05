package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/firmware"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
	"golang.org/x/sys/unix"
)

func TestUnknownHIDReportFiveIsNotAssumedToBeGNP(t *testing.T) {
	reports := []firmware.HIDReport{{ID: 5, Kind: "input", Bytes: 3, Fields: []firmware.HIDField{{OffsetBits: 0, SizeBits: 1, Count: 1, UsagePage: 0xff30, Usages: []uint32{0x20}, Flags: 2}}}}
	activity := hidActivityForReports(reports)
	activity.observe([]byte{5, 0, 0}, map[byte]int{5: 3})
	activity.observe([]byte{5, 1, 0}, map[byte]int{5: 3})
	text := activity.summary("hidraw-test")
	if strings.Contains(text, "GNP event") || !strings.Contains(text, "bit0:page=ff30,usage=0020") || !strings.Contains(text, "changed-bits=[0]") {
		t.Fatal(text)
	}
}

func TestGenericHIDFieldsMapRangesWithoutInventingArrayValues(t *testing.T) {
	report := firmware.HIDReport{Fields: []firmware.HIDField{
		{OffsetBits: 0, SizeBits: 1, Count: 4, UsagePage: 0x000b, UsageMin: 0x20, UsageMax: 0x23, Flags: 2},
		{OffsetBits: 4, SizeBits: 4, Count: 1, UsagePage: 0x000b, UsageMin: 0xb0, UsageMax: 0xbb, Flags: 0},
	}}
	got := describeHIDChanges(report, []int{1, 5})
	if !strings.Contains(got, "usage=0021") || !strings.Contains(got, "array-range=b0..bb") {
		t.Fatal(got)
	}
}

func TestDebugButtonCodesExcludeTypingAndRawScancodes(t *testing.T) {
	for _, event := range []struct {
		kind, code uint16
		value      int32
	}{{unix.EV_KEY, 30, 1}, {unix.EV_MSC, 4, 12345}, {unix.EV_KEY, 0x100, 999}} {
		if label := debugMediaEvent(event.kind, event.code, event.value); label != "" {
			t.Fatal(label)
		}
	}
	if got := debugMediaEvent(unix.EV_KEY, 0x100, 1); got != "Unmapped button: pressed" {
		t.Fatal(got)
	}
	if got := debugMediaEvent(unix.EV_REL, 0x0b, -120); !strings.Contains(got, "-120") {
		t.Fatal(got)
	}
}

func TestHIDIdentityIncludesBluetoothAndIgnoresPrivateFields(t *testing.T) {
	bus, pid, ok := diagnosticHIDIdentity("HID_ID=0005:00000B0E:0000ABCD\nHID_UNIQ=PRIVATE\nHID_NAME=PRIVATE")
	if !ok || bus != 5 || pid != 0xabcd {
		t.Fatal(bus, pid, ok)
	}
	node := diagnosticHIDNode{Path: "/dev/hidraw3", Bus: bus, PID: pid}
	if label := node.label(); strings.Contains(label, "PRIVATE") || !strings.Contains(label, "system-bluetooth") {
		t.Fatal(label)
	}
	if _, _, ok := diagnosticHIDIdentity("HID_ID=0003:0000ABCD:00000B0E"); ok {
		t.Fatal("non-Jabra identity accepted")
	}
}

func TestCandidateProfileCannotClaimNativeReadOrUsePrivateDefaults(t *testing.T) {
	var out bytes.Buffer
	writeCandidateProfile(&out, modelcatalog.ProfileEvidence{PID: 0x9999, Variant: "01-01", Firmware: "1.0.0", Sections: map[string]int{"settings": 2}, Definitions: []modelcatalog.Definition{
		{Section: "settings", ID: "NEW_SETTING", Properties: []string{"newFutureProperty"}, Choices: []string{"one", "two", "three"}},
		{Section: "settings", ID: "VOICE", Properties: []string{"voicePrompts"}},
	}})
	text := out.String()
	if !strings.Contains(text, "native-mapping=missing") || !strings.Contains(text, "query=13/3a") || strings.Contains(text, "PASS") {
		t.Fatal(text)
	}
}

func TestGuideAdaptsToAbsentAndDifferentPhysicalControls(t *testing.T) {
	steps, err := guidedControlSteps("")
	if err != nil || len(steps) != 0 {
		t.Fatal(steps, err)
	}
	steps, err = guidedControlSteps("3 7 9 9")
	if err != nil || len(steps) != 5 || steps[1].Label != "mute" || steps[2].Label != "wheel-up" || steps[4].Label != "extra-2" {
		t.Fatal(steps, err)
	}
	for _, step := range steps {
		if step.Label == "call" || step.Label == "volume-up" {
			t.Fatal("invented absent control", step)
		}
	}
	if _, err := guidedControlSteps("PRIVATE headset name"); err == nil {
		t.Fatal("free text was accepted into report labels")
	}
	steps, err = guidedControlSteps("10 11 12 13")
	if err != nil || len(steps) != 5 || steps[1].Label != "boom-up" || steps[2].Label != "boom-down" || steps[3].Label != "microphone-out" || steps[4].Label != "microphone-in" {
		t.Fatal(steps, err)
	}
}

func TestControlIPCObservationOmitsPrivateIdentityFields(t *testing.T) {
	text := controlEventIdentity(map[string]any{"pid": float64(0x4052), "id": float64(4), "connection": "usb", "serial": "PRIVATE_SERIAL", "name": "PRIVATE_NAME"})
	if !strings.Contains(text, "4052") || strings.Contains(text, "PRIVATE") {
		t.Fatal(text)
	}
}
