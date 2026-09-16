package firmware

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

var engage75RuntimePIDs = []uint16{0x1110, 0x1112, 0x1113, 0x111d, 0x111e, 0x111f, 0x1120, 0x1140, 0x1141, 0x1142, 0x114a, 0x114b, 0x114c, 0x114d}
var engage75Profile = sitelDECTProfile{Name: "Jabra Engage 75 / 75 SE", RuntimePIDs: engage75RuntimePIDs, BootPID: 0x1111, HeadsetImagePID: 0x1116, MMIImagePID: 0x1117, Targets: []byte{1, 28, 23, 24, 22, 21, 5, 4, 27}}

var engage75TargetOrder = []byte{1, 28, 23, 24, 22, 21, 5, 4, 27}

type engage75Archive struct {
	Manifest *BuildVector
	HEX      []sitelPlannedImage
	Radio    map[string]*sitelBluecoreImage
	Settings map[string][]sitelPSRRecord
}

func isEngage75Manifest(manifest *BuildVector) bool {
	if manifest == nil {
		return false
	}
	pids, err := parseTargetPIDs(manifest.TargetUSBPIDs)
	return err == nil && len(pids) == 1 && pids[0] == 0x1111
}

// Keep the package's different component versions and both radio variants.
// The actual chip is selected later from its raw identity, never from a name.
func loadEngage75Archive(path string) (*engage75Archive, error) {
	manifest, files, err := parseGnVArchive(path)
	if err != nil {
		return nil, err
	}
	if !isEngage75Manifest(manifest) || len(manifest.Files) != 9 {
		return nil, errors.New("not a complete Engage 75 firmware package")
	}
	if _, err := parseVersionTriplet(manifest.Version); err != nil {
		return nil, err
	}
	ordered := append([]GnVFile(nil), manifest.Files...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].UpdateOrder < ordered[j].UpdateOrder })
	hexManifest := *manifest
	hexManifest.Files = nil
	var xpvName, psrName string
	for i, file := range ordered {
		target := engage75TargetOrder[i]
		if file.SitelHidTargetID != strconv.Itoa(int(target)) || file.UpdateOrder < 0 || i > 0 && file.UpdateOrder == ordered[i-1].UpdateOrder {
			return nil, errors.New("wrong Engage 75 update target order")
		}
		if _, err := parseVersionTriplet(file.Version); err != nil {
			return nil, err
		}
		role, address, content, subtarget, region, extension := "base", "1", "firmware", "dect", "", ".hex"
		switch target {
		case 1:
			role, address = "headset", "10"
		case 28:
			role, address, content, subtarget, region = "headset", "10", "tunepack", "", "1"
		case 23:
			address, subtarget, extension = "2", "bluecore", ".xpv"
			xpvName = file.Name
		case 24:
			address, subtarget, extension, content = "2", "bluecore", ".psr", "ps_keys"
			psrName = file.Name
		case 22:
			address, subtarget = "3", "mmi"
		case 5:
			content, subtarget, region = "langpack", "", "7"
		case 4:
			content, subtarget = "graphics", ""
		case 27:
			content, subtarget, region = "tunepack", "", "1"
		}
		if file.Target != role || file.GNPAddress != address || file.Content != content || file.Subtarget != subtarget || file.RegionID != region || strings.ToLower(filepath.Ext(file.Name)) != extension {
			return nil, fmt.Errorf("invalid Engage 75 target %d metadata", target)
		}
		if role == "base" && (content == "firmware" || content == "ps_keys") && file.Version != manifest.Version {
			return nil, errors.New("engage 75 processor version does not match its package")
		}
		if extension == ".hex" {
			hexManifest.Files = append(hexManifest.Files, file)
		}
	}
	images, err := planSitelImages(&hexManifest, files)
	if err != nil {
		return nil, err
	}
	xdvName := strings.TrimSuffix(xpvName, filepath.Ext(xpvName)) + ".xdv"
	xpv, xdv, psr := files[xpvName], files[xdvName], files[psrName]
	settings, err := parseSitelPSR(psr)
	if err != nil {
		return nil, err
	}
	result := &engage75Archive{Manifest: manifest, HEX: images, Radio: make(map[string]*sitelBluecoreImage), Settings: make(map[string][]sitelPSRRecord)}
	for _, chip := range []string{"gordon", "rick"} {
		for _, data := range [][]byte{xpv, xdv, psr} {
			if err := validateEngage75RadioMetadata(data, chip, manifest.Version); err != nil {
				return nil, err
			}
		}
		image, err := parseSitelBluecoreImages(xpv, xdv, chip)
		if err != nil {
			return nil, err
		}
		for sector := range image.Sectors {
			if sector >= xapInternalSectors {
				return nil, errors.New("engage 75 radio image reaches reserved flash")
			}
		}
		result.Radio[chip] = image
		for _, setting := range settings {
			if setting.Chip == chip || setting.Chip == "all" {
				result.Settings[chip] = append(result.Settings[chip], setting)
			}
		}
		if len(result.Settings[chip]) == 0 {
			return nil, errors.New("engage 75 radio settings are missing for a chip variant")
		}
	}
	return result, nil
}

