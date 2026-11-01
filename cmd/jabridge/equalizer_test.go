package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/completion"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

// Flat reply and flat write vectors follow the vendor's documented field
// order and scales. In particular, the read-only maxGain bytes are absent
// from the write, whose prefix is count plus parameter rather than count alone.
const equalizerFlatReply = "0500001e00b4000006c21802ee00000676180bb800000629182ee00000063d28591000000828"
const equalizerFlatWrite = "0500000000b4000006c202ee000006760bb8000006292ee00000063d591000000828"

func equalizerPropertyForTest(t testing.TB) modelcatalog.Property {
	t.Helper()
	fixture, err := os.ReadFile("../../internal/modelcatalog/testdata/evolve3-65-1.0.1.json")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/bundles" {
			_, _ = w.Write([]byte(`{"unbundledProducts":[{"productName":"Jabra Evolve3 65","variants":[{"vendorId":2830,"productId":9545,"variantType":"01-A3","fwuProtocolId":17}],"firmwareReleases":[{"version":"1.0.1"}]}]}`))
			return
		}
		_, _ = w.Write(fixture)
	}))
	defer server.Close()
	client := modelcatalog.NewClient()
	client.BundlesURL, client.ModelsBaseURL, client.HTTPClient = server.URL+"/bundles", server.URL, server.Client()
	capabilities, err := client.Lookup(context.Background(), 0x2549, "01-A3", "1.0.1")
	if err != nil || !capabilities.ExactFirmwareProfile {
		t.Fatal("could not parse the exact public profile", err)
	}
	return capabilities.Properties["equalizerConfig"]
}

func TestUnsupportedEqualizerShowsOneGenericReadOnlyRow(t *testing.T) {
	var values []choiceSettingValue
	seen := make(map[string]struct{})
	for _, definition := range equalizerDefinitions() {
		values = appendUnavailableChoice(values, seen, definition, "Not supported on this firmware")
	}
	if len(values) != 1 || values[0].Definition.Key != "equalizer" || values[0].Definition.Label != "Equalizer" || values[0].Editable || len(values[0].Definition.Choices) != 0 {
		t.Fatal("unsupported layout presented misleading frequency rows", values)
	}
	info := ipcSettingInfo("headset", deviceSettingValue{Choice: &values[0]})
	if info.Editable || info.Value != "Not supported on this firmware" {
		t.Fatal(info)
	}
}

func FuzzEqualizerReply(f *testing.F) {
	codec, _, err := (equalizerBandCodec{band: 0}).BindProperty(equalizerPropertyForTest(f))
	if err != nil {
		f.Fatal(err)
	}
	flat, err := hex.DecodeString(equalizerFlatReply)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(flat)
	f.Add([]byte{5, 0, 0})
	f.Fuzz(func(t *testing.T, payload []byte) {
		if _, _, err := codec.Decode(payload); err != nil {
			return
		}
		encoded, err := codec.Encode(payload, 72)
		if err != nil || len(encoded) != 34 {
			t.Fatal("valid read could not encode a zero-gain change", err)
		}
	})
}

func TestEqualizerBashCompletion(t *testing.T) {
	for _, command := range []struct{ words, index string }{
		{"jabridge settings set headset.equalizer-60hz ''", "4"},
		{"jabridge ipc set headset equalizer-60hz ''", "5"},
	} {
		t.Run(command.words, func(t *testing.T) {
			cmd := exec.Command("bash", "--noprofile", "--norc")
			cmd.Stdin = strings.NewReader(completion.JabridgeBash + "\nCOMP_WORDS=(" + command.words + ")\nCOMP_CWORD=" + command.index + "\n_jabridge_completion\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatal(err, string(out))
			}
			values := strings.Fields(string(out))
			if len(values) != 97 || values[0] != "-36" || values[72] != "0" || values[96] != "12" {
				t.Fatal("incorrect half-step completion range", values)
			}
			codec := equalizerBandCodec{}
			for _, value := range values {
				if _, err := codec.ParseChoice(value); err != nil {
					t.Fatal("completion suggested an invalid gain", value, err)
				}
			}
		})
	}
}

