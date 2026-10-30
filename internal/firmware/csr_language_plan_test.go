package firmware

import (
	"bytes"
	"encoding/xml"
	"os"
	"strconv"
	"testing"
)

func TestCSRLanguagePlanUsesPerFileVersionAndContent(t *testing.T) {
	archive := os.Getenv("JABRIDGE_CSR17_TEST_PACKAGE")
	if archive == "" {
		t.Skip("set JABRIDGE_CSR17_TEST_PACKAGE to the official Evolve3 65 archive")
	}
	manifest, contents, err := parseGnVArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range []uint16{0x0409, 0x0407, 0x040c, 0x0411, 0x0412, 0x0804} {
		plan, err := prepareCSRLanguagePlan(manifest, contents, 17, language)
		if err != nil || plan.Language != language || plan.Firmware != "1.0.1" || plan.TuneVersion != [3]byte{0, 2, 1} || plan.Partition != 0 || len(plan.Image) == 0 {
			t.Fatal("language did not select its own image/version", language, plan, err)
		}
		expected := []byte(nil)
		for _, file := range manifest.Files {
			value, err := strconv.ParseUint(file.Language.ID, 0, 16)
			if err == nil && file.Content == "tunepack" && uint16(value) == language {
				if expected != nil {
					t.Fatal("ambiguous test fixture")
				}
				expected = contents[file.Name]
			}
		}
		if expected == nil || !bytes.Equal(plan.Image, expected) {
			t.Fatal("selected bytes belong to a different language", language)
		}
	}
	if _, err := prepareCSRLanguagePlan(manifest, contents, 16, 0x407); err == nil {
		t.Fatal("voice language update was authorized on a different protocol")
	}
	if _, err := prepareCSRLanguagePlan(manifest, contents, 17, 0x401); err == nil {
		t.Fatal("absent language silently fell back")
	}
}

func languageManifestFixture(t *testing.T) (*BuildVector, map[string][]byte) {
	t.Helper()
	data, err := os.ReadFile("testdata/evolve3-65-language-info.xml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest BuildVector
	if err := xml.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	contents := map[string][]byte{}
	for index, file := range manifest.Files {
		contents[file.Name] = []byte{byte(index), 0xa5}
	}
	return &manifest, contents
}

func TestCSRLanguageMetadataContractRunsWithoutFirmwareDownload(t *testing.T) {
	manifest, contents := languageManifestFixture(t)
	plan, err := prepareCSRLanguagePlan(manifest, contents, 17, 0x407)
	if err != nil || plan.Firmware != "1.0.1" || plan.TuneVersion != [3]byte{0, 2, 1} || len(plan.Image) != 2 {
		t.Fatal("separate content/version/partition contract failed", plan, err)
	}
	for _, language := range []uint16{0x409, 0x407, 0x40c, 0x411, 0x412, 0x804} {
		selected, err := prepareCSRLanguagePlan(manifest, contents, 17, language)
		if err != nil {
			t.Fatal(err)
		}
		var expected []byte
		for _, file := range manifest.Files {
			value, err := strconv.ParseUint(file.Language.ID, 0, 16)
			if err == nil && file.Content == "tunepack" && uint16(value) == language {
				expected = contents[file.Name]
			}
		}
		if expected == nil || !bytes.Equal(selected.Image, expected) {
			t.Fatal("fixture selection returned another language's distinct bytes", language, selected.Image, expected)
		}
	}
	for _, damage := range []func(*BuildVector, map[string][]byte){
		func(m *BuildVector, c map[string][]byte) {
			file := m.Files[0]
			file.Name = "duplicate.bin"
			m.Files = append(m.Files, file)
			c[file.Name] = []byte{1}
		},
		func(m *BuildVector, _ map[string][]byte) { m.Files[0].Language.ID = "" },
		func(m *BuildVector, _ map[string][]byte) { m.Files[len(m.Files)-1].Language.ID = "0x0407" },
		func(m *BuildVector, _ map[string][]byte) { m.Files[len(m.Files)-1].Version = "0.9.0" },
		func(m *BuildVector, _ map[string][]byte) { m.Files[0].Content = "unknown" },
		func(m *BuildVector, _ map[string][]byte) { m.Files[0].Name = "../voice.bin" },
		func(m *BuildVector, c map[string][]byte) { delete(c, m.Files[0].Name) },
		func(m *BuildVector, _ map[string][]byte) { m.TargetUSBPIDs = []string{"0x2e56"} },
		func(m *BuildVector, _ map[string][]byte) { m.MaxPreloadCount = 11 },
	} {
		m, c := languageManifestFixture(t)
		damage(m, c)
		if _, err := prepareCSRLanguagePlan(m, c, 17, 0x407); err == nil {
			t.Fatal("accepted ambiguous/foreign/damaged voice package")
		}
	}
}

func TestCSRLanguageNamesAndIDs(t *testing.T) {
	for input, want := range map[string]uint16{"German": 0x407, "English (US)": 0x409, "0x0407": 0x407, "1033": 0x409, "Japanese": 0x411} {
		if got, err := parseCSRLanguage(input); err != nil || got != want {
			t.Fatal(input, got, err)
		}
	}
	for _, input := range []string{"", "0", "0xdead", "1.1", "German;echo"} {
		if _, err := parseCSRLanguage(input); err == nil {
			t.Fatal("invalid language accepted", input)
		}
	}
}
