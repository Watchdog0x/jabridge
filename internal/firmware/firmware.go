// Package firmware implements Jabridge firmware information and update flows.
//
// Normal commands list supported devices, read public firmware metadata,
// download files, and verify the target model. They do not write to hardware.
// Firmware installation requires exact target validation and explicit typed or
// automation confirmation. Interrupted-transfer recovery is not qualified.
// No vendor program, library, or firmware is bundled.

package firmware

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/md5" // Jabra publishes MD5 as a release identity checksum.
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Watchdog0x/jabridge/internal/buildinfo"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
	"golang.org/x/term"
)

// ── Service and protocol constants ────────────────────────────────────────

const (
	// Jabra's USB vendor ID, universal across every product in their lineup.
	JabraVendorID = 0x0b0e

	// Public firmware metadata service used for compatibility checks.
	MetadataBaseURL = "https://sdkbackend.jabra.com/v4/Firmware"

	// Firmware download host returned by the metadata service.
	DownloadBaseURL = "https://sdkbackend.jabra.com"

	// Identify this client honestly rather than impersonating a vendor SDK.
	UserAgent = "Jabridge/1.0.0 (+https://github.com/Watchdog0x/jabridge)"

	// Timeout budget per HTTP call. Metadata is ~1KB, download can be MBs.
	MetadataTimeout         = 15 * time.Second
	DownloadTimeout         = 30 * time.Minute
	MaxFirmwareSize         = int64(4 << 30)
	MaxExpandedArchiveSize  = int64(256 << 20)
	MaxFirmwareManifestSize = int64(1 << 20)

	// Hardware writes are intentionally opt-in while the native updater is
	// awaiting validation on replaceable test hardware.
	HardwareWriteEnv        = "JABRIDGE_FIRMWARE_ENABLE_HARDWARE_WRITES"
	HardwareWriteAck        = "I_ACCEPT_THE_BRICK_RISK"
	HardwareWriteFlag       = "--i-accept-risk"
	legacyHardwareWriteFlag = "--i-accept-brick-risk"
)

var commandLineRiskAccepted atomic.Bool

func requireHardwareWrites() error {
	if !commandLineRiskAccepted.Load() && os.Getenv(HardwareWriteEnv) != HardwareWriteAck {
		return fmt.Errorf("firmware installation requires the explicit %s option", HardwareWriteFlag)
	}
	return nil
}

// ── XML response schema (matches observed server response exactly) ─────────

type Firmware struct {
	XMLName    xml.Name  `xml:"Firmware"`
	DeviceName string    `xml:"DeviceName"`
	Status     string    `xml:"Status"`
	Releases   []Release `xml:"Releases>Release"`
}

type Release struct {
	Version          string     `xml:"Version"`
	ReleaseDate      string     `xml:"ReleaseDate"`
	DownloadURL      string     `xml:"DownloadUrl"`
	Stage            string     `xml:"Stage"`
	FileName         string     `xml:"FileName"`
	FileSize         string     `xml:"FileSize"`
	Languages        []Language `xml:"languages>language"`
	OfficialMD5      string     `xml:"-"`
	CompatiblePIDs   []uint16   `xml:"-"`
	FirmwareProtocol []int      `xml:"-"`
}

type Language struct {
	ID   string `xml:"id,attr"`
	Name string `xml:",chardata"`
}

// LatestInfo is the newest firmware entry published for one product ID.
type LatestInfo struct {
	DeviceName  string
	ProductID   uint16
	Version     string
	ReleaseDate string
	FileName    string
	FileSize    string
}

// DownloadResult describes a downloaded firmware file. Downloading does not
// send anything to a device.
type DownloadResult struct {
	Path    string
	Version string
	Format  string
}

var firmwareModelCatalog = modelcatalog.NewClient()

func addOfficialReleaseEvidence(pid uint16, release *Release) error {
	return addOfficialReleaseEvidenceContext(context.Background(), pid, release)
}

func addOfficialReleaseEvidenceContext(parent context.Context, pid uint16, release *Release) error {
	if release == nil {
		return errors.New("missing firmware release")
	}
	ctx, cancel := context.WithTimeout(parent, MetadataTimeout)
	defer cancel()
	evidence, err := firmwareModelCatalog.FirmwareRelease(ctx, pid, release.Version)
	if err != nil {
		return fmt.Errorf("verify firmware in Jabra model catalog: %w", err)
	}
	if _, err := decodeOfficialMD5(evidence.MD5Checksum); err != nil {
		return fmt.Errorf("invalid published firmware checksum: %w", err)
	}
	release.OfficialMD5 = evidence.MD5Checksum
	release.CompatiblePIDs = append([]uint16(nil), evidence.CompatiblePIDs...)
	release.FirmwareProtocol = append([]int(nil), evidence.FirmwareProtocols...)
	return nil
}

// ── USB device enumeration via sysfs ───────────────────────────────────────

type USBDevice struct {
	SysPath      string
	VendorID     uint16
	ProductID    uint16
	Manufacturer string
	Product      string
	Serial       string
	ViaDongle    bool
	Firmware     string
}

// enumerateUSB walks /sys/bus/usb/devices and returns all devices matching
// the Jabra vendor ID. Pure Go, no cgo, no libusb. Works on any Linux system
// without root.
func enumerateUSB() ([]USBDevice, error) {
	root := "/sys/bus/usb/devices"
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}

	var devices []USBDevice
	for _, entry := range entries {
		dir := filepath.Join(root, entry.Name())
		vid, err := readHexSysfs(filepath.Join(dir, "idVendor"))
		if err != nil {
			continue // not a device (interface subdir, root hub, etc.)
		}
		if vid != JabraVendorID {
			continue
		}
		pid, err := readHexSysfs(filepath.Join(dir, "idProduct"))
		if err != nil {
			continue
		}
		dev := USBDevice{
			SysPath:      dir,
			VendorID:     vid,
			ProductID:    pid,
			Manufacturer: readTextSysfs(filepath.Join(dir, "manufacturer")),
			Product:      readTextSysfs(filepath.Join(dir, "product")),
			Serial:       readTextSysfs(filepath.Join(dir, "serial")),
		}
		devices = append(devices, dev)
	}
	return devices, nil
}

func readHexSysfs(path string) (uint16, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 16, 16)
	if err != nil {
		return 0, err
	}
	return uint16(n), nil
}