func TestEqualizerSnapshotReadsOnlyOnceAndUnavailableControlsCannotWrite(t *testing.T) {
	property := equalizerPropertyForTest(t)
	flat := equalizerBytes(t, equalizerFlatReply)
	reads := make(choiceSnapshotReads)
	queries := 0
	for _, original := range equalizerDefinitions() {
		definition, supported := choiceSettingForCatalog(original, property, true)
		if !supported {
			t.Fatal("could not bind EQ row")
		}
		value, err := readChoiceSettingFromSnapshot(definition, reads, func() ([]byte, error) {
			queries++
			return append([]byte(nil), flat...), nil
		})
		if err != nil || choiceSettingName(value) != "0 dB" {
			t.Fatal("EQ row did not use the snapshot", value, err)
		}
	}
	if queries != 1 {
		t.Fatal("repeated the same device query for each EQ row", queries)
	}
	reads = make(choiceSnapshotReads)
	for _, original := range equalizerDefinitions() {
		definition, _ := choiceSettingForCatalog(original, property, true)
		_, err := readChoiceSettingFromSnapshot(definition, reads, func() ([]byte, error) {
			queries++
			return nil, errors.New("device unavailable")
		})
		if err == nil {
			t.Fatal("ignored the snapshot's query error")
		}
		unavailable := unavailableChoiceSetting(definition, "Unavailable")
		info := ipcSettingInfo("headset", deviceSettingValue{Choice: &unavailable})
		if info.Editable || info.Value != "Unavailable" {
			t.Fatal("unavailable EQ control became editable", info)
		}
		if _, _, err := applyDeviceSettingFromText(nil, deviceSettingValue{Choice: &unavailable}, "0"); err == nil {
			t.Fatal("unavailable control accepted a write")
		}
	}
	if queries != 2 {
		t.Fatal("repeated failed queries or shared cache across snapshots", queries)
	}
}

