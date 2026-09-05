package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"debug/elf"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.0.1", "1.0.0", 1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc.9", 1},
		{"1.0.0-rc.2", "1.0.0-rc.1", 1},
		{"1.0.0-rc.10", "1.0.0-rc.2", 1},
		{"2.0.0-alpha", "1.9.9", 1},
	}
	for _, test := range tests {
		t.Run(test.left+"_vs_"+test.right, func(t *testing.T) {
			got, err := compareVersions(test.left, test.right)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("compareVersions(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
			}
		})
	}
}

func TestCheckChoosesNewestPublishedRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/releases" {
			http.NotFound(w, r)
			return
		}
		base := "http://" + r.Host
		releases := []Release{
			{TagName: "v1.0.0-rc.1", HTMLURL: base + "/release/rc1", Prerelease: true, Assets: releaseAssets(base, "1.0.0-rc.1")},
			{TagName: "v0.9.0", HTMLURL: base + "/release/090", Assets: releaseAssets(base, "0.9.0")},
			{TagName: "v9.0.0", Draft: true, Assets: releaseAssets(base, "9.0.0")},
		}
		_ = json.NewEncoder(w).Encode(releases)
	}))
	defer server.Close()

	client := &Client{
		HTTP:       server.Client(),
		APIBaseURL: server.URL,
		Repository: "owner/repo",
		GOOS:       "linux",
		GOARCH:     "amd64",
	}
	plan, err := client.Check(context.Background(), "0.8.0", true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version != "1.0.0-rc.1" || !plan.NewerThanCurrent {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if plan.ArchiveName != "jabridge_1.0.0-rc.1_linux_amd64.tar.gz" {
		t.Fatalf("archive name = %q", plan.ArchiveName)
	}
}

func TestInstallVerifiesAndReplacesBinary(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("ELF fixture uses the current Linux amd64 test binary")
	}
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	newBinary, err := os.ReadFile(testExecutable)
	if err != nil {
		t.Fatal(err)
	}
	archive := makeArchive(t, map[string][]byte{
		"jabridge_1.0.0_linux_amd64/jabridge": newBinary,
	})
	digest := sha256.Sum256(archive)
	archiveName := "jabridge_1.0.0_linux_amd64.tar.gz"
	checksum := []byte(fmt.Sprintf("%x  %s\n", digest, archiveName))
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	signature := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, archive)) + "\n")
	badSignature := []byte(base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)) + "\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/archive":
			_, _ = w.Write(archive)
		case "/checksum":
			_, _ = w.Write(checksum)
		case "/signature":
			_, _ = w.Write(signature)
		case "/bad-signature":
			_, _ = w.Write(badSignature)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	targetDir := t.TempDir()
	runningPath := filepath.Join(targetDir, "jabridge")
	if err := os.WriteFile(runningPath, []byte("old jabridge"), 0o755); err != nil {
		t.Fatal(err)
	}

	client := &Client{
		HTTP:       server.Client(),
		APIBaseURL: server.URL,
		Repository: "owner/repo",
		GOOS:       "linux",
		GOARCH:     "amd64",
		PublicKey:  publicKey,
	}
	plan := Plan{
		Version:          "1.0.0",
		ArchiveName:      archiveName,
		Archive:          Asset{Name: archiveName, BrowserDownloadURL: server.URL + "/archive", Size: int64(len(archive))},
		Checksum:         Asset{Name: archiveName + ".sha256", BrowserDownloadURL: server.URL + "/checksum", Size: int64(len(checksum))},
		Signature:        Asset{Name: archiveName + ".sig", BrowserDownloadURL: server.URL + "/signature", Size: int64(len(signature))},
		NewerThanCurrent: true,
	}
	badPlan := plan
	badPlan.Signature = Asset{Name: archiveName + ".sig", BrowserDownloadURL: server.URL + "/bad-signature", Size: int64(len(badSignature))}
	if err := client.Install(context.Background(), badPlan, runningPath); err == nil {
		t.Fatal("invalid release signature was accepted")
	}
	unchanged, err := os.ReadFile(runningPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != "old jabridge" {
		t.Fatal("binary changed after invalid signature")
	}
	if err := client.Install(context.Background(), plan, runningPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(runningPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newBinary) {
		t.Fatal("jabridge was not replaced")
	}
}

func TestExtractBinariesRejectsTraversal(t *testing.T) {
	archive := makeArchive(t, map[string][]byte{"../jabridge": []byte("bad")})
	if _, err := extractBinaries(archive); err == nil {
		t.Fatal("unsafe archive path was accepted")
	}
}

func TestExtractBinariesAllowsIPCGuide(t *testing.T) {
	archive := makeArchive(t, map[string][]byte{
		"jabridge_1.0.0_linux_amd64/jabridge":    []byte("binary"),
		"jabridge_1.0.0_linux_amd64/docs/IPC.md": []byte("guide"),
	})
	files, err := extractBinaries(archive)
	if err != nil {
		t.Fatal(err)
	}
	if string(files["jabridge"]) != "binary" {
		t.Fatalf("extracted binary = %q", files["jabridge"])
	}
}

func TestValidateELFAcceptsPIE(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("ELF fixture uses the current Linux amd64 test binary")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 18 {
		t.Fatal("test executable has no ELF type field")
	}
	binary.LittleEndian.PutUint16(data[16:18], uint16(elf.ET_DYN))
	if err := validateELF(data, "amd64"); err != nil {
		t.Fatalf("PIE was rejected: %v", err)
	}
}

func TestParseChecksumRequiresMatchingName(t *testing.T) {
	digest := sha256.Sum256([]byte("archive"))
	body := []byte(fmt.Sprintf("%x  wrong.tar.gz\n", digest))
	if _, err := parseChecksum(body, "right.tar.gz"); err == nil {
		t.Fatal("checksum for wrong filename was accepted")
	}
}

func TestParseSignatureRejectsInvalidData(t *testing.T) {
	if _, err := parseSignature([]byte("not-a-signature")); err == nil {
		t.Fatal("invalid release signature was accepted")
	}
}

func TestEmbeddedReleaseKeyMatchesSigningVector(t *testing.T) {
	signature, err := base64.StdEncoding.DecodeString("+zyn6ipl/0KeKmJtTjYY/OngKBOGfCOYoVEz/W7pUwBSskFaEqCZEsomz9FJOS581732L7hV3cCxA1NhD/EDAQ==")
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("jabridge-release-key-self-test-v1")
	if !ed25519.Verify(NewClient().PublicKey, message, signature) {
		t.Fatal("embedded public key does not match the release signer")
	}
}

func releaseAssets(base, version string) []Asset {
	name := "jabridge_" + version + "_linux_amd64.tar.gz"
	return []Asset{
		{Name: name, BrowserDownloadURL: base + "/" + name, Size: 10},
		{Name: name + ".sha256", BrowserDownloadURL: base + "/" + name + ".sha256", Size: 10},
		{Name: name + ".sig", BrowserDownloadURL: base + "/" + name + ".sig", Size: 10},
	}
}

func makeArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		header := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
