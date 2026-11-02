package history

import (
	"strings"
	"testing"
)

func TestResetHistoryKeepsOnlyKnownOutcomes(t *testing.T) {
	for _, outcome := range []string{"refused", "rejected", "unknown", "requested", "restart-verified"} {
		event := sanitize(Event{Component: "device", Action: "reset", Phase: "observed", Method: "device.reset.bound", ResetOutcome: outcome})
		if event.ResetOutcome != outcome || event.Method != "device.reset.bound" || !strings.Contains(Describe(event), "reset="+outcome) {
			t.Fatal(event)
		}
	}
	for _, event := range []Event{{Action: "reset", ResetOutcome: "PRIVATE_DEVICE_DATA"}, {Action: "settings", ResetOutcome: "unknown"}} {
		if got := sanitize(event); got.ResetOutcome != "" {
			t.Fatal("unexpected reset history data", got)
		}
	}
	if !TraceMethod("device.reset.bound") {
		t.Fatal("bound reset is not traced")
	}
}