func equalizerBytes(t *testing.T, value string) []byte {
	t.Helper()
	payload, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func equalizerDefinitionForTest(t *testing.T, band int) choiceSettingDefinition {
	t.Helper()
	definition, supported := choiceSettingForCatalog(equalizerDefinitions()[band], equalizerPropertyForTest(t), true)
	if !supported {
		t.Fatal("exact equalizer profile was refused")
	}
	return definition
}

func TestEqualizerHasFiveBandControls(t *testing.T) {
	keys := map[string]bool{
		"equalizer-60hz": false, "equalizer-250hz": false,
		"equalizer-1khz": false, "equalizer-4khz": false, "equalizer-7.6khz": false,
	}
	for _, definition := range choiceSettingDefinitions(settingScopeHeadset) {
		if _, wanted := keys[definition.Key]; wanted {
			if definition.Class != gnpClassConfig || definition.Op != 0x7d || len(definition.Request) != 1 || definition.Request[0] != 0 {
				t.Fatalf("incorrect equalizer query: %+v", definition)
			}
			keys[definition.Key] = true
		}
	}
	for key, found := range keys {
		if !found {
			t.Errorf("equalizer band %s is missing", key)
		}
	}
}

func TestEqualizerGoldenPayloadsAndReadback(t *testing.T) {
	definition := equalizerDefinitionForTest(t, 0)
	codec := definition.Codec
	flat := equalizerBytes(t, equalizerFlatReply)
	if len(flat) != 38 || len(definition.Choices) != 88 {
		t.Fatal("unexpected vendor reply or allowed gain range")
	}
	for _, test := range []struct{ input, written, gainBytes string }{
		{"0", equalizerFlatWrite, "0000"},
		{"+3", "0500000000b400b406c202ee000006760bb8000006292ee00000063d591000000828", "00b4"},
		{"-3", "0500000000b4ff4c06c202ee000006760bb8000006292ee00000063d591000000828", "ff4c"},
	} {
		t.Run(test.input, func(t *testing.T) {
			index, ok := findChoiceIndex(definition, test.input)
			if !ok {
				t.Fatal("gain input was refused")
			}
			payload, err := codec.Encode(flat, definition.Choices[index].Raw)
			if err != nil || !bytes.Equal(payload, equalizerBytes(t, test.written)) || len(payload) != 34 {
				t.Fatalf("write = %x, %v; want %s", payload, err, test.written)
			}
			readback := append([]byte(nil), flat...)
			copy(readback[6:8], equalizerBytes(t, test.gainBytes))
			if err := codec.Verify(payload, readback); err != nil {
				t.Fatal("matching readback was refused", err)
			}
			value, err := decodeChoiceSettingPayload(definition, readback)
			if err != nil || value.ChoiceIndex != index || value.Raw != definition.Choices[index].Raw {
				t.Fatal("wire gain did not resolve to its logical choice", value, err)
			}
			info := ipcSettingInfo("headset", deviceSettingValue{Choice: &value})
			if info.Kind != "choice" || !info.Editable || info.Value != definition.Choices[index].Name {
				t.Fatal("IPC and TUI gain value differs", info)
			}
		})
	}
	for _, input := range []string{"7.5", "-36", "-3.5-db", "3 dB", "0"} {
		if _, ok := findChoiceIndex(definition, input); !ok {
			t.Fatal("valid half-step gain refused", input)
		}
	}
	for _, input := range []string{"8", "12", "-36.5", "0.1", "NaN", "Inf", "other"} {
		if _, ok := findChoiceIndex(definition, input); ok {
			t.Fatal("out-of-band or inexact gain accepted", input)
		}
	}
}

func TestEqualizerPreservesFreshMasterAndOtherBands(t *testing.T) {
	definition := equalizerDefinitionForTest(t, 0)
	current := equalizerBytes(t, equalizerFlatReply)
	binary.BigEndian.PutUint16(current[1:3], 0xffc4) // Actual master -1 dB.
	binary.BigEndian.PutUint16(current[13:15], 720)  // Another band at +12 dB, above its own +6 limit.
	index, ok := findChoiceIndex(definition, "3")
	if !ok {
		t.Fatal("gain choice unavailable")
	}
	written, err := definition.Codec.Encode(current, definition.Choices[index].Raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written[2:4], []byte{0xff, 0xc4}) || !bytes.Equal(written[12:14], []byte{0x02, 0xd0}) {
		t.Fatal("overwrote fresh master gain or a different band", written)
	}
	readback := append([]byte(nil), current...)
	binary.BigEndian.PutUint16(readback[6:8], 180)
	if err := definition.Codec.Verify(written, readback); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{1, 3, 6, 13, 36} {
		changed := append([]byte(nil), readback...)
		changed[offset] ^= 1
		if err := definition.Codec.Verify(written, changed); err == nil {
			t.Fatalf("accepted a mismatching readback at byte %d", offset)
		}
	}
}

func TestEqualizerRejectsUnknownLayoutsAndKeepsOffStepReads(t *testing.T) {
	definition := equalizerDefinitionForTest(t, 0)
	flat := equalizerBytes(t, equalizerFlatReply)
	invalid := [][]byte{nil, flat[:37], append(append([]byte(nil), flat...), 0)}
	for _, offset := range []int{0, 3, 4, 8} {
		payload := append([]byte(nil), flat...)
		payload[offset] ^= 1
		invalid = append(invalid, payload)
	}
	for _, payload := range invalid {
		if _, _, err := definition.Codec.Decode(payload); err == nil {
			t.Fatalf("accepted an unsupported equalizer reply %x", payload)
		}
		if _, err := definition.Codec.Encode(payload, 72); err == nil {
			t.Fatal("encoded a write from an unsupported reply")
		}
	}
	if _, _, err := (equalizerBandCodec{}).Decode(flat); err == nil {
		t.Fatal("unbound equalizer was accepted")
	}
	for _, id := range []byte{88, 96, 255} {
		if _, err := definition.Codec.Encode(flat, id); err == nil {
			t.Fatal("accepted gain beyond this band's maximum", id)
		}
	}
	current := append([]byte(nil), flat...)
	binary.BigEndian.PutUint16(current[6:8], 80)
	value, err := decodeChoiceSettingPayload(definition, current)
	if err != nil || value.ChoiceIndex != -1 || choiceSettingName(value) != "1.33 dB" {
		t.Fatal("rounded or hid a valid non-half-step current gain", value, err)
	}
}

func TestEqualizerCatalogBoundsAndIncompleteProfiles(t *testing.T) {
	property := equalizerPropertyForTest(t)
	for band, count := range []int{88, 85, 85, 85, 93} {
		definition, supported := choiceSettingForCatalog(equalizerDefinitions()[band], property, true)
		if !supported || len(definition.Choices) != count || !definition.Writable {
			t.Fatal("incorrect model-specific gain choices", band, count, supported)
		}
		readonly, supported := choiceSettingForCatalog(equalizerDefinitions()[band], property, false)
		if !supported || readonly.Writable {
			t.Fatal("fallback firmware profile permitted an equalizer write")
		}
	}
	property.Access = "ReadOnly"
	if definition, supported := choiceSettingForCatalog(equalizerDefinitions()[0], property, true); !supported || definition.Writable {
		t.Fatal("ignored catalog read-only access")
	}
	for _, damage := range []func(*modelcatalog.Property){
		func(p *modelcatalog.Property) { p.CompositeSettings[0].IsStatic = nil },
		func(p *modelcatalog.Property) { p.CompositeSettings[0].ID = "" },
		func(p *modelcatalog.Property) { p.CompositeSettings[0].SetPath = "" },
		func(p *modelcatalog.Property) { p.CompositeSettings[0].Choices[0].Value = "<invalid>" },
		func(p *modelcatalog.Property) { p.CompositeSettings[0].Choices[0].Value = "NaN" },
		func(p *modelcatalog.Property) { p.CompositeSettings[1] = p.CompositeSettings[0] },
		func(p *modelcatalog.Property) { p.CompositeSettings[16].GetPath = "" },
	} {
		data, err := json.Marshal(equalizerPropertyForTest(t))
		if err != nil {
			t.Fatal(err)
		}
		var broken modelcatalog.Property
		if err := json.Unmarshal(data, &broken); err != nil {
			t.Fatal(err)
		}
		damage(&broken)
		if _, supported := choiceSettingForCatalog(equalizerDefinitions()[0], broken, true); supported {
			t.Fatal("accepted incomplete or ambiguous equalizer metadata")
		}
	}
}