func validateEngage75RadioMetadata(data []byte, chip, wanted string) error {
	active := true
	foundID, foundVersion := false, false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 4096)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		section, marker, err := sitelBluecoreChipMarker(line)
		if err != nil {
			return err
		}
		if marker {
			active = section == chip || section == "all"
			continue
		}
		if !active || !strings.HasPrefix(line, "// GN ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "// GN "))
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "FWU_ID":
			if len(fields) != 2 {
				return errors.New("invalid Engage 75 radio firmware ID")
			}
			id, err := strconv.ParseUint(fields[1], 0, 32)
			if err != nil || id != 0x0b0e1111 {
				return errors.New("engage 75 radio firmware ID mismatch")
			}
			foundID = true
		case "VERSION":
			if len(fields) != 2 || fields[1] != wanted {
				return errors.New("engage 75 radio firmware version mismatch")
			}
			foundVersion = true
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !foundID || !foundVersion {
		return errors.New("missing Engage 75 chip-specific firmware identity")
	}
	return nil
}

// The full family path is required; a seven-HEX-only plan would silently omit
// the radio and its settings. Keep that invalid even for internal callers.
func (a *sitelDECTArchive) validateEngage75() error {
	if a.Engage75 == nil || a.Manifest != a.Engage75.Manifest || !isEngage75Manifest(a.Manifest) || !reflect.DeepEqual(a.Profile, engage75Profile) || len(a.Images) != 7 || !reflect.DeepEqual(a.Images, a.Engage75.HEX) {
		return errors.New("incomplete Engage 75 component plan")
	}
	wantedTargets := []byte{1, 28, 22, 21, 5, 4, 27}
	for i, image := range a.Images {
		if image.File.SitelHidTargetID != strconv.Itoa(int(wantedTargets[i])) || len(image.Segments) == 0 {
			return errors.New("wrong Engage 75 HEX component order")
		}
		address := "1"
		if wantedTargets[i] == 1 || wantedTargets[i] == 28 {
			address = "10"
		}
		if wantedTargets[i] == 22 {
			address = "3"
		}
		if image.File.GNPAddress != address {
			return errors.New("wrong Engage 75 HEX component address")
		}
	}
	for _, chip := range []string{"gordon", "rick"} {
		image := a.Engage75.Radio[chip]
		if image == nil || image.Chip != chip || len(image.Sectors) == 0 || len(a.Engage75.Settings[chip]) == 0 {
			return errors.New("missing Engage 75 radio variant")
		}
		if err := validateSitelPSR(chip, a.Engage75.Settings[chip]); err != nil {
			return err
		}
		for sector, words := range image.Sectors {
			if sector >= xapInternalSectors || len(words) != xapInternalWords {
				return errors.New("invalid Engage 75 radio sector")
			}
		}
	}
	return nil
}