func readTextSysfs(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ── Firmware metadata fetch ────────────────────────────────────────────────

// fetchFirmwareInfo queries Jabra's public firmware API for the given product
// and returns the parsed response. VariantType is ignored server-side for
// most products, so we pass empty string.
func fetchFirmwareInfo(pid uint16) (*Firmware, error) {
	return fetchFirmwareInfoContext(context.Background(), pid)
}

func fetchFirmwareInfoContext(ctx context.Context, pid uint16) (*Firmware, error) {
	url := fmt.Sprintf("%s/%x?VendorId=%04x&VariantType=", MetadataBaseURL, pid, JabraVendorID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/xml")

	client := &http.Client{Timeout: MetadataTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no firmware catalog entry for product 0x%04x (Jabra does not have metadata for this PID)", pid)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var fw Firmware
	if err := xml.Unmarshal(body, &fw); err != nil {
		return nil, fmt.Errorf("parse xml: %w (first 200 bytes: %q)", err, trim(body, 200))
	}

	// Sort releases newest-first by version string (semver-ish, decent enough).
	sort.SliceStable(fw.Releases, func(i, j int) bool {
		return compareVersions(fw.Releases[i].Version, fw.Releases[j].Version) > 0
	})
	return &fw, nil
}

// LatestForPID returns the latest published firmware for a product.
func LatestForPID(pid uint16) (LatestInfo, error) {
	info, err := fetchFirmwareInfo(pid)
	if err != nil {
		return LatestInfo{}, err
	}
	if len(info.Releases) == 0 {
		return LatestInfo{}, fmt.Errorf("no firmware releases for PID 0x%04x", pid)
	}
	latest := info.Releases[0]
	return LatestInfo{
		DeviceName:  info.DeviceName,
		ProductID:   pid,
		Version:     latest.Version,
		ReleaseDate: latest.ReleaseDate,
		FileName:    latest.FileName,
		FileSize:    latest.FileSize,
	}, nil
}

// FirmwareDiagnostic verifies metadata and, if already downloaded, the exact
// published file. It never downloads a package or opens a hardware device.
type FirmwareDiagnostic struct {
	Latest            LatestInfo
	Protocols         []int
	ChecksumPublished bool
	Cached            bool
	ChecksumMatches   bool
	NativeLayout      bool
	Stage             string
}

func DiagnoseFirmware(ctx context.Context, pid uint16, cacheDir string) (FirmwareDiagnostic, error) {
	result := FirmwareDiagnostic{Stage: "metadata"}
	metadata, err := fetchFirmwareInfoContext(ctx, pid)
	if err != nil {
		return result, err
	}
	if len(metadata.Releases) == 0 {
		return result, errors.New("no published firmware")
	}
	release := metadata.Releases[0]
	result.Latest = LatestInfo{ProductID: pid, Version: release.Version, FileName: release.FileName, FileSize: release.FileSize}
	result.Stage = "published checksum"
	if err := addOfficialReleaseEvidenceContext(ctx, pid, &release); err != nil {
		return result, err
	}
	result.ChecksumPublished = true
	result.Protocols = append([]int(nil), release.FirmwareProtocol...)
	name := filepath.Base(release.FileName)
	if name == "." || name == "" || name == string(filepath.Separator) {
		return result, errors.New("invalid catalog filename")
	}
	path := filepath.Join(cacheDir, name)
	result.Stage = "cached file"
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		result.Stage = "not downloaded"
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		return result, errors.New("cached firmware is not a regular file")
	}
	result.Cached = true
	if info.Size() > 64<<20 {
		result.Stage = "file exceeds diagnostic size budget"
		return result, nil
	}
	if info.Size() <= 0 || info.Size() > MaxFirmwareSize {
		return result, errors.New("cached firmware has invalid size")
	}
	digest, err := firmwareFileMD5(path)
	if err != nil {
		return result, err
	}
	if digest != release.OfficialMD5 {
		return result, errors.New("cached firmware checksum mismatch")
	}
	result.ChecksumMatches = true
	result.Stage = "native layout"
	for _, protocol := range result.Protocols {
		if protocol == 7 {
			result.NativeLayout = validateNativeCSRArchive(path) == nil
			break
		}
	}
	result.Stage = "complete"
	return result, nil
}

// DownloadLatest downloads the latest firmware file without installing it.
func DownloadLatest(pid uint16, outDir string) (DownloadResult, error) {
	info, err := fetchFirmwareInfo(pid)
	if err != nil {
		return DownloadResult{}, err
	}
	if len(info.Releases) == 0 {
		return DownloadResult{}, fmt.Errorf("no firmware releases for PID 0x%04x", pid)
	}
	latest := info.Releases[0]
	if err := addOfficialReleaseEvidence(pid, &latest); err != nil {
		return DownloadResult{}, err
	}
	filePath, err := downloadFirmware(latest, outDir)
	if err != nil {
		return DownloadResult{}, err
	}
	format, err := detectFormat(filePath)
	if err != nil {
		return DownloadResult{}, err
	}
	return DownloadResult{Path: filePath, Version: latest.Version, Format: format.String()}, nil
}

// compareVersions does component-wise numeric semver compare. Avoids the
// classic "1.10.0 < 1.2.0" string-sort bug — same logic as nxbench task 001.
func compareVersions(a, b string) int {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, _ := strconv.Atoi(pa[i])
		nb, _ := strconv.Atoi(pb[i])
		if na != nb {
			if na < nb {
				return -1
			}
			return 1
		}
	}
	return len(pa) - len(pb)
}

func trim(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}

// ── Firmware download ──────────────────────────────────────────────────────

// downloadFirmware streams a release's DownloadUrl to a local file. Supports
// HTTP Range but we just do a single GET here — most firmware files are
// <5 MB and complete in a few seconds even on slow links.
func downloadFirmware(rel Release, outDir string) (string, error) {
	if rel.DownloadURL == "" {
		return "", errors.New("release has empty DownloadUrl")
	}
	if _, err := decodeOfficialMD5(rel.OfficialMD5); err != nil {
		return "", fmt.Errorf("release has no valid official checksum: %w", err)
	}
	url := DownloadBaseURL + rel.DownloadURL

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)

	client := &http.Client{Timeout: DownloadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http get: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download HTTP %d", resp.StatusCode)
	}

	// Ensure output directory exists. Name file after the server-side filename
	// if available, else synthesize from version.
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	name := strings.TrimSpace(rel.FileName)
	if name == "" {
		name = fmt.Sprintf("firmware-%s.bin", rel.Version)
	}
	name = filepath.Base(name)
	if name == "." || name == string(filepath.Separator) {
		return "", errors.New("release has an invalid FileName")
	}
	outPath := filepath.Join(outDir, name)

	if resp.ContentLength > MaxFirmwareSize {
		return "", fmt.Errorf("firmware is too large: %d bytes", resp.ContentLength)
	}
	f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			if cachedErr := validateCachedFirmware(outPath, rel, resp.ContentLength); cachedErr != nil {
				return "", fmt.Errorf("existing firmware file is not reusable: %w", cachedErr)
			}
			fmt.Fprintf(os.Stderr, "[jabridge firmware] using existing verified file %s\n", outPath)
			return outPath, nil
		}
		return "", fmt.Errorf("create without overwrite: %w", err)
	}
	complete := false
	defer func() {
		_ = f.Close()
		if !complete {
			_ = os.Remove(outPath)
		}
	}()

	digest := md5.New() // #nosec G401 -- compared with Jabra's published release checksum.
	written, err := io.Copy(io.MultiWriter(f, digest), io.LimitReader(resp.Body, MaxFirmwareSize+1))
	if err != nil {
		return "", fmt.Errorf("copy: %w", err)
	}
	if written > MaxFirmwareSize {
		return "", fmt.Errorf("firmware exceeded %d bytes", MaxFirmwareSize)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close: %w", err)
	}
	actualMD5 := base64.StdEncoding.EncodeToString(digest.Sum(nil))
	if actualMD5 != rel.OfficialMD5 {
		return "", fmt.Errorf("downloaded firmware checksum %s does not match Jabra's published checksum", actualMD5)
	}
	complete = true
	fmt.Fprintf(os.Stderr, "[jabridge firmware] downloaded %d bytes → %s\n", written, outPath)
	fmt.Fprintln(os.Stderr, "[jabridge firmware] official release checksum verified")
	return outPath, nil
}

func validateCachedFirmware(path string, release Release, expectedSize int64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() <= 0 || info.Size() > MaxFirmwareSize {
		return fmt.Errorf("invalid file size %d", info.Size())
	}
	if expectedSize > 0 && info.Size() != expectedSize {
		return fmt.Errorf("size %d does not match server size %d", info.Size(), expectedSize)
	}
	if release.OfficialMD5 != "" {
		actualMD5, err := firmwareFileMD5(path)
		if err != nil {
			return err
		}
		if actualMD5 != release.OfficialMD5 {
			return errors.New("cached file does not match Jabra's published checksum")
		}
	}
	format, err := detectFormat(path)
	if err != nil {
		return fmt.Errorf("detect format: %w", err)
	}
	if format == FormatUnknown {
		return errors.New("unknown firmware format")
	}
	if format == FormatGnVArchive {
		manifest, err := parseFirmwareManifest(path)
		if err != nil {
			return err
		}
		if release.Version != "" && manifest.Version != release.Version {
			return fmt.Errorf("archive version %q does not match requested %q", manifest.Version, release.Version)
		}
	}
	return nil
}

func decodeOfficialMD5(value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	if len(decoded) != md5.Size {
		return nil, fmt.Errorf("decoded length is %d, want %d", len(decoded), md5.Size)
	}
	return decoded, nil
}

