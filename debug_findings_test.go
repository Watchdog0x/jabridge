package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/firmware"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func TestExplicitCatalogReadOnlyCannotEnableSettingsWrites(t *testing.T) {
	for _, access := range []string{"ReadOnly", "Protected", "None", "unknown"} {
		if catalogAllowsSettingWrite(modelcatalog.Property{Access: access}) {
			t.Fatalf("write allowed for %s", access)
		}
	}
	if !catalogAllowsSettingWrite(modelcatalog.Property{Access: "ReadWrite"}) {
		t.Fatal("read/write property blocked")
	}
}

func TestHIDActivityOmitsValuesAndGNPSerialReplies(t *testing.T) {
	activity := newHIDActivity()
	reports := map[byte]int{1: 3, 5: 64}
	activity.observe([]byte{1, 0, 0}, reports)
	activity.observe([]byte{1, 4, 0}, reports)
	serial := make([]byte, 64)
	serial[0] = 5
	serial[4] = 0xc0
	serial[5] = 2
	serial[6] = 1
	copy(serial[7:], "PRIVATE_SERIAL")
	activity.observe(serial, reports)
	summary := activity.summary("hidraw-test")
	if !strings.Contains(summary, "changed-bits=[2]") || strings.Contains(summary, "PRIVATE") || strings.Contains(summary, "report=5") {
		t.Fatal(summary)
	}
}

func TestServiceHistoryKeepsFailureCategoryWithoutPrivateLogText(t *testing.T) {
	got := strings.Join(serviceFailureCategories("open /home/private-name/secret: permission denied\npanic: PRIVATE_TOKEN"), "\n")
	if strings.Contains(got, "private-name") || strings.Contains(got, "PRIVATE") || !strings.Contains(got, "permission denied") || !strings.Contains(got, "panic") {
		t.Fatal(got)
	}
}

func TestTimeoutAdviceDoesNotPrescribeRootOrUdev(t *testing.T) {
	steps := strings.Join(reportNextSteps("native installed firmware: device reply timed out"), "\n")
	if strings.Contains(steps, "run jabridge setup") || !strings.Contains(steps, "packet framing") {
		t.Fatal(steps)
	}
	permissions := strings.Join(reportNextSteps("hidraw7: permission denied"), "\n")
	if !strings.Contains(permissions, "run jabridge setup on the host") {
		t.Fatal(permissions)
	}
}

func TestFirmwareFindingNeverQualifiesEvolve3OrSpeakFlashing(t *testing.T) {
	for _, protocol := range []int{1, 16, 17} {
		got := nativeFirmwareFinding(firmware.FirmwareDiagnostic{Protocols: []int{protocol}, Cached: true, ChecksumMatches: true})
		if !strings.Contains(got, "NOT IMPLEMENTED") {
			t.Fatal(got)
		}
	}
	got := nativeFirmwareFinding(firmware.FirmwareDiagnostic{Protocols: []int{7}, Cached: true, NativeLayout: true})
	if !strings.Contains(got, "NOT TESTED") {
		t.Fatal(got)
	}
}

func TestFirmwareReportDeduplicatesPhysicalAndServiceInventory(t *testing.T) {
	old := diagnoseFirmwareFile
	t.Cleanup(func() { diagnoseFirmwareFile = old })
	calls := 0
	diagnoseFirmwareFile = func(ctx context.Context, pid uint16, dir string) (firmware.FirmwareDiagnostic, error) {
		calls++
		return firmware.FirmwareDiagnostic{Latest: firmware.LatestInfo{Version: "2.32.8"}, Protocols: []int{1}, ChecksumPublished: true}, nil
	}
	var output bytes.Buffer
	writeFirmwareDiagnostic(&output, []uint16{0x0422, 0x0422})
	if calls != 1 || !strings.Contains(output.String(), "2.32.8") || !strings.Contains(output.String(), "NOT IMPLEMENTED") {
		t.Fatal(output.String(), calls)
	}
}

func TestInputBitmapUsesKernelWordOrder(t *testing.T) {
	word := fmt.Sprintf("%x", uint64(1)<<uint(113%nativeWordBits))
	words := []string{word}
	for i := 0; i < int(113/nativeWordBits); i++ {
		words = append(words, "0")
	}
	names := inputCapabilityNames(strings.Join(words, " "))
	if len(names) != 1 || names[0] != "Mute" {
		t.Fatal(names)
	}
}

func TestSpatialSettingRejectsTruncationAndPreservesOtherControls(t *testing.T) {
	definition, found := findBoolSettingDefinition(settingScopeHeadset, "spatial-call-audio")
	if !found {
		t.Fatal("missing setting")
	}
	if _, err := decodeBoolSettingPayload(definition, []byte{1}); err == nil {
		t.Fatal("truncated spatial response accepted")
	}
	got, err := mergeSettingPayload([]byte{0xa0, 0xb1, 0x44}, definition.ResponseIndex, definition.BitMask, 1)
	if err != nil || !bytes.Equal(got, []byte{0xa1, 0xb1, 0x44}) {
		t.Fatal(got, err)
	}
}

func TestANCChoicesExcludeCalibrationAndUseDocumentedWireValues(t *testing.T) {
	for _, definition := range headsetChoiceSettingDefinitions {
		if definition.Key != "noise-control" {
			continue
		}
		if definition.Op != 0xbe || !bytes.Equal(definition.Request, []byte{1}) || !bytes.Equal(definition.WritePrefix, []byte{1}) {
			t.Fatal(definition)
		}
		if len(definition.Choices) != 3 || definition.Choices[0].Raw != 1 || definition.Choices[1].Raw != 2 || definition.Choices[2].Raw != 4 {
			t.Fatal(definition.Choices)
		}
		return
	}
	t.Fatal("missing noise-control setting")
}
