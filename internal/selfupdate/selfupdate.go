// Package selfupdate updates the Jabridge application binaries.
// It never talks to a headset or installs device firmware.
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIBase    = "https://api.github.com"
	defaultRepository = "Watchdog0x/jabridge"
	maxAPIResponse    = 2 << 20
	maxChecksum       = 64 << 10
	maxArchive        = 100 << 20
	maxBinary         = 64 << 20
	maxExtraFile      = 4 << 20
	maxExtracted      = 140 << 20
)

// Asset is one file attached to a GitHub release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
}

// Release is the part of a GitHub release response used by the updater.
type Release struct {
	TagName    string  `json:"tag_name"`
	HTMLURL    string  `json:"html_url"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Plan describes an available release and its files for this machine.
type Plan struct {
	Version          string
	TagName          string
	ReleaseURL       string
	ArchiveName      string
	Archive          Asset
	Checksum         Asset
	NewerThanCurrent bool
}

// Client talks to the public GitHub Releases API.
type Client struct {
	HTTP       *http.Client
	APIBaseURL string
	Repository string
	GOOS       string
	GOARCH     string
}

// NewClient returns a client with safe defaults for the official repository.
func NewClient() *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout: 2 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("too many download redirects")
				}
				if req.URL.Scheme != "https" || !isGitHubDownloadHost(req.URL.Hostname()) {
					return fmt.Errorf("refusing download redirect to %s", req.URL.Redacted())
				}
				return nil
			},
		},
		APIBaseURL: defaultAPIBase,
		Repository: defaultRepository,
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
	}
}

// Check finds the newest suitable release and its archive.
func (c *Client) Check(ctx context.Context, currentVersion string, includePrerelease bool) (Plan, error) {
	if c == nil {
		return Plan{}, errors.New("nil update client")
	}
	c.setDefaults()
	if c.GOOS != "linux" {
		return Plan{}, fmt.Errorf("self-update supports Linux only, not %s", c.GOOS)
	}
	if c.GOARCH != "amd64" && c.GOARCH != "arm64" {
		return Plan{}, fmt.Errorf("no release archive is defined for Linux/%s", c.GOARCH)
	}

	release, err := c.latestRelease(ctx, includePrerelease)
	if err != nil {
		return Plan{}, err
	}
	version, err := cleanVersion(release.TagName)
	if err != nil {
		return Plan{}, fmt.Errorf("release tag %q: %w", release.TagName, err)
	}
	comparison, err := compareVersions(version, currentVersion)
	if err != nil {
		return Plan{}, fmt.Errorf("compare versions: %w", err)
	}

	plan := Plan{
		Version:          version,
		TagName:          release.TagName,
		ReleaseURL:       release.HTMLURL,
		NewerThanCurrent: comparison > 0,
	}
	if !plan.NewerThanCurrent {
		return plan, nil
	}

	archiveName := fmt.Sprintf("jabridge_%s_linux_%s.tar.gz", version, c.GOARCH)
	checksumName := archiveName + ".sha256"
	archive, ok := findAsset(release.Assets, archiveName)
	if !ok {
		return Plan{}, fmt.Errorf("release %s has no %s asset", release.TagName, archiveName)
	}
	checksum, ok := findAsset(release.Assets, checksumName)
	if !ok {
		return Plan{}, fmt.Errorf("release %s has no %s asset", release.TagName, checksumName)
	}

	plan.ArchiveName = archiveName
	plan.Archive = archive
	plan.Checksum = checksum
	return plan, nil
}

// Install downloads, verifies, and atomically replaces jabridge and jafw.
func (c *Client) Install(ctx context.Context, plan Plan, executablePath string) error {
	if !plan.NewerThanCurrent {
		return errors.New("release is not newer than the running version")
	}
	c.setDefaults()
	if err := c.validateAssetURL(plan.Archive.BrowserDownloadURL); err != nil {
		return fmt.Errorf("archive URL: %w", err)
	}
	if err := c.validateAssetURL(plan.Checksum.BrowserDownloadURL); err != nil {
		return fmt.Errorf("checksum URL: %w", err)
	}

	checksumBody, err := c.download(ctx, plan.Checksum, maxChecksum)
	if err != nil {
		return fmt.Errorf("download checksum: %w", err)
	}
	expected, err := parseChecksum(checksumBody, plan.ArchiveName)
	if err != nil {
		return fmt.Errorf("read checksum: %w", err)
	}

	archiveBody, err := c.download(ctx, plan.Archive, maxArchive)
	if err != nil {
		return fmt.Errorf("download archive: %w", err)
	}
	actual := sha256.Sum256(archiveBody)
	if !bytes.Equal(actual[:], expected) {
		return fmt.Errorf("archive checksum mismatch: got %x", actual)
	}
	if plan.Archive.Digest != "" {
		apiDigest := strings.TrimPrefix(plan.Archive.Digest, "sha256:")
		if len(apiDigest) != sha256.Size*2 || !strings.EqualFold(apiDigest, hex.EncodeToString(actual[:])) {
			return errors.New("archive does not match the digest reported by GitHub")
		}
	}

	files, err := extractBinaries(archiveBody)
	if err != nil {
		return err
	}
	for name, data := range files {
		if err := validateELF(data, c.GOARCH); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := replaceBinaries(executablePath, files); err != nil {
		return fmt.Errorf("install application update: %w", err)
	}
	return nil
}

func (c *Client) setDefaults() {
	if c.HTTP == nil {
		c.HTTP = NewClient().HTTP
	}
	if c.APIBaseURL == "" {
		c.APIBaseURL = defaultAPIBase
	}
	if c.Repository == "" {
		c.Repository = defaultRepository
	}
	if c.GOOS == "" {
		c.GOOS = runtime.GOOS
	}
	if c.GOARCH == "" {
		c.GOARCH = runtime.GOARCH
	}
}

func (c *Client) latestRelease(ctx context.Context, includePrerelease bool) (Release, error) {
	if !includePrerelease {
		var release Release
		endpoint := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(c.APIBaseURL, "/"), c.Repository)
		if err := c.getJSON(ctx, endpoint, &release); err != nil {
			return Release{}, fmt.Errorf("get latest stable release: %w", err)
		}
		return release, nil
	}

	var releases []Release
	endpoint := fmt.Sprintf("%s/repos/%s/releases?per_page=30", strings.TrimRight(c.APIBaseURL, "/"), c.Repository)
	if err := c.getJSON(ctx, endpoint, &releases); err != nil {
		return Release{}, fmt.Errorf("list releases: %w", err)
	}
	var selected *Release
	for i := range releases {
		release := &releases[i]
		if release.Draft {
			continue
		}
		if _, err := cleanVersion(release.TagName); err != nil {
			continue
		}
		if selected == nil {
			selected = release
			continue
		}
		cmp, err := compareVersions(release.TagName, selected.TagName)
		if err == nil && cmp > 0 {
			selected = release
		}
	}
	if selected == nil {
		return Release{}, errors.New("no published release found")
	}
	return *selected, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Jabridge-self-update")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("GitHub returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxAPIResponse))
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

func (c *Client) download(ctx context.Context, asset Asset, limit int64) ([]byte, error) {
	if asset.Size < 0 || asset.Size > limit {
		return nil, fmt.Errorf("asset size %d exceeds limit %d", asset.Size, limit)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "Jabridge-self-update")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("download exceeds %d bytes", limit)
	}
	return body, nil
}

func (c *Client) validateAssetURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if c.APIBaseURL != defaultAPIBase {
		return nil // Tests and private mirrors may use a local HTTP server.
	}
	if u.Scheme != "https" || u.Hostname() != "github.com" {
		return fmt.Errorf("expected an HTTPS github.com URL, got %s", u.Redacted())
	}
	prefix := "/" + defaultRepository + "/releases/download/"
	if !strings.HasPrefix(u.EscapedPath(), prefix) {
		return fmt.Errorf("URL is outside %s releases", defaultRepository)
	}
	return nil
}

func isGitHubDownloadHost(host string) bool {
	return host == "github.com" || host == "objects.githubusercontent.com" ||
		host == "release-assets.githubusercontent.com"
}

func findAsset(assets []Asset, name string) (Asset, bool) {
	for _, asset := range assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return Asset{}, false
}

func parseChecksum(body []byte, archiveName string) ([]byte, error) {
	fields := strings.Fields(string(body))
	if len(fields) != 2 {
		return nil, errors.New("checksum file must contain one SHA-256 line")
	}
	if strings.TrimPrefix(fields[1], "*") != archiveName {
		return nil, fmt.Errorf("checksum names %q, expected %q", fields[1], archiveName)
	}
	digest, err := hex.DecodeString(fields[0])
	if err != nil || len(digest) != sha256.Size {
		return nil, errors.New("checksum is not a valid SHA-256 value")
	}
	return digest, nil
}

func extractBinaries(archive []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	defer gz.Close()

	wanted := map[string]bool{"jabridge": true, "jafw": true}
	allowedExtra := map[string]bool{
		"README.md":           true,
		"HARDWARE_TESTING.md": true,
		"LICENSE":             true,
		"jabridge.bash":       true,
		"jafw.bash":           true,
	}
	files := make(map[string][]byte, len(wanted))
	reader := tar.NewReader(gz)
	var extracted int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read release archive: %w", err)
		}
		clean := path.Clean(header.Name)
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("unsafe archive path %q", header.Name)
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("unsupported archive entry %q", header.Name)
		}
		name := path.Base(clean)
		if !wanted[name] {
			if !allowedExtra[name] || header.Size < 0 || header.Size > maxExtraFile {
				return nil, fmt.Errorf("unexpected file %q in release archive", header.Name)
			}
			extracted += header.Size
			if extracted > maxExtracted {
				return nil, errors.New("release archive expands past its safety limit")
			}
			if _, err := io.CopyN(io.Discard, reader, header.Size); err != nil {
				return nil, fmt.Errorf("read %s: %w", name, err)
			}
			continue
		}
		if _, exists := files[name]; exists {
			return nil, fmt.Errorf("duplicate %s in release archive", name)
		}
		if header.Size < 1 || header.Size > maxBinary {
			return nil, fmt.Errorf("invalid size %d for %s", header.Size, name)
		}
		extracted += header.Size
		if extracted > maxExtracted {
			return nil, errors.New("release archive expands past its safety limit")
		}
		data, err := io.ReadAll(io.LimitReader(reader, maxBinary+1))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		if int64(len(data)) != header.Size {
			return nil, fmt.Errorf("short %s data", name)
		}
		files[name] = data
	}
	for name := range wanted {
		if len(files[name]) == 0 {
			return nil, fmt.Errorf("release archive is missing %s", name)
		}
	}
	return files, nil
}

func validateELF(data []byte, goarch string) error {
	file, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("not a valid ELF program: %w", err)
	}
	defer file.Close()
	want := elf.EM_NONE
	switch goarch {
	case "amd64":
		want = elf.EM_X86_64
	case "arm64":
		want = elf.EM_AARCH64
	default:
		return fmt.Errorf("unsupported architecture %s", goarch)
	}
	if file.Machine != want {
		return fmt.Errorf("wrong machine type %s, expected %s", file.Machine, want)
	}
	if file.Type != elf.ET_EXEC {
		return fmt.Errorf("ELF type is %s, expected executable", file.Type)
	}
	return nil
}

func replaceBinaries(executablePath string, files map[string][]byte) error {
	if executablePath == "" {
		return errors.New("running executable path is empty")
	}
	resolved, err := filepath.EvalSymlinks(executablePath)
	if err != nil {
		return fmt.Errorf("resolve running executable: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("inspect running executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("running executable is not a regular file")
	}

	targetDir := filepath.Dir(resolved)
	stagingDir, err := os.MkdirTemp(targetDir, ".jabridge-update-")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w", targetDir, err)
	}
	defer os.RemoveAll(stagingDir)

	type updateFile struct {
		name      string
		stage     string
		target    string
		backup    string
		hadOld    bool
		installed bool
	}
	updates := []updateFile{
		{name: "jafw", target: filepath.Join(targetDir, "jafw")},
		{name: "jabridge", target: resolved},
	}
	for i := range updates {
		item := &updates[i]
		data, ok := files[item.name]
		if !ok {
			return fmt.Errorf("new release is missing %s", item.name)
		}
		item.stage = filepath.Join(stagingDir, "new-"+item.name)
		item.backup = filepath.Join(stagingDir, "old-"+item.name)
		if err := writeExecutable(item.stage, data); err != nil {
			return err
		}
		if old, err := os.Lstat(item.target); err == nil {
			if old.Mode()&os.ModeSymlink != 0 || !old.Mode().IsRegular() {
				return fmt.Errorf("refusing to replace non-regular path %s", item.target)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	rollback := func(last int) {
		for i := last; i >= 0; i-- {
			item := &updates[i]
			if item.installed {
				_ = os.Remove(item.target)
			}
			if item.hadOld {
				_ = os.Rename(item.backup, item.target)
			}
		}
	}

	for i := range updates {
		item := &updates[i]
		if _, err := os.Stat(item.target); err == nil {
			if err := os.Rename(item.target, item.backup); err != nil {
				rollback(i - 1)
				return fmt.Errorf("back up %s: %w", item.name, err)
			}
			item.hadOld = true
		}
		if err := os.Rename(item.stage, item.target); err != nil {
			rollback(i)
			return fmt.Errorf("replace %s: %w", item.name, err)
		}
		item.installed = true
	}
	if err := syncDirectory(targetDir); err != nil {
		rollback(len(updates) - 1)
		return err
	}
	return nil
}

func writeExecutable(name string, data []byte) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return fmt.Errorf("stage %s: %w", filepath.Base(name), err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Chmod(name, 0o755)
}

func syncDirectory(name string) error {
	dir, err := os.Open(name)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

type parsedVersion struct {
	major, minor, patch int
	prerelease          []string
}

func cleanVersion(version string) (string, error) {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if _, err := parseVersion(version); err != nil {
		return "", err
	}
	return version, nil
}

func compareVersions(left, right string) (int, error) {
	l, err := parseVersion(strings.TrimPrefix(strings.TrimSpace(left), "v"))
	if err != nil {
		return 0, fmt.Errorf("invalid version %q", left)
	}
	r, err := parseVersion(strings.TrimPrefix(strings.TrimSpace(right), "v"))
	if err != nil {
		return 0, fmt.Errorf("invalid version %q", right)
	}
	for _, pair := range [][2]int{{l.major, r.major}, {l.minor, r.minor}, {l.patch, r.patch}} {
		if pair[0] < pair[1] {
			return -1, nil
		}
		if pair[0] > pair[1] {
			return 1, nil
		}
	}
	if len(l.prerelease) == 0 && len(r.prerelease) == 0 {
		return 0, nil
	}
	if len(l.prerelease) == 0 {
		return 1, nil
	}
	if len(r.prerelease) == 0 {
		return -1, nil
	}
	for i := 0; i < len(l.prerelease) && i < len(r.prerelease); i++ {
		li, ri := l.prerelease[i], r.prerelease[i]
		if li == ri {
			continue
		}
		ln, lnum := numericIdentifier(li)
		rn, rnum := numericIdentifier(ri)
		switch {
		case lnum && rnum && ln < rn:
			return -1, nil
		case lnum && rnum:
			return 1, nil
		case lnum:
			return -1, nil
		case rnum:
			return 1, nil
		case li < ri:
			return -1, nil
		default:
			return 1, nil
		}
	}
	if len(l.prerelease) < len(r.prerelease) {
		return -1, nil
	}
	if len(l.prerelease) > len(r.prerelease) {
		return 1, nil
	}
	return 0, nil
}

func parseVersion(version string) (parsedVersion, error) {
	version = strings.SplitN(version, "+", 2)[0]
	parts := strings.SplitN(version, "-", 2)
	core := strings.Split(parts[0], ".")
	if len(core) != 3 {
		return parsedVersion{}, errors.New("version must have major.minor.patch")
	}
	values := make([]int, 3)
	for i, value := range core {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return parsedVersion{}, errors.New("version contains a non-numeric core value")
		}
		values[i] = parsed
	}
	result := parsedVersion{major: values[0], minor: values[1], patch: values[2]}
	if len(parts) == 2 {
		if parts[1] == "" {
			return parsedVersion{}, errors.New("empty prerelease value")
		}
		result.prerelease = strings.Split(parts[1], ".")
		for _, identifier := range result.prerelease {
			if identifier == "" {
				return parsedVersion{}, errors.New("empty prerelease identifier")
			}
		}
	}
	return result, nil
}

func numericIdentifier(value string) (int, bool) {
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.Atoi(value)
	return parsed, err == nil
}