func firmwareFileMD5(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > MaxFirmwareSize {
		return "", fmt.Errorf("firmware is not a valid regular file: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	digest := md5.New() // #nosec G401 -- compared with Jabra's published release checksum.
	written, err := io.Copy(digest, io.LimitReader(file, MaxFirmwareSize+1))
	if err != nil {
		return "", err
	}
	if written != info.Size() {
		return "", errors.New("firmware changed while hashing")
	}
	return base64.StdEncoding.EncodeToString(digest.Sum(nil)), nil
}

// ── Firmware format detection by magic bytes ──────────────────────────────

type FirmwareFormat int

const (
	FormatUnknown FirmwareFormat = iota
	FormatCSRDFU2
	FormatUSBDFU11
	FormatDfuSeSTM
	FormatGnVArchive // ZIP + info.xml with <buildVector> — Jabra's proprietary CSR-on-ZIP
	FormatPlainZIP
)

func (f FirmwareFormat) String() string {
	switch f {
	case FormatCSRDFU2:
		return "CSR DFU2 firmware"
	case FormatUSBDFU11:
		return "USB DFU-1.1 (standard — compatible with dfu-util)"
	case FormatDfuSeSTM:
		return "DfuSe (STMicro extended DFU — dfu-util with --alt)"
	case FormatGnVArchive:
		return "Jabra firmware archive"
	case FormatPlainZIP:
		return "plain ZIP (unknown contents)"
	}
	return "unknown"
}

// detectFormat reads the first and last bytes of a firmware file to identify
// its format. Based on magic-byte knowledge extracted from jfwu's strings and
// the downloaded BIZ 2400 II sample.
func detectFormat(path string) (FirmwareFormat, error) {
	f, err := os.Open(path)
	if err != nil {
		return FormatUnknown, err
	}
	defer func() { _ = f.Close() }()

	head := make([]byte, 16)
	if _, err := io.ReadFull(f, head); err != nil {
		return FormatUnknown, err
	}

	switch {
	case string(head[:8]) == "CSR-dfu2":
		return FormatCSRDFU2, nil
	case string(head[:5]) == "DfuSe":
		return FormatDfuSeSTM, nil
	case head[0] == 'P' && head[1] == 'K':
		// ZIP — check whether it contains info.xml with <buildVector>.
		// If so, it's a Jabra GnV archive (the proprietary CSR-on-ZIP format).
		if isGnVArchive(path) {
			return FormatGnVArchive, nil
		}
		return FormatPlainZIP, nil
	}

	// Standard USB DFU-1.1 files have the suffix signature "UFD" in the last
	// 16 bytes (DFU suffix struct). Check by reading the tail.
	stat, _ := f.Stat()
	if stat.Size() >= 16 {
		tail := make([]byte, 16)
		if _, err := f.ReadAt(tail, stat.Size()-16); err == nil {
			// DFU suffix: ...DFUxxxxUFD + 4 bytes CRC (dwCRC)
			if tail[8] == 'U' && tail[9] == 'F' && tail[10] == 'D' {
				return FormatUSBDFU11, nil
			}
		}
	}

	return FormatUnknown, nil
}

// isGnVArchive cheaply checks whether a ZIP contains info.xml with a
// <buildVector> root element. Doesn't fully parse — just a format sniff.
func isGnVArchive(path string) bool {
	r, err := zip.OpenReader(path)
	if err != nil {
		return false
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		if filepath.Base(f.Name) == "info.xml" {
			rc, err := f.Open()
			if err != nil {
				return false
			}
			head := make([]byte, 256)
			n, _ := io.ReadFull(rc, head)
			_ = rc.Close()
			return strings.Contains(string(head[:n]), "<buildVector")
		}
	}
	return false
}

// ── GnV archive manifest parsing ───────────────────────────────────────────

type BuildVector struct {
	XMLName                      xml.Name  `xml:"buildVector"`
	Version                      string    `xml:"version,attr"`
	ProductName                  string    `xml:"productName,attr"`
	ReleaseDate                  string    `xml:"releaseDate,attr"`
	PartialUploadAllowed         string    `xml:"partialUploadAllowed"`
	MaxPreloadCount              int       `xml:"maxPreloadCount"`
	MaxBlePreloadCount           int       `xml:"maxBlePreloadCount"`
	MinFirmwareUpdaterAppVersion string    `xml:"minFirmwareUpdaterAppVersion"`
	TargetUSBPIDs                []string  `xml:"targetUsbPids>usbPid"`
	Files                        []GnVFile `xml:"files>file"`
}

type GnVFile struct {
	Name      string `xml:"name,attr"`
	Content   string `xml:"content"`
	Version   string `xml:"version"`
	Target    string `xml:"target"`
	Partition int    `xml:"partition"`
	CRC       string `xml:"crc"`
	Language  struct {
		ID   string `xml:"id,attr"`
		Name string `xml:",chardata"`
	} `xml:"language"`
}

func parseFirmwareManifest(path string) (*BuildVector, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	defer func() { _ = r.Close() }()
	for _, file := range r.File {
		if filepath.Base(file.Name) != "info.xml" {
			continue
		}
		if file.UncompressedSize64 > uint64(MaxFirmwareManifestSize) {
			return nil, fmt.Errorf("info.xml is too large: %d bytes", file.UncompressedSize64)
		}
		reader, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("open info.xml: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, MaxFirmwareManifestSize+1))
		closeErr := reader.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read info.xml: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close info.xml: %w", closeErr)
		}
		if int64(len(data)) > MaxFirmwareManifestSize {
			return nil, fmt.Errorf("info.xml exceeds %d bytes", MaxFirmwareManifestSize)
		}
		var manifest BuildVector
		if err := xml.Unmarshal(data, &manifest); err != nil {
			return nil, fmt.Errorf("parse info.xml: %w", err)
		}
		return &manifest, nil
	}
	return nil, errors.New("info.xml not found in archive")
}

// parseGnVArchive opens a GnV archive ZIP, extracts info.xml, parses it, and
// returns the manifest plus a map of file-name → decompressed bytes.
func parseGnVArchive(path string) (*BuildVector, map[string][]byte, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open zip: %w", err)
	}
	defer func() { _ = r.Close() }()

	contents := make(map[string][]byte, len(r.File))
	var infoXML []byte
	var expandedSize int64
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > uint64(MaxExpandedArchiveSize) ||
			expandedSize > MaxExpandedArchiveSize-int64(f.UncompressedSize64) {
			return nil, nil, fmt.Errorf("expanded firmware archive exceeds %d bytes", MaxExpandedArchiveSize)
		}
		expandedSize += int64(f.UncompressedSize64)
		rc, err := f.Open()
		if err != nil {
			return nil, nil, fmt.Errorf("open %s: %w", f.Name, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, int64(f.UncompressedSize64)+1))
		closeErr := rc.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		if closeErr != nil {
			return nil, nil, fmt.Errorf("close %s: %w", f.Name, closeErr)
		}
		contents[filepath.Base(f.Name)] = data
		if filepath.Base(f.Name) == "info.xml" {
			infoXML = data
		}
	}
	if infoXML == nil {
		return nil, nil, errors.New("info.xml not found in archive")
	}

	var bv BuildVector
	if err := xml.Unmarshal(infoXML, &bv); err != nil {
		return nil, nil, fmt.Errorf("parse info.xml: %w", err)
	}
	return &bv, contents, nil
}

func parseTargetPIDs(values []string) ([]uint16, error) {
	pids := make([]uint16, 0, len(values))
	seen := make(map[uint16]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(strings.ToLower(raw))
		value = strings.TrimPrefix(value, "0x")
		if value == "" {
			continue
		}
		parsed, err := strconv.ParseUint(value, 16, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid target USB PID %q: %w", raw, err)
		}
		pid := uint16(parsed)
		if _, duplicate := seen[pid]; duplicate {
			continue
		}
		seen[pid] = struct{}{}
		pids = append(pids, pid)
	}
	if len(pids) == 0 {
		return nil, errors.New("firmware manifest has no target USB PID")
	}
	return pids, nil
}

func validateAttachedFirmwareTarget(path string) error {
	manifest, err := parseFirmwareManifest(path)
	if err != nil {
		return fmt.Errorf("read firmware manifest: %w", err)
	}
	targets, err := parseTargetPIDs(manifest.TargetUSBPIDs)
	if err != nil {
		return err
	}
	devices, err := enumerateFirmwareTargets()
	if err != nil {
		return fmt.Errorf("enumerate Jabra devices: %w", err)
	}
	checksum, err := firmwareFileMD5(path)
	if err != nil {
		return fmt.Errorf("hash firmware: %w", err)
	}

	var catalogErrors []string
	for _, device := range devices {
		if device.VendorID != JabraVendorID {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), MetadataTimeout)
		evidence, lookupErr := firmwareModelCatalog.FirmwareRelease(ctx, device.ProductID, manifest.Version)
		cancel()
		if lookupErr != nil {
			catalogErrors = append(catalogErrors, fmt.Sprintf("0x%04X: %v", device.ProductID, lookupErr))
			continue
		}
		if firmwareReleaseMatchesDevice(checksum, device.ProductID, evidence) {
			fmt.Fprintf(os.Stderr, "[jabridge firmware] official checksum matches attached PID 0x%04X\n", device.ProductID)
			return nil
		}
	}

	targetStrings := make([]string, 0, len(targets))
	for _, target := range targets {
		targetStrings = append(targetStrings, fmt.Sprintf("0x%04X", target))
	}
	attachedStrings := make([]string, 0, len(devices))
	for _, device := range devices {
		attachedStrings = append(attachedStrings, fmt.Sprintf("0x%04X", device.ProductID))
	}
	if len(attachedStrings) == 0 {
		attachedStrings = append(attachedStrings, "none")
	}
	detail := ""
	if len(catalogErrors) > 0 {
		detail = "; catalog check: " + strings.Join(catalogErrors, "; ")
	}
	return fmt.Errorf(
		"firmware %s target %s does not have the published checksum for attached Jabra PID %s%s",
		manifest.Version, strings.Join(targetStrings, ","), strings.Join(attachedStrings, ","), detail,
	)
}

func firmwareReleaseMatchesDevice(checksum string, pid uint16, evidence *modelcatalog.ReleaseEvidence) bool {
	return evidence != nil && checksum != "" && checksum == evidence.MD5Checksum && containsPID(evidence.CompatiblePIDs, pid)
}

