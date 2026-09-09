package buttons

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveReadOnlyJabraInputDiscovery(t *testing.T) {
	if os.Getenv("JABRIDGE_BUTTON_READ_ONLY_TEST") != "1" {
		t.Skip("explicit read-only hardware test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var sources []Source
	NativeMonitor(ctx, func(found []Source) { sources = found }, func(Observation) {})
	if len(sources) == 0 {
		t.Fatal("no connected Jabra HID nodes")
	}
	for _, source := range sources {
		t.Logf("PID=%04x ready=%t controls=%d error=%s", source.PID, source.Ready, len(source.Controls), source.Error)
		for _, control := range source.Controls {
			t.Logf("report=%d usage=%04x control=%s constant=%t music-eligible=%t", control.Report, control.Usage, control.Name, control.Constant, control.MusicEligible)
		}
	}
}
