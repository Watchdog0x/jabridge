package firmware

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"path"
	"strconv"
	"strings"
)

const maxCameraArchive = int64(2 << 30)

type bulkCameraProfile struct {
	Name        string
	ArchivePID  uint16
	RuntimePIDs []uint16
}

var bulkCameraProfiles = []bulkCameraProfile{
	{Name: "Jabra PanaCast 40 VBS", ArchivePID: 0x3080, RuntimePIDs: []uint16{0x3081, 0x3085, 0x3086}},
	{Name: "Jabra PanaCast U30", ArchivePID: 0x3093, RuntimePIDs: []uint16{0x3092, 0x3093, 0x3094}},
}

func bulkCameraProfileForPID(pid uint16) (bulkCameraProfile, bool) {
	for _, profile := range bulkCameraProfiles {
		if containsPID(profile.RuntimePIDs, pid) {
			return profile, true
		}
	}
	return bulkCameraProfile{}, false
}
func bulkCameraProfileForManifest(manifest *BuildVector) (bulkCameraProfile, bool) {
	if manifest == nil || len(manifest.Files) != 0 {
		return bulkCameraProfile{}, false
	}
	pids, err := parseTargetPIDs(manifest.TargetUSBPIDs)
	if err != nil || len(pids) != 1 {
		return bulkCameraProfile{}, false
	}
	for _, profile := range bulkCameraProfiles {
		if pids[0] == profile.ArchivePID {
			return profile, true
		}
	}
	return bulkCameraProfile{}, false
}
func isBulkCameraManifest(manifest *BuildVector) bool {
	_, ok := bulkCameraProfileForManifest(manifest)
	return ok
}

func validateCameraVersion(version string) error {
	parts := strings.Split(version, ".")
	if len(parts) < 3 || len(parts) > 4 {
		return errors.New("invalid camera firmware version")
	}
	for _, part := range parts {
		if part == "" || strings.Trim(part, "0123456789") != "" {
			return errors.New("invalid camera firmware version")
		}
		if _, err := strconv.ParseUint(part, 10, 32); err != nil {
			return err
		}
	}
	return nil
}

type bulkCameraArchive struct {
	Manifest *BuildVector
	Profile  bulkCameraProfile
	Size     int64
	MD5      [16]byte
}

func loadBulkCameraArchive(filename string) (*bulkCameraArchive, error) {
	file, info, err := openFirmwareRead(filename)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if info.Size() > maxCameraArchive {
		return nil, errors.New("camera archive exceeds the native size limit")
	}
	z, err := zip.NewReader(file, info.Size())
	if err != nil {
		return nil, err
	}
	manifest, err := parseFirmwareManifestFiles(z.File)
	if err != nil {
		return nil, err
	}
	profile, ok := bulkCameraProfileForManifest(manifest)
	if !ok {
		return nil, errors.New("not a supported camera bulk archive")
	}
	if err := validateCameraVersion(manifest.Version); err != nil {
		return nil, err
	}
	if len(z.File) > 256 {
		return nil, errors.New("camera archive has too many entries")
	}
	entries := map[string]*zip.File{}
	total := uint64(0)
	for _, entry := range z.File {
		name := entry.Name
		if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || path.Clean(name) != strings.TrimSuffix(name, "/") || strings.HasPrefix(name, "../") {
			return nil, errors.New("invalid camera archive entry path")
		}
		if _, duplicate := entries[name]; duplicate {
			return nil, errors.New("duplicate camera archive entry")
		}
		entries[name] = entry
		if entry.UncompressedSize64 > uint64(maxCameraArchive) || total > uint64(MaxFirmwareSize)-entry.UncompressedSize64 {
			return nil, errors.New("camera archive expands beyond its size limit")
		}
		total += entry.UncompressedSize64
	}
	payload, properties := entries["payload.bin"], entries["payload_properties.txt"]
	if payload == nil || properties == nil || entries["info.xml"] == nil || payload.UncompressedSize64 < 24 || properties.UncompressedSize64 > 4096 {
		return nil, errors.New("camera archive is missing its bounded payload metadata")
	}
	propertyReader, err := properties.Open()
	if err != nil {
		return nil, err
	}
	propertyData, readErr := io.ReadAll(io.LimitReader(propertyReader, 4097))
	closeErr := propertyReader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(propertyData) > 4096 {
		return nil, errors.New("camera payload properties are too large")
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(propertyData))
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		_, duplicate := values[key]
		if !ok || key == "" || duplicate {
			return nil, errors.New("invalid or duplicate camera payload property")
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	fileSize, err := strconv.ParseUint(values["FILE_SIZE"], 10, 64)
	if err != nil || fileSize != payload.UncompressedSize64 {
		return nil, errors.New("camera payload size does not match its properties")
	}
	metadataSize, err := strconv.ParseUint(values["METADATA_SIZE"], 10, 64)
	if err != nil || metadataSize < 24 || metadataSize > fileSize {
		return nil, errors.New("invalid camera payload metadata size")
	}
	wantedFile, err := base64.StdEncoding.DecodeString(values["FILE_HASH"])
	if err != nil || len(wantedFile) != sha256.Size {
		return nil, errors.New("invalid camera payload SHA-256")
	}
	wantedMetadata, err := base64.StdEncoding.DecodeString(values["METADATA_HASH"])
	if err != nil || len(wantedMetadata) != sha256.Size {
		return nil, errors.New("invalid camera metadata SHA-256")
	}
	reader, err := payload.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	fileHash, metadataHash := sha256.New(), sha256.New()
	stream := io.TeeReader(reader, fileHash)
	header := make([]byte, 24)
	if _, err := io.ReadFull(stream, header); err != nil {
		return nil, err
	}
	manifestSize := binary.BigEndian.Uint64(header[12:20])
	signatureSize := uint64(binary.BigEndian.Uint32(header[20:24]))
	if string(header[:4]) != "CrAU" || binary.BigEndian.Uint64(header[4:12]) != 2 || manifestSize != metadataSize-24 || signatureSize > fileSize-metadataSize {
		return nil, errors.New("unsupported camera payload header")
	}
	_, _ = metadataHash.Write(header)
	if _, err := io.CopyN(metadataHash, stream, int64(metadataSize)-24); err != nil {
		return nil, err
	}
	remaining, err := io.Copy(io.Discard, io.LimitReader(stream, int64(fileSize-metadataSize)+1))
	if err != nil {
		return nil, err
	}
	if uint64(remaining) != fileSize-metadataSize {
		return nil, errors.New("camera payload length changed")
	}
	if !bytes.Equal(fileHash.Sum(nil), wantedFile) || !bytes.Equal(metadataHash.Sum(nil), wantedMetadata) {
		return nil, errors.New("camera payload or metadata checksum mismatch")
	}
	// Hash the descriptor whose ZIP was parsed. Install callers pass a sealed
	// snapshot, and this also prevents a pathname replacement between reads.
	archiveHash := md5.New()
	if _, err := io.Copy(archiveHash, io.NewSectionReader(file, 0, info.Size())); err != nil {
		return nil, err
	}
	archive := &bulkCameraArchive{Manifest: manifest, Profile: profile, Size: info.Size()}
	copy(archive.MD5[:], archiveHash.Sum(nil))
	return archive, nil
}