func containsPID(values []uint16, wanted uint16) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func firmwareTargetsAttachedDevice(targets []uint16, devices []USBDevice) bool {
	for _, device := range devices {
		for _, target := range targets {
			if device.VendorID == JabraVendorID && device.ProductID == target {
				return true
			}
		}
	}
	return false
}

// verifyManifestCRCs walks every file in the manifest, looks up its bytes in
// the archive, and computes a CRC32 for comparison. Returns a list of per-file
// results. The CRCs in info.xml are NOT standard CRC32 — they're CSR's own
// checksum variant, so we compute both standard CRC32 (IEEE polynomial) and
// CRC32-C (Castagnoli) for reference. A "match" on either is informative;
// neither matching doesn't necessarily mean the archive is corrupt, just that
// we don't know the exact CSR algorithm. This function is honest about that.
type CRCResult struct {
	FileName      string
	Partition     int
	DeclaredCRC   string
	ComputedCRC32 string
	ComputedCRCC  string
	MatchIEEE     bool
	MatchCastag   bool
}

func verifyManifestCRCs(bv *BuildVector, contents map[string][]byte) []CRCResult {
	results := make([]CRCResult, 0, len(bv.Files))
	castagnoli := crc32.MakeTable(crc32.Castagnoli)
	for _, f := range bv.Files {
		data, ok := contents[f.Name]
		if !ok {
			results = append(results, CRCResult{FileName: f.Name, Partition: f.Partition, DeclaredCRC: f.CRC})
			continue
		}
		ieee := crc32.ChecksumIEEE(data)
		cast := crc32.Checksum(data, castagnoli)
		declared := strings.ToLower(strings.TrimPrefix(f.CRC, "0x"))
		r := CRCResult{
			FileName:      f.Name,
			Partition:     f.Partition,
			DeclaredCRC:   f.CRC,
			ComputedCRC32: fmt.Sprintf("0x%08x", ieee),
			ComputedCRCC:  fmt.Sprintf("0x%08x", cast),
			MatchIEEE:     fmt.Sprintf("%08x", ieee) == declared,
			MatchCastag:   fmt.Sprintf("%08x", cast) == declared,
		}
		results = append(results, r)
	}
	return results
}

// ── Jfwu delegation for GnV archives ──────────────────────────────────────

// flashViaJfwu delegates the actual chip-level partition upload to the
// existing jfwu binary. This is the pragmatic path for GnV archives — we
// handle everything around the flash (download, verify, extract) in pure Go
// and only invoke jfwu for the proprietary CSR protocol step.
func flashViaJfwu(archivePath string, extraArgs []string) error {
	if err := requireHardwareWrites(); err != nil {
		return err
	}

	// Vendor binaries are never bundled. Use an explicitly configured path or
	// a binary installed on PATH from an authorized Jabra distribution.
	jfwuBin := os.Getenv("JABRIDGE_FIRMWARE_VENDOR_TOOL")
	if jfwuBin == "" {
		found, err := exec.LookPath("jfwu")
		if err != nil {
			return errors.New("jfwu binary not found; set JABRIDGE_FIRMWARE_VENDOR_TOOL to an authorized Jabra updater or install jfwu on PATH")
		}
		jfwuBin = found
	}
	if info, err := os.Stat(jfwuBin); err != nil || info.IsDir() {
		return fmt.Errorf("invalid JABRIDGE_FIRMWARE_VENDOR_TOOL path %q", jfwuBin)
	}

	abs, err := filepath.Abs(archivePath)
	if err != nil {
		return err
	}

	args := append([]string(nil), extraArgs...)
	if format, detectErr := detectFormat(abs); detectErr == nil && format == FormatGnVArchive {
		manifest, parseErr := parseFirmwareManifest(abs)
		if parseErr != nil {
			return parseErr
		}
		pids, parseErr := parseTargetPIDs(manifest.TargetUSBPIDs)
		if parseErr != nil {
			return parseErr
		}
		if len(pids) == 1 {
			args = append(args, "-p", fmt.Sprintf("%04X", pids[0]))
		}
	}
	// Current JabraCLI FWU syntax, confirmed from the official 1.6.48.0
	// AppImage: jfwu [options] -p <PID> -f <firmware.zip>.
	args = append(args, "-f", abs)

	cmd := exec.Command(jfwuBin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	fmt.Fprintf(os.Stderr, "[jabridge firmware] exec: %s %s\n", jfwuBin, strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("jfwu exited non-zero: %w", err)
	}
	fmt.Fprintln(os.Stderr, "[jabridge firmware] jfwu exit=0 — partition upload completed successfully")
	return nil
}

// ── Flash via dfu-util (standard USB DFU path only) ────────────────────────

func flashViaDfuUtil(firmwarePath string) error {
	if err := requireHardwareWrites(); err != nil {
		return err
	}
	dfuBin, err := exec.LookPath("dfu-util")
	if err != nil {
		return errors.New("dfu-util not found in PATH — install with `sudo pacman -S dfu-util` or the apt/dnf equivalent")
	}
	args := []string{
		"-d", fmt.Sprintf("%04x:", JabraVendorID),
		"-D", firmwarePath,
		"-R", // reset device after transfer
	}
	cmd := exec.Command(dfuBin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Fprintf(os.Stderr, "[jabridge firmware] exec: %s %s\n", dfuBin, strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dfu-util exited non-zero: %w", err)
	}
	// Do NOT claim success unless exit is 0. This is the draft-is-not-send rule.
	fmt.Fprintln(os.Stderr, "[jabridge firmware] dfu-util exit=0 — firmware transfer completed successfully")
	return nil
}

// ── Format-to-flasher dispatch ─────────────────────────────────────────────
//
// Single source of truth for "which backend flashes which firmware format".
// Factored out so the routing table can be unit-tested without touching
// real USB hardware or the external dfu-util / jfwu binaries.

type flashRoute int

const (
	routeNone flashRoute = iota
	routeDfuUtil
	routeJfwu
)

func (r flashRoute) String() string {
	switch r {
	case routeDfuUtil:
		return "dfu-util"
	case routeJfwu:
		return "jfwu"
	}
	return "none"
}

// routeFor returns the flasher that should handle a given firmware format.
// USB DFU-1.1 → dfu-util (fully open source, no proprietary binary). Every
// other known format routes through jfwu, which is Jabra's own updater and
// speaks the CSR-dfu2 / GnV / DfuSe protocols natively. Unknown formats
// return routeNone — we refuse to flash what we can't identify.
func routeFor(format FirmwareFormat) flashRoute {
	switch format {
	case FormatUSBDFU11:
		return routeDfuUtil
	case FormatCSRDFU2, FormatGnVArchive, FormatDfuSeSTM, FormatPlainZIP:
		return routeJfwu
	}
	return routeNone
}

// flashByFormat is the single entry point used by both `jabridge firmware dev flash` and
// the end-to-end `jabridge firmware dev all` pipeline. It picks the correct backend via
// routeFor and delegates. Error reporting is honest: if no backend handles
// the format, we return an error instead of silently skipping.
func flashByFormat(path string, format FirmwareFormat) error {
	if err := requireHardwareWrites(); err != nil {
		return err
	}
	if format == FormatGnVArchive {
		if err := validateAttachedFirmwareTarget(path); err != nil {
			return err
		}
	}
	route := routeFor(format)
	fmt.Fprintf(os.Stderr, "[jabridge firmware] format=%s → route=%s\n", format.String(), route.String())
	switch route {
	case routeDfuUtil:
		return flashViaDfuUtil(path)
	case routeJfwu:
		return flashViaJfwu(path, nil)
	}
	return fmt.Errorf("no flasher available for format %s — refusing to flash unknown firmware", format.String())
}

// ── CLI ────────────────────────────────────────────────────────────────────

// Run executes the firmware subcommand inside the main Jabridge binary.
func Run(args []string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if failure, ok := recovered.(commandFailure); ok {
				err = errors.New(failure.message)
				return
			}
			panic(recovered)
		}
	}()

	if len(args) == 0 {
		cmdStatus()
		return nil
	}

	switch args[0] {
	case "version", "--version", "-v":
		fmt.Printf("jabridge firmware %s (%s)\n", buildinfo.Version, buildinfo.Name)
	case "status", "list", "info":
		cmdStatus()
	case "check":
		cmdCheck(args[1:])
	case "download":
		cmdDownload(args[1:])
	case "verify":
		cmdVerify(args[1:])
	case "install":
		cmdInstall(args[1:])
	case "detect":
		cmdDetect(args[1:])
	case "manifest":
		cmdManifest(args[1:])
	case "-h", "--help", "help":
		usage()
	case "dev":
		cmdDeveloper(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown firmware command %q", args[0])
	}
	return nil
}

func cmdDeveloper(args []string) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		developerUsage()
		return
	}
	switch args[0] {
	case "flash":
		cmdFlash(args[1:])
	case "all":
		cmdAll(args[1:])
	case "bccmd-test":
		cmdBCCMDTest(args[1:])
	case "flash-csr-ota":
		cmdFlashCsrOta(args[1:])
	case "dongle-info":
		cmdDongleInfo(args[1:])
	case "device-version":
		cmdDeviceVersion(args[1:])
	default:
		die("unknown developer command %q; run jabridge firmware dev --help", args[0])
	}
}

