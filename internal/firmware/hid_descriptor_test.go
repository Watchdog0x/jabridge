package firmware

import "testing"

func TestHIDDescriptorAccumulatesOutputFieldsAndRestoresGlobals(t *testing.T) {
	desc := []byte{0x85, 5, 0x75, 8, 0x95, 31, 0x91, 2, 0xa4, 0x85, 9, 0x95, 1, 0x81, 2, 0xb4, 0x91, 2}
	if size, ok := parseGnpOutputReportSizeFound(desc); !ok || size != 63 {
		t.Fatalf("split report: %d %v", size, ok)
	}
	desc = append(desc, 0x91, 2)
	if _, ok := parseGnpOutputReportSizeFound(desc); ok {
		t.Fatal("oversized aggregate accepted")
	}
}

func TestHIDDescriptorRejectsMalformedAndFeatureOnlyReports(t *testing.T) {
	for _, desc := range [][]byte{{0xb4}, {0xfe, 4, 0, 1}, {0x85, 0}, {0x75, 8, 0x95, 63, 0x85, 5, 0xb1, 2}} {
		if _, ok := parseGnpOutputReportSizeFound(desc); ok {
			t.Fatalf("invalid output accepted: %x", desc)
		}
	}
}

func TestDescriptorRetainsSignedRangesAcrossGlobalPushPop(t *testing.T) {
	desc := []byte{0x85, 1, 0x75, 8, 0x95, 1, 0x15, 0x81, 0x25, 0x7f, 0xa4, 0x15, 0, 0x26, 0xff, 0, 0x81, 2, 0xb4, 0x81, 2}
	reports, err := parseHIDReports(desc)
	if err != nil || len(reports) != 1 || len(reports[0].Fields) != 2 {
		t.Fatal(reports, err)
	}
	first, second := reports[0].Fields[0], reports[0].Fields[1]
	if first.LogicalMin != 0 || first.LogicalMax != 255 || second.LogicalMin != -127 || second.LogicalMax != 127 {
		t.Fatal(reports)
	}
}
