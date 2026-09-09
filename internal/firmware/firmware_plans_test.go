package firmware

import (
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCSRProtocol17StagePlanning(t *testing.T) {
	selected := []GnVFile{{Name: "voice.bin", Content: "voiceprompt"}, {Name: "firmware.bin", Content: "firmware"}}
	for _, test := range []struct {
		firmware, language bool
		want               string
	}{
		{true, true, "language,firmware"}, {true, false, "language,firmware"}, {false, true, "language"}, {false, false, "combined"},
	} {
		stages, err := planCSRStages(17, selected, test.firmware, test.language)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, stage := range stages {
			names = append(names, stage.Kind)
			if !stage.RequiresConfigExit {
				t.Fatal("missing mode exit")
			}
		}
		if strings.Join(names, ",") != test.want {
			t.Fatal(names, test)
		}
	}
	stages, err := planCSRStages(16, selected, true, true)
	if err != nil || len(stages) != 1 || len(stages[0].Files) != 2 {
		t.Fatal(stages, err)
	}
	// A firmware image may contain a language variant without being a voice pack.
	selected[1].Language.ID = "0x0409"
	stages, err = planCSRStages(17, selected, true, false)
	if err != nil || len(stages) != 2 || stages[1].Files[0].Name != "firmware.bin" {
		t.Fatal(stages, err)
	}
	for _, content := range []string{"langpack", "voiceprompt", "tunepack"} {
		stages, err = planCSRStages(17, []GnVFile{{Content: content}}, false, true)
		if err != nil || len(stages) != 1 || stages[0].Kind != "language" {
			t.Fatal(content, err)
		}
	}
	if _, err := planCSRStages(7, selected, true, true); err == nil {
		t.Fatal("wrong protocol accepted")
	}
	if _, err := planCSRStages(17, nil, true, true); err == nil {
		t.Fatal("empty plan")
	}
	if _, err := planCSRStages(17, []GnVFile{{Content: "unknown"}}, true, true); err == nil {
		t.Fatal("unknown stage")
	}
}

func hexRecord(address uint16, kind byte, data ...byte) string {
	record := append([]byte{byte(len(data)), byte(address >> 8), byte(address), kind}, data...)
	var sum byte
	for _, value := range record {
		sum += value
	}
	record = append(record, -sum)
	return ":" + strings.ToUpper(hex.EncodeToString(record)) + "\r\n"
}

func TestIntelHEXPreservesSparseSegmentAndLinearAddresses(t *testing.T) {
	image := hexRecord(0, 2, 0x60, 0) + hexRecord(0, 0, 1, 2, 3) + hexRecord(0, 4, 1, 0) + hexRecord(16, 0, 4, 5) + hexRecord(0, 1)
	segments, err := parseIntelHexImage([]byte(image))
	if err != nil || len(segments) != 2 || segments[0].Address != 0x60000 || segments[1].Address != 0x1000010 || len(segments[1].Data) != 2 {
		t.Fatal(segments, err)
	}
}

func TestIntelHEXRejectsBadImages(t *testing.T) {
	valid := hexRecord(0, 0, 1, 2) + hexRecord(0, 1)
	for name, image := range map[string]string{
		"checksum":  ":020000000102FA\n:00000001FF\n",
		"truncated": ":1000000001\n",
		"no EOF":    hexRecord(0, 0, 1),
		"after EOF": valid + hexRecord(10, 0, 1),
		"overlap":   hexRecord(0, 0, 1, 2) + hexRecord(1, 0, 3) + hexRecord(0, 1),
		"overflow":  hexRecord(0, 4, 255, 255) + hexRecord(0xffff, 0, 1, 2) + hexRecord(0, 1),
		"bad type":  hexRecord(0, 6, 1) + hexRecord(0, 1),
		"bad base":  hexRecord(0, 2, 1) + valid,
		"bad end":   hexRecord(0, 0, 1) + hexRecord(1, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseIntelHexImage([]byte(image)); err == nil {
				t.Fatal("invalid image accepted")
			}
		})
	}
}