// cmdBCCMDTest opens a /dev/hidraw node, parses the HID descriptor to pick
// the vendor-defined Report ID, sends a BCCMD PS_READ (non-destructive), and
// prints the response. First live validation of the reverse-engineered
// protocol against real Jabra hardware.
func cmdBCCMDTest(args []string) {
	if err := requireHardwareWrites(); err != nil {
		die("bccmd-test: %v", err)
	}
	if len(args) < 1 {
		die("usage: jabridge firmware dev bccmd-test <hidraw-path>  (e.g. /dev/hidraw8)")
	}
	hidrawPath := args[0]

	client, err := OpenBCCMD(hidrawPath)
	if err != nil {
		die("open %s: %v", hidrawPath, err)
	}
	defer func() { _ = client.Close() }()

	fmt.Printf("Opened %s — vendor Report ID: 0x%02x\n", hidrawPath, client.ReportID())
	fmt.Println()

	// PSKEY_BDADDR = 0x0001 — the Bluetooth device address. Every CSR
	// BlueCore chip has this key. Reading it is non-destructive and a
	// good sanity check that the protocol round-trips.
	fmt.Println("Sending BCCMD Get request (varid=0x7003 PS, key=0x0001 PSKEY_BDADDR, max_words=4)...")
	resp, err := client.PSRead(0x0001, 4)
	if err != nil {
		die("PSRead: %v", err)
	}
	fmt.Printf("Response (%d bytes): % x\n", len(resp), resp)

	// Best-effort decode of the response wire format based on the
	// inverse of the request. Honest about unknowns.
	if len(resp) >= 10 {
		fmt.Println()
		fmt.Println("Outer HID report bytes:")
		fmt.Printf("  [0]    Report ID: 0x%02x\n", resp[0])
		fmt.Printf("  [1..2] %02x %02x\n", resp[1], resp[2])
		fmt.Printf("  [3]    flag: 0x%02x (0x80 = request, other = response)\n", resp[3])
		fmt.Printf("  [4..5] constants: %02x %02x\n", resp[4], resp[5])
		if len(resp) >= 10 {
			fmt.Printf("  [6..7] seq/id: 0x%04x\n", uint16(resp[6])|uint16(resp[7])<<8)
			fmt.Printf("  [8..9] length: %d bytes\n", uint16(resp[8])|uint16(resp[9])<<8)
		}
		if len(resp) > 10 {
			fmt.Println()
			fmt.Println("BCCMD response payload starts at offset 10:")
			for i := 10; i < len(resp) && i < 40; i += 2 {
				if i+1 < len(resp) {
					word := uint16(resp[i]) | uint16(resp[i+1])<<8
					fmt.Printf("  +%02x: 0x%04x\n", i-10, word)
				}
			}
		}
	}
}

func cmdManifest(args []string) {
	if len(args) < 1 {
		die("usage: jabridge firmware manifest <gnv-archive.zip>")
	}
	bv, contents, err := parseGnVArchive(args[0])
	if err != nil {
		die("parse: %v", err)
	}
	fmt.Printf("Product:        %s\n", bv.ProductName)
	fmt.Printf("Version:        %s\n", bv.Version)
	fmt.Printf("Release date:   %s\n", bv.ReleaseDate)
	fmt.Printf("Target USB PID: %s\n", strings.Join(bv.TargetUSBPIDs, ", "))
	fmt.Printf("Min updater:    %s\n", bv.MinFirmwareUpdaterAppVersion)
	fmt.Printf("Partial upload: %s\n", bv.PartialUploadAllowed)
	fmt.Printf("Files (%d):\n", len(bv.Files))

	// Sort by partition number for readable display
	files := append([]GnVFile(nil), bv.Files...)
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Partition != files[j].Partition {
			return files[i].Partition < files[j].Partition
		}
		return files[i].Language.ID < files[j].Language.ID
	})
	for _, f := range files {
		size := len(contents[f.Name])
		lang := f.Language.Name
		if lang == "" {
			lang = f.Language.ID
		}
		fmt.Printf("  part %3d  %-60s  %8d B  crc=%s  (%s)\n", f.Partition, f.Name, size, f.CRC, lang)
	}

	// Verify CRCs (honest about not knowing the exact CSR algo)
	fmt.Println()
	fmt.Println("CRC verification (standard IEEE CRC32 + Castagnoli):")
	any := false
	for _, r := range verifyManifestCRCs(bv, contents) {
		if r.MatchIEEE || r.MatchCastag {
			fmt.Printf("  ✓ %s  (%s)\n", r.FileName, crcAlgoName(r))
			any = true
		}
	}
	if !any {
		fmt.Println("  (no files matched standard CRC32/CRC32-C — Jabra's manifest CRCs use a proprietary CSR algorithm, expected)")
	}
}

func crcAlgoName(r CRCResult) string {
	switch {
	case r.MatchIEEE && r.MatchCastag:
		return "IEEE + Castagnoli"
	case r.MatchIEEE:
		return "IEEE"
	case r.MatchCastag:
		return "Castagnoli"
	}
	return "neither"
}

func usage() {
	fmt.Fprintln(os.Stderr, `jabridge firmware — Jabridge firmware utility (pure Go, no libjabra.so)

Independent community software; not an official Jabra program.

Usage:
  jabridge firmware                  show device and firmware status
  jabridge firmware download         download for the attached device
  jabridge firmware verify FILE      check a file against the device
  jabridge firmware install FILE     run the experimental native installer

More:
  jabridge firmware download --pid HEX
  jabridge firmware dev --help

Install verifies the file's target and asks the user to type INSTALL. If an
earlier transfer did not finish, running the same command with the exact same
archive enters recovery retry and asks for RECOVER. Changed recovery PIDs are
never guessed.`)
}

func developerUsage() {
	fmt.Fprintln(os.Stderr, `Developer commands are experimental and may damage hardware:

  jabridge firmware dev flash FILE
  jabridge firmware dev all
  jabridge firmware dev bccmd-test HIDRAW_PATH
  jabridge firmware dev flash-csr-ota [OPTIONS] FILE
  jabridge firmware dev dongle-info HIDRAW_PATH
  jabridge firmware dev device-version HIDRAW_PATH

They remain blocked unless the exact development safety gate is enabled.`)
}

func cmdStatus() {
	devs, err := enumerateFirmwareTargets()
	if err != nil {
		die("scan USB devices: %v", err)
	}
	fmt.Printf("jabridge firmware %s\n", buildinfo.Version)
	if len(devs) == 0 {
		fmt.Println("No supported Jabra USB device found.")
		return
	}
	fmt.Printf("%d detected Jabra firmware target(s):\n", len(devs))
	for _, d := range devs {
		name := d.Product
		if name == "" {
			name = "Unknown device"
		}
		fmt.Printf("\n%s\n", name)
		fmt.Printf("  USB:             0b0e:%04x\n", d.ProductID)
		if d.ViaDongle {
			fmt.Println("  Connection:      through dongle")
		}
		installed := "unknown"
		probe := &JabraDevice{
			VendorID:      d.VendorID,
			ProductID:     d.ProductID,
			DeviceName:    name,
			USBDevicePath: d.SysPath,
			SerialNumber:  d.Serial,
			IsDongle:      isDonglePID(d.ProductID),
		}
		if d.ViaDongle && d.Firmware != "" {
			installed = d.Firmware
		} else if !d.ViaDongle {
			if hidrawPath, err := findHidrawForDevice(probe); err == nil {
				probe.HidrawPath = hidrawPath
				if version, err := GetFirmwareVersion(probe); err == nil && version != "" {
					installed = version
				}
			}
		}
		fmt.Printf("  Installed:       %s\n", installed)
		firmware, err := fetchFirmwareInfo(d.ProductID)
		if err != nil || len(firmware.Releases) == 0 {
			fmt.Println("  Latest firmware: unknown")
			continue
		}
		latest := firmware.Releases[0].Version
		fmt.Printf("  Latest firmware: %s\n", latest)
		if installed != "unknown" {
			status := "update available"
			if installed == latest {
				status = "up to date"
			}
			fmt.Printf("  Status:          %s\n", status)
		}
	}
	fmt.Println("\nRead-only check complete. No device was changed.")
}

