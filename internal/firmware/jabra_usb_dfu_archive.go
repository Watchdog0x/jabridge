package firmware

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type jabraDFUImage struct {
	Manifest *BuildVector
	Payload  []byte // CSR-dfu2 image, excluding the 16-byte USB DFU suffix
	MD5, SHA string // catalog Base64 MD5 and local hexadecimal SHA-256
	Profile  usbDFUProfile
}

func isUSBDFUManifest(manifest *BuildVector) bool {
	if manifest == nil {
		return false
	}
	pids, err := parseTargetPIDs(manifest.TargetUSBPIDs)
	if err != nil || len(pids) != 1 {
		return false
	}
	profile, ok := usbDFUProfileForPID(pids[0])
	return ok && profile.DFUPID == pids[0]
}

func loadJabraDFUImage(path string) (*jabraDFUImage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 8<<20 {
		return nil, errors.New("invalid USB DFU firmware archive size or type")
	}
	data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, errors.New("USB DFU firmware archive changed while reading")
	}
	return parseJabraDFUImage(data)
}

func parseJabraDFUImage(data []byte) (*jabraDFUImage, error) {
	if bytes.HasPrefix(data, []byte("CSR-dfu2")) {
		return finishJabraDFUImage(data, nil, data)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	if len(archive.File) != 2 {
		return nil, errors.New("USB DFU archive must contain exactly info.xml and one DFU image")
	}
	files := map[string][]byte{}
	for _, file := range archive.File {
		limit := uint64(8 << 20)
		if file.Name == "info.xml" {
			limit = 1 << 20
		}
		if !file.Mode().IsRegular() || file.Name != filepath.Base(file.Name) || strings.ContainsAny(file.Name, "\\\x00") ||
			file.UncompressedSize64 == 0 || file.UncompressedSize64 > limit || files[file.Name] != nil {
			return nil, errors.New("invalid or duplicate USB DFU archive entry")
		}
		r, err := file.Open()
		if err != nil {
			return nil, err
		}
		content, readErr := io.ReadAll(io.LimitReader(r, int64(limit)+1))
		closeErr := r.Close()
		if readErr != nil || closeErr != nil || uint64(len(content)) != file.UncompressedSize64 {
			return nil, errors.New("USB DFU archive entry failed size or ZIP checksum validation")
		}
		files[file.Name] = content
	}
	manifest := &BuildVector{}
	if err := xml.Unmarshal(files["info.xml"], manifest); err != nil {
		return nil, fmt.Errorf("USB DFU manifest: %w", err)
	}
	if !isUSBDFUManifest(manifest) || len(manifest.Files) != 1 {
		return nil, errors.New("not a single-image Jabra USB DFU DFU archive")
	}
	if _, err := parseVersionTriplet(manifest.Version); err != nil {
		return nil, err
	}
	entry := manifest.Files[0]
	if filepath.Ext(entry.Name) != ".dfu" || entry.Content != "firmware" || entry.Target != "headset" ||
		entry.Partition != 0 || entry.Version != manifest.Version {
		return nil, errors.New("unsupported USB DFU manifest payload")
	}
	image, found := files[entry.Name]
	if !found {
		return nil, errors.New("USB DFU manifest payload is missing")
	}
	return finishJabraDFUImage(data, manifest, image)
}

func finishJabraDFUImage(archive []byte, manifest *BuildVector, image []byte) (*jabraDFUImage, error) {
	if len(image) < 48 {
		return nil, errors.New("short USB DFU image")
	}
	profile, ok := usbDFUProfileForPID(binary.LittleEndian.Uint16(image[len(image)-14 : len(image)-12]))
	if !ok {
		return nil, errors.New("USB DFU image has no supported device profile")
	}
	if manifest == nil {
		fields := strings.Fields(string(image[16:32]))
		if len(fields) < 2 {
			return nil, errors.New("USB DFU image has no version")
		}
		manifest = &BuildVector{ProductName: profile.Name, Version: fields[1], TargetUSBPIDs: []string{fmt.Sprintf("0x%04x", profile.DFUPID)}}
	}
	targets, err := parseTargetPIDs(manifest.TargetUSBPIDs)
	if err != nil || len(targets) != 1 || targets[0] != profile.DFUPID {
		return nil, errors.New("DFU suffix and manifest target disagree")
	}
	if _, err := parseVersionTriplet(manifest.Version); err != nil {
		return nil, err
	}
	payload, err := parseJabraDFUFile(image, manifest.Version, profile.DFUPID)
	if err != nil {
		return nil, err
	}
	digest, strong := md5.Sum(archive), sha256.Sum256(archive)
	return &jabraDFUImage{Manifest: manifest, Payload: payload,
		MD5: base64.StdEncoding.EncodeToString(digest[:]), SHA: hex.EncodeToString(strong[:]), Profile: profile}, nil
}

func parseJabraDFUFile(data []byte, version string, dfuPID uint16) ([]byte, error) {
	if len(data) < 48 || len(data) > 8<<20 {
		return nil, errors.New("invalid USB DFU image size")
	}
	suffix := data[len(data)-16:]
	if string(suffix[8:11]) != "UFD" || suffix[11] != 16 ||
		binary.LittleEndian.Uint16(suffix[2:4]) != dfuPID ||
		binary.LittleEndian.Uint16(suffix[4:6]) != JabraVendorID ||
		(binary.LittleEndian.Uint16(suffix[6:8]) != 0x0100 && binary.LittleEndian.Uint16(suffix[6:8]) != 0x0110) {
		return nil, errors.New("DFU suffix does not identify the Jabra USB DFU bootloader")
	}
	// DFU 1.1 appendix A uses the uncomplemented running IEEE CRC32.
	if ^crc32.ChecksumIEEE(data[:len(data)-4]) != binary.LittleEndian.Uint32(suffix[12:]) {
		return nil, errors.New("USB DFU suffix CRC mismatch")
	}
	if string(data[:8]) != "CSR-dfu2" || binary.LittleEndian.Uint16(data[8:10]) != 3 ||
		int(binary.LittleEndian.Uint32(data[10:14])) != len(data)-16 {
		return nil, errors.New("invalid CSR-dfu2 header or declared size")
	}
	header := int(binary.LittleEndian.Uint16(data[14:16]))
	if header < 32 || header > 4096 || header >= len(data)-16 {
		return nil, errors.New("invalid CSR-dfu2 header length")
	}
	fields := strings.Fields(string(data[16:32]))
	if len(fields) < 2 || fields[1] != version {
		return nil, errors.New("USB DFU image version disagrees with its manifest")
	}
	return data[:len(data)-16], nil
}
