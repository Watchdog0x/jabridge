package firmware

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDECTPublicResultFixtureMatchesRealInspection(t *testing.T) {
	w := newEngage75World(syntheticEngage75Archive(), 0x35)
	w.headSerial = "PRIVATE_COMPONENT_SERIAL"
	device := w.base.device()
	device.Serial = "PRIVATE_USB_SERIAL"
	report, err := inspectDECTComponents(context.Background(), device, &engage75ManagementPeer{world: w}, func() error { return nil }, time.Millisecond, time.Second)
	if err != nil || !report.IdentityPassed {
		t.Fatal(report.InstallerIdentity, err)
	}
	for i := range report.Checks {
		report.Checks[i].ElapsedMS = 0
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("PRIVATE_")) {
		t.Fatal("real inspection result contains serial")
	}
	expected, err := os.ReadFile("testdata/dect-component-public-result.json")
	if err != nil {
		t.Fatalf("Missing public fixture; GENERATED_FIXTURE=%s", base64.StdEncoding.EncodeToString(data))
	}
	if strings.TrimSpace(string(expected)) != string(data) {
		t.Fatal("public fixture does not match the real inspection output")
	}
}