func connectedDongleChildren(devices []USBDevice) []USBDevice {
	var children []USBDevice
	for _, device := range devices {
		if !isDonglePID(device.ProductID) {
			continue
		}
		probe := &JabraDevice{VendorID: device.VendorID, ProductID: device.ProductID, SerialNumber: device.Serial}
		hidrawPath, err := findHidrawForDevice(probe)
		if err != nil {
			continue
		}
		transport, err := OpenHidraw(hidrawPath)
		if err != nil {
			continue
		}
		productID, pidErr := QueryChildProductID(transport, 0x50, 750*time.Millisecond)
		if pidErr != nil || productID == 0 {
			_ = transport.Close()
			continue
		}
		name, nameErr := QueryChildName(transport, 0x51, 750*time.Millisecond)
		if nameErr != nil {
			_ = transport.Close()
			continue
		}
		firmwareVersion, _ := QueryFirmwareVersion(transport, 4, 0x52, 900*time.Millisecond)
		_ = transport.Close()
		if strings.TrimSpace(name) == "" {
			name = fmt.Sprintf("Jabra headset (PID %04x)", productID)
		}
		children = append(children, USBDevice{
			VendorID:  JabraVendorID,
			ProductID: productID,
			Product:   name,
			ViaDongle: true,
			Firmware:  firmwareVersion,
		})
	}
	return children
}

func cmdCheck(args []string) {
	if len(args) < 1 {
		die("usage: jabridge firmware check <pid-hex>")
	}
	pid := parsePID(args[0])
	fw, err := fetchFirmwareInfo(pid)
	if err != nil {
		die("fetch: %v", err)
	}
	fmt.Printf("Product: %s (PID 0x%04x)\n", fw.DeviceName, pid)
	fmt.Printf("Status:  %s\n", fw.Status)
	fmt.Printf("Releases (%d):\n", len(fw.Releases))
	for i, r := range fw.Releases {
		marker := "  "
		if i == 0 {
			marker = "→ " // highlight latest
		}
		fmt.Printf("%s%-10s  %s  %s  (%s)\n", marker, r.Version, r.ReleaseDate, r.FileName, r.FileSize)
	}
}

func cmdDownload(args []string) {
	var pid uint16
	havePID := false
	outDir := "./firmware"
	var wantVersion string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pid":
			if i+1 >= len(args) {
				die("--pid needs a hexadecimal product ID")
			}
			i++
			pid = parsePID(args[i])
			havePID = true
		case "--version":
			if i+1 >= len(args) {
				die("--version needs a firmware version")
			}
			i++
			wantVersion = args[i]
		case "--out":
			if i+1 >= len(args) {
				die("--out needs a directory")
			}
			i++
			outDir = args[i]
		default:
			// Keep the old positional form during the 1.0 preview.
			if !havePID {
				pid = parsePID(args[i])
				havePID = true
			} else if wantVersion == "" {
				wantVersion = args[i]
			} else {
				die("unexpected download argument %q", args[i])
			}
		}
	}
	if !havePID {
		devs, err := enumerateFirmwareTargets()
		if err != nil {
			die("scan USB devices: %v", err)
		}
		switch len(devs) {
		case 0:
			die("no Jabra firmware target found; use --pid HEX for a device that is not attached")
		case 1:
			pid = devs[0].ProductID
		default:
			targets := make([]string, 0, len(devs))
			for _, device := range devs {
				targets = append(targets, fmt.Sprintf("%s (0x%04x)", device.Product, device.ProductID))
			}
			die("more than one firmware target found: %s; choose one with --pid HEX", strings.Join(targets, ", "))
		}
	}

	fw, err := fetchFirmwareInfo(pid)
	if err != nil {
		die("fetch: %v", err)
	}
	if len(fw.Releases) == 0 {
		die("no releases for PID 0x%04x", pid)
	}

	rel := fw.Releases[0] // latest (sorted newest-first)
	if wantVersion != "" {
		found := false
		for _, r := range fw.Releases {
			if r.Version == wantVersion {
				rel = r
				found = true
				break
			}
		}
		if !found {
			die("version %q not found for PID 0x%04x — available: %v", wantVersion, pid, versionsOf(fw.Releases))
		}
	}
	if err := addOfficialReleaseEvidence(pid, &rel); err != nil {
		die("download: %v", err)
	}

	fmt.Fprintf(os.Stderr, "[jabridge firmware] downloading %s %s (%s) from %s\n",
		fw.DeviceName, rel.Version, rel.FileSize, DownloadBaseURL+rel.DownloadURL)
	path, err := downloadFirmware(rel, outDir)
	if err != nil {
		die("download: %v", err)
	}

	format, _ := detectFormat(path)
	fmt.Printf("Downloaded: %s\n", path)
	fmt.Printf("Format:     %s\n", format.String())
	fmt.Printf("Version:    %s\n", rel.Version)
	fmt.Printf("Released:   %s\n", rel.ReleaseDate)
	fmt.Println("No device was changed.")
}

func enumerateFirmwareTargets() ([]USBDevice, error) {
	devices, err := enumerateUSB()
	if err != nil {
		return nil, err
	}
	devices = usableFirmwareDevices(devices)
	return append(devices, connectedDongleChildren(devices)...), nil
}

func usableFirmwareDevices(devices []USBDevice) []USBDevice {
	result := make([]USBDevice, 0, len(devices))
	for _, device := range devices {
		name := strings.ToLower(device.Product)
		if strings.Contains(name, "deskstand") || strings.Contains(name, "desk stand") ||
			strings.Contains(name, "charger") || strings.Contains(name, "cradle") {
			continue
		}
		result = append(result, device)
	}
	return result
}

func cmdDetect(args []string) {
	if len(args) < 1 {
		die("usage: jabridge firmware detect <file>")
	}
	format, err := detectFormat(args[0])
	if err != nil {
		die("detect: %v", err)
	}
	fmt.Printf("%s: %s\n", args[0], format.String())
}

func cmdFlash(args []string) {
	if len(args) < 1 {
		die("usage: jabridge firmware install <file>")
	}
	if err := requireHardwareWrites(); err != nil {
		die("firmware install: %v", err)
	}
	path := args[0]
	format, err := detectFormat(path)
	if err != nil {
		die("detect: %v", err)
	}
	if err := flashByFormat(path, format); err != nil {
		die("flash: %v", err)
	}
}

func cmdInstall(args []string) {
	path, accepted, err := parseInstallArgs(args)
	if err != nil {
		die("firmware install: %v", err)
	}
	if !accepted && !term.IsTerminal(int(os.Stdin.Fd())) {
		die("firmware install needs interactive confirmation; run it in a terminal or use %s for deliberate automation", HardwareWriteFlag)
	}
	format, err := detectFormat(path)
	if err != nil {
		die("detect: %v", err)
	}
	if format != FormatGnVArchive {
		die("the native installer currently supports only Jabra firmware archives, got %s", format.String())
	}
	if err := validateAttachedFirmwareTarget(path); err != nil {
		die("firmware target check: %v", err)
	}
	manifest, err := parseFirmwareManifest(path)
	if err != nil {
		die("firmware manifest: %v", err)
	}
	if err := validateNativeCSRArchive(path); err != nil {
		die("firmware protocol is not supported by the native installer: %v", err)
	}
	transfer, err := prepareFirmwareTransfer(path, manifest)
	if err != nil {
		die("firmware recovery state: %v", err)
	}
	confirmation := "INSTALL"
	if transfer.Recovery {
		confirmation = "RECOVER"
	}
	if !accepted {
		fmt.Fprintf(os.Stderr, "Firmware: %s %s\n", manifest.ProductName, manifest.Version)
		fmt.Fprintf(os.Stderr, "Target PID: %s\n", strings.Join(manifest.TargetUSBPIDs, ", "))
		if transfer.Recovery {
			fmt.Fprintln(os.Stderr, "Unfinished transfer found. Recovery will replay the same archive.")
		}
		if !confirmFirmwareAction(os.Stdin, os.Stderr, confirmation) {
			die("firmware install cancelled")
		}
	}
	if err := saveFirmwareRecoveryState(transfer.State); err != nil {
		die("save firmware recovery state: %v", err)
	}

	fmt.Fprintln(os.Stderr, "WARNING: experimental firmware transfer accepted by the user.")
	commandLineRiskAccepted.Store(true)
	defer commandLineRiskAccepted.Store(false)
	cmdFlashCsrOta([]string{"--force", path})
	if err := clearFirmwareRecoveryState(); err != nil {
		die("firmware transfer completed but recovery state cleanup failed: %v", err)
	}
}

