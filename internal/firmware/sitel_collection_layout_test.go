package firmware

import "testing"

// Reconstruct the layout reported in issue #43 on 2026-09-20. The report gives
// parsed metadata, not raw descriptor bytes: FF00 collection, FF54 byte fields,
// report ID 0, 64 bytes in each direction, usage 1, data/variable flags.
func evolve2ReportedBootDescriptor() []byte {
	return []byte{0x06, 0x00, 0xff, 0x09, 1, 0xa1, 1, 0x06, 0x54, 0xff, 0x15, 0x80, 0x25, 0x7f, 0x75, 8, 0x95, 64, 0x09, 1, 0x81, 2, 0x09, 1, 0x91, 2, 0xc0}
}

func TestSitelEvolve2ReportedCollectionLayout(t *testing.T) {
	reports, err := parseHIDReports(evolve2ReportedBootDescriptor())
	if err != nil || len(reports) != 2 {
		t.Fatal(reports, err)
	}
	for _, report := range reports {
		if report.ID != 0 || report.Bytes != 64 || len(report.Fields) != 1 {
			t.Fatal(report)
		}
		f := report.Fields[0]
		if f.collectionPage != 0xff00 || f.UsagePage != 0xff54 || f.OffsetBits != 0 || f.SizeBits != 8 || f.Count != 64 || f.Flags != 2 || f.LogicalMin != -128 || f.LogicalMax != 127 || len(f.Usages) != 1 || f.Usages[0] != 1 {
			t.Fatal(f)
		}
	}
	in, out, err := selectSitelLayouts(reports)
	if err != nil || in.ReportBytes != 65 || in.ReportID != 0 || out != in {
		t.Fatalf("reported Evolve2 boot layout rejected: %+v %+v %v", in, out, err)
	}
	if _, err := SelectControlLayout(reports); err == nil {
		t.Fatal("firmware interface accepted as normal GNP management")
	}
}

func TestSitelGenericCollectionDoesNotAcceptOtherFields(t *testing.T) {
	for _, kind := range []string{"normal-gnp", "unrelated-field", "standard-collection", "wrong-usage", "constant", "mixed", "too-long"} {
		t.Run(kind, func(t *testing.T) {
			reports, err := parseHIDReports(evolve2ReportedBootDescriptor())
			if err != nil {
				t.Fatal(err)
			}
			for i := range reports {
				f := &reports[i].Fields[0]
				switch kind {
				case "normal-gnp":
					f.UsagePage = 0xff00
				case "unrelated-field":
					f.UsagePage = 0xff30
				case "standard-collection":
					f.collectionPage = 1
				case "wrong-usage":
					f.Usages = []uint32{2}
				case "constant":
					f.Flags = 3
				case "mixed":
					f.Count = 32
					other := *f
					other.OffsetBits = 256
					other.UsagePage = 0xff00
					reports[i].Fields = append(reports[i].Fields, other)
				case "too-long":
					f.Count = 128
					reports[i].Bytes = 128
				}
			}
			if _, _, err := selectSitelLayouts(reports); err == nil {
				t.Fatal("unrelated report accepted", kind)
			}
		})
	}
}
