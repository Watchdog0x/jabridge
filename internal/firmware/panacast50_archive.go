package firmware

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

var panacast50PIDs = []uint16{0x3009, 0x3011, 0x3012}

func isPanaCast50Manifest(manifest *BuildVector) bool {
	if manifest == nil {
		return false
	}
	pids, err := parseTargetPIDs(manifest.TargetUSBPIDs)
	return err == nil && len(pids) == 1 && pids[0] == 0x3010
}

type panacast50Archive struct {
	Manifest *BuildVector
	Data     []byte
	MD5      [16]byte
}

func loadPanaCast50Archive(filename string) (*panacast50Archive, error) {
	manifest, contents, err := parseGnVArchive(filename)
	if err != nil {
		return nil, err
	}
	if !isPanaCast50Manifest(manifest) || len(manifest.Files) != 1 || len(contents) != 2 {
		return nil, errors.New("not a complete PanaCast 50 archive")
	}
	if _, err := parseVersionTriplet(manifest.Version); err != nil {
		return nil, err
	}
	file := manifest.Files[0]
	if file.Name != "upgrade.zip" || file.Content != "firmware" || file.Target != "base" || file.Version != manifest.Version {
		return nil, errors.New("invalid PanaCast 50 bundle manifest")
	}
	data := contents["upgrade.zip"]
	if len(data) == 0 {
		return nil, errors.New("PanaCast 50 upgrade.zip is missing")
	}
	if err := validatePanaCast50Bundle(data); err != nil {
		return nil, err
	}
	return &panacast50Archive{Manifest: manifest, Data: data, MD5: md5.Sum(data)}, nil
}

// Inner checksums establish bundle consistency. Official outer-release
// matching is still required before either file-transfer transport is used.
func validatePanaCast50Bundle(data []byte) error {
	if len(data) == 0 || int64(len(data)) > MaxExpandedArchiveSize {
		return errors.New("PanaCast 50 bundle is too large")
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	if len(z.File) > 128 {
		return errors.New("PanaCast 50 bundle has too many files")
	}
	entries := map[string]*zip.File{}
	total := uint64(0)
	const expandedLimit = uint64(512 << 20)
	for _, file := range z.File {
		name := file.Name
		if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || path.Clean(name) != strings.TrimSuffix(name, "/") || strings.HasPrefix(name, "../") {
			return errors.New("invalid PanaCast 50 bundle path")
		}
		if entries[name] != nil {
			return errors.New("duplicate PanaCast 50 bundle file")
		}
		entries[name] = file
		if file.UncompressedSize64 > expandedLimit || total > expandedLimit-file.UncompressedSize64 {
			return errors.New("PanaCast 50 bundle expands beyond its limit")
		}
		total += file.UncompressedSize64
	}
	readSmall := func(name string, limit uint64) ([]byte, error) {
		file := entries[name]
		if file == nil || file.UncompressedSize64 > limit {
			return nil, fmt.Errorf("missing or oversized PanaCast 50 %s", name)
		}
		r, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = r.Close() }()
		return io.ReadAll(io.LimitReader(r, int64(limit)+1))
	}
	sums, err := readSmall("sha256sums", 32<<10)
	if err != nil {
		return err
	}
	sumsDigest, err := readSmall("sha256sums.sha256", sha256.Size)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(sums)
	if !bytes.Equal(digest[:], sumsDigest) {
		return errors.New("PanaCast 50 checksum-list digest did not match")
	}
	signature, err := readSmall("sha256sums.sig", 4096)
	if err != nil || len(signature) < 128 {
		return errors.New("PanaCast 50 bundle signature is missing")
	}
	wanted := map[string][]byte{}
	scanner := bufio.NewScanner(bytes.NewReader(sums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			return errors.New("invalid PanaCast 50 checksum entry")
		}
		name := strings.TrimPrefix(fields[1], "./")
		hash, err := hex.DecodeString(fields[0])
		if err != nil || len(hash) != sha256.Size || wanted[name] != nil || entries[name] == nil {
			return errors.New("invalid or duplicate PanaCast 50 checksum")
		}
		wanted[name] = hash
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	for _, required := range []string{"system.transfer.list", "system.new.dat", "system.patch.dat", "boot.img"} {
		if wanted[required] == nil {
			return fmt.Errorf("PanaCast 50 checksum list is missing %s", required)
		}
	}
	for _, file := range z.File {
		if file.FileInfo().IsDir() {
			continue
		}
		r, err := file.Open()
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(hash, io.LimitReader(r, int64(file.UncompressedSize64)+1))
		closeErr := r.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != int64(file.UncompressedSize64) {
			return errors.New("PanaCast 50 bundle entry size changed")
		}
		if expected := wanted[file.Name]; expected != nil && !bytes.Equal(hash.Sum(nil), expected) {
			return fmt.Errorf("PanaCast 50 bundle checksum failed for %s", file.Name)
		}
	}
	return nil
}