// validateNativeCSRArchive rejects other Jabra updater formats before saving
// recovery state or opening a device. Protocol-7 CSR/GNP archives contain
// partitioned .gnv files, per-partition CRCs, and a final partition 254.
// Other firmware protocol families use different payloads and commands.
func validateNativeCSRArchive(path string) error {
	unpacked, err := UnpackGnVArchive(path)
	if err != nil {
		return err
	}
	if _, err := parseVersionTriplet(unpacked.Manifest.Version); err != nil {
		return err
	}
	if len(unpacked.Manifest.Files) == 0 {
		return errors.New("archive has no firmware partitions")
	}
	hasFooter := false
	for _, file := range unpacked.Manifest.Files {
		if !strings.EqualFold(filepath.Ext(file.Name), ".gnv") {
			return fmt.Errorf("payload %q is not a CSR/GNP .gnv partition", file.Name)
		}
		if strings.TrimSpace(file.CRC) == "" {
			return fmt.Errorf("payload %q has no partition CRC", file.Name)
		}
		if file.Partition == 254 {
			hasFooter = true
		}
	}
	if !hasFooter {
		return errors.New("archive has no final partition 254")
	}
	partitions, err := BuildOtaPartitions(unpacked, "0x0409")
	if err != nil {
		return err
	}
	if len(partitions) == 0 {
		return errors.New("archive has no usable CSR/GNP partitions")
	}
	return nil
}

func confirmFirmwareAction(reader io.Reader, writer io.Writer, confirmation string) bool {
	if _, err := fmt.Fprintf(writer, "Type %s to start the firmware transfer: ", confirmation); err != nil {
		return false
	}
	scanner := bufio.NewScanner(io.LimitReader(reader, 128))
	return scanner.Scan() && strings.TrimSpace(scanner.Text()) == confirmation
}

func parseInstallArgs(args []string) (path string, accepted bool, err error) {
	for _, argument := range args {
		switch argument {
		case HardwareWriteFlag:
			accepted = true
		case legacyHardwareWriteFlag:
			accepted = true
		default:
			if path != "" {
				return "", false, fmt.Errorf("unexpected argument %q", argument)
			}
			path = argument
		}
	}
	if path == "" {
		return "", false, fmt.Errorf("usage: jabridge firmware install FILE [%s]", HardwareWriteFlag)
	}
	return path, accepted, nil
}

func cmdVerify(args []string) {
	if len(args) < 1 {
		die("usage: jabridge firmware verify <firmware.zip>")
	}
	format, err := detectFormat(args[0])
	if err != nil {
		die("detect: %v", err)
	}
	if format != FormatGnVArchive {
		die("verify currently requires a GnV archive, got %s", format.String())
	}
	if err := validateAttachedFirmwareTarget(args[0]); err != nil {
		die("verify: %v", err)
	}
	fmt.Println("Official firmware bytes match an attached Jabra device; no device write was performed.")
}