func TestSitelPlanPreservesControllerAndHeadsetTargets(t *testing.T) {
	manifest := []byte(`<buildVector version="4.1.3"><files>
<file name="tones.hex"><content>tunepack</content><sitelHidTargetId>27</sitelHidTargetId><gnpAddress>1</gnpAddress><updateOrder>20</updateOrder><regionId>1</regionId></file>
<file name="controller.hex"><content>firmware</content><sitelHidTargetId>29</sitelHidTargetId><gnpAddress>1</gnpAddress><updateOrder>15</updateOrder></file>
<file name="headset.hex"><content>firmware</content><sitelHidTargetId>03</sitelHidTargetId><gnpAddress>1</gnpAddress><updateOrder>1</updateOrder></file>
</files></buildVector>`)
	var bv BuildVector
	if err := xml.Unmarshal(manifest, &bv); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, file := range bv.Files {
		files[file.Name] = []byte(hexRecord(0, 0, 1, 2) + hexRecord(0, 1))
	}
	plan, err := planSitelImages(&bv, files)
	if err != nil || len(plan) != 3 {
		t.Fatal(plan, err)
	}
	for i, want := range []string{"03", "29", "27"} {
		if plan[i].File.SitelHidTargetID != want || plan[i].File.GNPAddress != "1" {
			t.Fatal(plan)
		}
	}
	bv.Files[1].SitelHidTargetID = "27"
	if _, err := planSitelImages(&bv, files); err == nil {
		t.Fatal("ambiguous target accepted")
	}
}

func TestCSRArchiveRejectsCorruptSelectedImage(t *testing.T) {
	manifest := fmt.Sprintf(`<buildVector version="1.2.3"><targetUsbPids><usbPid>0x1234</usbPid></targetUsbPids><files><file name="app.gnv"><partition>5</partition><crc>0x%08x</crc><language id="0x0409"/></file><file name="footer.gnv"><partition>254</partition><crc>0x%08x</crc><language id="0x0409"/></file></files></buildVector>`, referenceStageCRC([]byte{1, 2, 3}), referenceStageCRC([]byte{4}))
	path := writeFirmwareArchiveFixture(t, manifest, map[string][]byte{"app.gnv": {1, 2, 9}, "footer.gnv": {4}})
	if err := validateNativeCSRArchive(path); err == nil || !strings.Contains(err.Error(), "CRC does not match") {
		t.Fatal("corruption accepted", err)
	}
}

func TestLocalFirmwareReferencePlans(t *testing.T) {
	dir := os.Getenv("JABRIDGE_FIRMWARE_REFERENCE_DIR")
	if dir == "" {
		t.Skip("local read-only reference archives not provided")
	}
	data, err := os.ReadFile(filepath.Join(dir, "4051", "info.xml"))
	if err != nil {
		t.Fatal(err)
	}
	var bv BuildVector
	if err := xml.Unmarshal(data, &bv); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, file := range bv.Files {
		if filepath.Base(file.Name) != file.Name {
			t.Fatal("unsafe reference filename")
		}
		data, err := os.ReadFile(filepath.Join(dir, "4051", file.Name))
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name] = data
	}
	plan, err := planSitelImages(&bv, files)
	if err != nil || len(plan) != 3 {
		t.Fatal(plan, err)
	}
	for _, image := range plan {
		t.Logf("order=%d target=%s address=%s records=%d", image.File.UpdateOrder, image.File.SitelHidTargetID, image.File.GNPAddress, len(image.Segments))
	}
	for _, pid := range []string{"24a3", "24b3"} {
		data, err := os.ReadFile(filepath.Join(dir, pid, "info.xml"))
		if err != nil {
			t.Fatal(err)
		}
		var manifest BuildVector
		if err := xml.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		checked := 0
		for _, file := range manifest.Files {
			// The reference extraction intentionally contains only English images.
			if !strings.EqualFold(file.Language.ID, "0x0409") {
				continue
			}
			if filepath.Base(file.Name) != file.Name {
				t.Fatal("unsafe reference filename")
			}
			data, err := os.ReadFile(filepath.Join(dir, pid, file.Name))
			if err != nil {
				t.Fatal(err)
			}
			want, err := parseHexCRC(file.CRC)
			if err != nil {
				t.Fatal(err)
			}
			if got := csrImageCRC(data); got != want {
				t.Fatalf("reference %s/%s CRC mismatch: got %08x want %08x", pid, file.Name, got, want)
			}
			checked++
		}
		if checked == 0 {
			t.Fatal("no English reference images checked")
		}
		t.Logf("model %s: %d original English image CRCs matched manifest", pid, checked)
	}
}
