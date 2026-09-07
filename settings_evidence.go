package main

import (
	"bytes"
	"fmt"
	"time"

	"github.com/Watchdog0x/jabridge/internal/history"
)

func startSettingWriteEvidence(device *jabra_DeviceInfo, key string) uint64 {
	op := history.NextOperation()
	recordSettingEvidence(device, key, op, "setting-request", nil)
	return op
}

func recordSettingEvidence(device *jabra_DeviceInfo, key string, operation uint64, action string, err error) {
	entry := historyDeviceEvent(device, action)
	entry.Setting, entry.Operation = key, operation
	entry.Phase = "ok"
	if action == "setting-request" {
		entry.Phase = "start"
	}
	if err != nil {
		entry.Phase, entry.Error = "error", history.Classify(err)
	}
	history.Record(entry)
}

func writeSettingEvidenceSummary(out *bytes.Buffer, events []history.Event) {
	type operation struct {
		at      time.Time
		pid     uint16
		setting string
		ack     string
		read    string
	}
	var order []string
	operations := map[string]*operation{}
	for _, event := range events {
		if event.Action != "setting-request" && event.Action != "setting-ack" && event.Action != "setting-readback" {
			continue
		}
		if event.Operation == 0 || event.Setting == "" {
			continue
		}
		key := fmt.Sprintf("%s/%d", event.Session, event.Operation)
		op := operations[key]
		if op == nil {
			op = &operation{at: event.Time, pid: event.USBProduct, setting: event.Setting, ack: "NOT OBSERVED", read: "NOT OBSERVED"}
			operations[key] = op
			order = append(order, key)
		}
		if op.pid != event.USBProduct || op.setting != event.Setting {
			continue
		}
		state := "NOT OBSERVED"
		switch event.Phase {
		case "ok":
			state = "PASS"
		case "error":
			state = "FAIL (" + event.Error + ")"
		}
		switch event.Action {
		case "setting-ack":
			op.ack = state
		case "setting-readback":
			op.read = state
		}
	}
	fmt.Fprintln(out, "\nRecorded setting write evidence (latest 12 retained operations):")
	if len(order) == 0 {
		fmt.Fprintln(out, "No staged write evidence recorded. Earlier app versions cannot provide it retroactively.")
	}
	for _, key := range order[max(0, len(order)-12):] {
		op := operations[key]
		fmt.Fprintf(out, "%s USB 0b0e:%04x setting %s: device ACK=%s; matching readback=%s\n", op.at.UTC().Format(time.RFC3339), op.pid, op.setting, op.ack, op.read)
	}
	fmt.Fprintln(out, "These are historical operations, not new tests or a promise about another unit with the same USB model ID.")
	fmt.Fprintln(out, "Persistence after restart: NOT TESTED automatically. Compare the setting before and after a real device restart.")
	fmt.Fprintln(out, "Physical behavior: NOT TESTED by readback. Buttons, audio and voice announcements need separate checks.")
}