// cmdAll is the end-to-end pipeline: enumerate, pick first Jabra device,
// fetch metadata, download latest, attempt flash.
func cmdAll(args []string) {
	if err := requireHardwareWrites(); err != nil {
		die("all: %v", err)
	}
	outDir := "./firmware"
	if len(args) >= 2 && args[0] == "--out" {
		outDir = args[1]
	}

	devs, err := enumerateUSB()
	if err != nil {
		die("enumerate: %v", err)
	}
	if len(devs) == 0 {
		die("no Jabra device attached")
	}
	target := devs[0]
	fmt.Fprintf(os.Stderr, "[jabridge firmware] targeting 0b0e:%04x %q\n", target.ProductID, target.Product)

	fw, err := fetchFirmwareInfo(target.ProductID)
	if err != nil {
		die("fetch: %v", err)
	}
	if len(fw.Releases) == 0 {
		die("no releases available for %s", fw.DeviceName)
	}
	latest := fw.Releases[0]
	if err := addOfficialReleaseEvidence(target.ProductID, &latest); err != nil {
		die("download: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[jabridge firmware] latest firmware: %s %s (%s)\n", fw.DeviceName, latest.Version, latest.ReleaseDate)

	path, err := downloadFirmware(latest, outDir)
	if err != nil {
		die("download: %v", err)
	}

	format, _ := detectFormat(path)
	fmt.Fprintf(os.Stderr, "[jabridge firmware] downloaded %s, format=%s\n", path, format.String())

	if err := flashByFormat(path, format); err != nil {
		die("flash: %v", err)
	}
}

// ── Helpers ────────────────────────────────────────────────────────────────

func parsePID(s string) uint16 {
	s = strings.TrimPrefix(strings.ToLower(s), "0x")
	n, err := strconv.ParseUint(s, 16, 16)
	if err != nil {
		die("invalid pid hex %q: %v", s, err)
	}
	return uint16(n)
}

func displaySerial(serial string) string {
	if serial == "" {
		return ""
	}
	if os.Getenv("JABRIDGE_FIRMWARE_SHOW_SERIAL") == "1" {
		return serial
	}
	return "<redacted>"
}

func versionsOf(rs []Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return out
}

type commandFailure struct {
	message string
}

func die(format string, args ...any) {
	panic(commandFailure{message: fmt.Sprintf(format, args...)})
}

// ── flash-csr-ota: pure-Go CSR OTA flash ──────────────────────────────────
//
// Syntax: jabridge firmware dev flash-csr-ota [--dry-run] [--force] [--hidraw /dev/hidrawN] <archive.zip>
//
// By default, the command walks /sys/bus/usb/devices for a Jabra VID
// device whose PID matches the archive's target USB PID, then locates
// the hidraw<N> node under that device's interface 3. You can override
// with --hidraw to point at a specific node (e.g. the dongle's hidraw7
// for testing dongle-child flows).
//
// --dry-run opens a scratch file instead of the real hidraw node and
// writes every outbound report to it as hex lines. Useful for sanity-
// checking the byte sequence before touching real hardware.
func cmdFlashCsrOta(args []string) {
	var (
		dryRun     bool
		force      bool
		useUSB     bool
		hidrawPath string
		archive    string
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dryRun = true
		case "--force":
			force = true
		case "--usb":
			useUSB = true
		case "--hidraw":
			if i+1 >= len(args) {
				die("--hidraw needs a value")
			}
			hidrawPath = args[i+1]
			i++
		default:
			if archive != "" {
				die("unexpected arg %q", args[i])
			}
			archive = args[i]
		}
	}
	if archive == "" {
		die("usage: jabridge firmware dev flash-csr-ota [--dry-run] [--usb] [--hidraw <path>] <firmware.zip>")
	}

	if !dryRun {
		if !force {
			die("live CSR OTA requires --force plus the %s acknowledgement", HardwareWriteEnv)
		}
		if err := requireHardwareWrites(); err != nil {
			die("flash-csr-ota: %v", err)
		}
	}

	// All three blocking issues resolved 2026-04-12:
	//   1. Pre-OTA init commands: 6-command sequence now in runOtaInit()
	//   2. Chunk format: byte[7..8] = 16-bit LE chunk index, byte[9] = actual
	//      data length, byte[10] = 0x00 always (verified 36415/36415 chunks)
	//   3. Variable chunk size handled by splitChunks() + buildWriteBlock()
	fmt.Fprintf(os.Stderr, "[flash-csr-ota] unpacking %s (pure-Go, no 7z)...\n", archive)
	unpacked, err := UnpackGnVArchive(archive)
	if err != nil {
		die("unpack: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[flash-csr-ota] %s v%s — target PID %s\n",
		unpacked.Manifest.ProductName, unpacked.Manifest.Version,
		strings.Join(unpacked.Manifest.TargetUSBPIDs, ","))

	partitions, err := BuildOtaPartitions(unpacked, "0x0409")
	if err != nil {
		die("build partitions: %v", err)
	}
	for _, p := range partitions {
		fmt.Fprintf(os.Stderr, "  part %3d  %7d bytes  crc=0x%08x\n", p.ID, len(p.Data), p.CRC32)
	}

	version, err := parseVersionTriplet(unpacked.Manifest.Version)
	if err != nil {
		die("version: %v", err)
	}

	// Locate hidraw node if not overridden (skip when using usbfs — it
	// detaches the kernel HID driver, removing the hidraw node).
	if hidrawPath == "" && !dryRun && !useUSB {
		targetPID := strings.ToLower(strings.TrimPrefix(unpacked.Manifest.TargetUSBPIDs[0], "0x"))
		path, err := findHidrawForJabraPID(targetPID)
		if err != nil {
			die("find hidraw: %v", err)
		}
		hidrawPath = path
		fmt.Fprintf(os.Stderr, "[flash-csr-ota] using %s\n", hidrawPath)
	}

	var tr OtaTransport
	if useUSB {
		// Use usbfs transport for configurable write timeouts (fixes
		// the footer chunk timeout). Pure Go — no libusb, no cgo.
		targetPID := strings.ToLower(strings.TrimPrefix(unpacked.Manifest.TargetUSBPIDs[0], "0x"))
		pid, _ := strconv.ParseUint(targetPID, 16, 16)
		ut, err := OpenUsbfs(JabraVendorID, uint16(pid), 30*time.Second)
		if err != nil {
			die("usbfs open: %v", err)
		}
		defer func() { _ = ut.Close() }()
		tr = ut
		fmt.Fprintf(os.Stderr, "[flash-csr-ota] using usbfs transport — 30s write timeout\n")
	} else if dryRun {
		dryPath := "/tmp/jabridge-firmware-dryrun.hex"
		f, err := os.Create(dryPath)
		if err != nil {
			die("dry-run create: %v", err)
		}
		defer func() { _ = f.Close() }()
		tr = &dryRunTransport{w: f}
		fmt.Fprintf(os.Stderr, "[flash-csr-ota] DRY RUN — writes go to %s, no device touched\n", dryPath)
	} else {
		hr, err := OpenHidraw(hidrawPath)
		if err != nil {
			die("open hidraw: %v", err)
		}
		defer func() { _ = hr.Close() }()
		tr = hr
	}

	// Auto-detect report size from the HID descriptor. Falls back to 63
	// if the descriptor can't be read (dry-run, usbfs, etc.).
	reportSize := 63
	if hidrawPath != "" {
		reportSize = DetectGnpReportSize(hidrawPath)
	}
	fmt.Fprintf(os.Stderr, "[flash-csr-ota] HID report size: %d bytes\n", reportSize)

	// Source address: 0 = auto-detect in runOtaInit (probe 0x08 then 0x01)
	srcAddr := byte(0)

	// Parse target PID for re-attach detection
	var targetPIDVal uint16
	if len(unpacked.Manifest.TargetUSBPIDs) > 0 {
		tpid := strings.ToLower(strings.TrimPrefix(unpacked.Manifest.TargetUSBPIDs[0], "0x"))
		pid64, _ := strconv.ParseUint(tpid, 16, 16)
		targetPIDVal = uint16(pid64)
	}

	opts := DefaultCsrOtaOptions()
	u := NewCsrOtaUpdater(tr, opts, reportSize, srcAddr, targetPIDVal)

	fmt.Fprintf(os.Stderr, "[flash-csr-ota] starting flash of %d partitions (version %d.%d.%d)...\n",
		len(partitions), version[0], version[1], version[2])
	if err := u.FlashAll(partitions, version); err != nil {
		die("flash: %v", err)
	}
	fmt.Fprintln(os.Stderr, "[flash-csr-ota] all partitions written — device should detach and re-attach now")
}

// dryRunTransport implements OtaTransport against a plain file. Write
// records each outbound report as a hex line; Read returns a fake ACK
// or event that lets the updater's state machine progress without a
// real device. NOT suitable for capturing event-level behavior — just
// for sanity-checking the byte sequence.
type dryRunTransport struct {
	w        io.Writer
	writeCnt int
}

func (d *dryRunTransport) Write(report []byte) error {
	if _, err := fmt.Fprintf(d.w, "# write %d\n%x\n", d.writeCnt, report); err != nil {
		return err
	}
	d.writeCnt++
	return nil
}

func (d *dryRunTransport) Read(timeout time.Duration) ([]byte, error) {
	// Return a canned ACK that the parser will accept. This is a HACK
	// meant only for dry-run mode. The seq byte is 0 which won't match
	// any real outgoing command, so the updater will retry/timeout —
	// dry-run mode is NOT end-to-end testable, it's just "print the
	// writes that would be made before the first response timeout".
	buf := make([]byte, GnpReportSize)
	buf[0] = 0x00
	buf[1] = GnpSrcHost
	buf[2] = 0x00
	buf[3] = 0xca
	buf[4] = 0xff
	return buf, nil
}

func (d *dryRunTransport) Close() error { return nil }

// findHidrawForJabraPID walks /sys/bus/usb/devices looking for a device
// whose VID is Jabra and PID matches the argument (lowercase hex).
// Returns the path to the hidraw<N> node under interface 3 of that
// device — e.g. "/dev/hidraw8" for the Evolve2 85.
//
// We look specifically at interface :1.3 because that's where jfwu's
// logs show the HID interface for CSR OTA ("1-5.1:1.3/0003:0B0E:24B9.../hidraw/hidraw8").
func findHidrawForJabraPID(pidHex string) (string, error) {
	devs, err := enumerateUSB()
	if err != nil {
		return "", err
	}
	want, err := strconv.ParseUint(pidHex, 16, 16)
	if err != nil {
		return "", fmt.Errorf("invalid pid hex %q: %w", pidHex, err)
	}
	for _, d := range devs {
		if d.ProductID != uint16(want) {
			continue
		}
		probe := &JabraDevice{VendorID: d.VendorID, ProductID: d.ProductID}
		if path, err := findHidrawForDevice(probe); err == nil {
			return path, nil
		}
		return "", fmt.Errorf("found Jabra 0b0e:%s but no matching hidraw node", pidHex)
	}
	return "", fmt.Errorf("no attached Jabra device with PID 0x%s", pidHex)
}

// ── dongle-info: query a Jabra dongle for its paired child device ─────────
//
// Syntax: jabridge firmware dev dongle-info <hidraw-path>
//
// Sends the dongle child-info queries (class 0x46) and prints the result:
// paired headset PID, serial, and name. Useful for verifying a Link 380
// is paired before trying to flash over RF, and for debugging the
// class-0x46 framing against real hardware.
// cmdDeviceVersion queries a Jabra device's firmware version directly via
// the GNP IDENT class (0x02 op 0x03). No libjabra.so needed — pure hidraw.
// This is the same query jfwu sends during init (CMD #4 in the analysis).
// Response format: 0xCC 0x02 0x03 <len> <version-ascii-string>
func cmdDeviceVersion(args []string) {
	if len(args) < 1 {
		die("usage: jabridge firmware dev device-version <hidraw-path>  (e.g. /dev/hidraw8)")
	}
	hr, err := OpenHidraw(args[0])
	if err != nil {
		die("open: %v", err)
	}
	defer func() { _ = hr.Close() }()

	// Try src=0x08 (headset) first. If the response is a NAK (byte[4]==0xFE),
	// retry with src=0x01 (dongle).
	var resp []byte
	var usedSrc byte
	for _, src := range []byte{GnpSrcHost, 0x01} {
		seq := byte(0x00)
		report := buildInitQuery(src, seq, 0x02, 0x03)
		if err := hr.Write(report); err != nil {
			die("write: %v", err)
		}
		resp, err = hr.Read(5 * time.Second)
		if err != nil {
			if src == 0x01 {
				die("read: %v (tried src=0x08 and src=0x01)", err)
			}
			fmt.Fprintf(os.Stderr, "[device-version] src=0x%02x timeout, trying src=0x01...\n", src)
			continue
		}
		// Check for NAK: strip report ID, check byte[4]
		check := resp
		if len(check) > 0 && check[0] == GnpReportID {
			check = check[1:]
		}
		if len(check) >= 5 && check[4] == 0xFE {
			fmt.Fprintf(os.Stderr, "[device-version] src=0x%02x NAK, trying src=0x01...\n", src)
			continue
		}
		usedSrc = src
		break
	}
	if resp == nil {
		die("no valid response from device")
	}

	// Strip HID report ID if present
	if len(resp) > 0 && resp[0] == GnpReportID {
		resp = resp[1:]
	}

	// Response: 00 <src> <seq> <flags|len> 02 03 <strlen> <version...>
	if len(resp) < 7 {
		die("response too short: %d bytes", len(resp))
	}
	// Payload starts at byte 6: [strlen] [version-chars...]
	vlen := int(resp[6])
	if vlen+7 > len(resp) {
		vlen = len(resp) - 7
	}
	version := string(resp[7 : 7+vlen])
	fmt.Printf("Firmware version: %s\n", version)
	fmt.Printf("  (queried via GNP IDENT class 0x02 op 0x03 on %s, src=0x%02x)\n", args[0], usedSrc)
}

func cmdDongleInfo(args []string) {
	if len(args) < 1 {
		die("usage: jabridge firmware dev dongle-info <hidraw-path>  (e.g. /dev/hidraw7)")
	}
	hr, err := OpenHidraw(args[0])
	if err != nil {
		die("open: %v", err)
	}
	defer func() { _ = hr.Close() }()

	pid, err := QueryChildProductID(hr, 0x05, 2*time.Second)
	if err != nil {
		die("queryChildProductID: %v", err)
	}
	name, err := QueryChildName(hr, 0x07, 2*time.Second)
	if err != nil {
		die("queryChildName: %v", err)
	}
	serial, err := QueryChildSerial(hr, 0x06, 2*time.Second)
	if err != nil {
		die("queryChildSerial: %v", err)
	}
	fmt.Printf("Paired child: %s\n", name)
	fmt.Printf("  PID:    0x%04x\n", pid)
	fmt.Printf("  Serial: %s\n", serial)
}
