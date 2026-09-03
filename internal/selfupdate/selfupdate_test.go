package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
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

func TestInstallVerifiesAndReplacesBothBinaries(t *testing.T) {
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
		"jabridge_1.0.0_linux_amd64/jafw":     newBinary,
	})
	digest := sha256.Sum256(archive)
	archiveName := "jabridge_1.0.0_linux_amd64.tar.gz"
	checksum := []byte(fmt.Sprintf("%x  %s\n", digest, archiveName))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/archive":
			_, _ = w.Write(archive)
		case "/checksum":
			_, _ = w.Write(checksum)
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
	if err := os.WriteFile(filepath.Join(targetDir, "jafw"), []byte("old jafw"), 0o755); err != nil {
		t.Fatal(err)
	}

	client := &Client{
		HTTP:       server.Client(),
		APIBaseURL: server.URL,
		Repository: "owner/repo",
		GOOS:       "linux",
		GOARCH:     "amd64",
	}
	plan := Plan{
		Version:          "1.0.0",
		ArchiveName:      archiveName,
		Archive:          Asset{Name: archiveName, BrowserDownloadURL: server.URL + "/archive", Size: int64(len(archive))},
		Checksum:         Asset{Name: archiveName + ".sha256", BrowserDownloadURL: server.URL + "/checksum", Size: int64(len(checksum))},
		NewerThanCurrent: true,
	}
	if err := client.Install(context.Background(), plan, runningPath); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"jabridge", "jafw"} {
		got, err := os.ReadFile(filepath.Join(targetDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, newBinary) {
			t.Fatalf("%s was not replaced", name)
		}
	}
}

func TestExtractBinariesRejectsTraversal(t *testing.T) {
	archive := makeArchive(t, map[string][]byte{"../jabridge": []byte("bad")})
	if _, err := extractBinaries(archive); err == nil {
		t.Fatal("unsafe archive path was accepted")
	}
}

func TestParseChecksumRequiresMatchingName(t *testing.T) {
	digest := sha256.Sum256([]byte("archive"))
	body := []byte(fmt.Sprintf("%x  wrong.tar.gz\n", digest))
	if _, err := parseChecksum(body, "right.tar.gz"); err == nil {
		t.Fatal("checksum for wrong filename was accepted")
	}
}

func releaseAssets(base, version string) []Asset {
	name := "jabridge_" + version + "_linux_amd64.tar.gz"
	return []Asset{
		{Name: name, BrowserDownloadURL: base + "/" + name, Size: 10},
		{Name: name + ".sha256", BrowserDownloadURL: base + "/" + name + ".sha256", Size: 10},
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
