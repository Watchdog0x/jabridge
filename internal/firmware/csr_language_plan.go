package firmware

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// A language transfer is distinct from reinstalling the main firmware. Both
// files can use partition zero, and the tunes pack has its own version.
type csrLanguagePlan struct {
	Firmware    string
	Language    uint16
	TuneVersion [3]byte
	Partition   byte
	Preload     uint16
	Image       []byte
}

func csrLanguageName(language uint16) string {
	switch language {
	case 0x0409:
		return "English (US)"
	case 0x0407:
		return "German"
	case 0x040c:
		return "French"
	case 0x0411:
		return "Japanese"
	case 0x0412:
		return "Korean"
	case 0x0804:
		return "Chinese (Simplified)"
	}
	return fmt.Sprintf("Unknown (%d)", language)
}

func VoiceLanguageName(language uint16) string { return csrLanguageName(language) }

func parseCSRLanguage(value string) (uint16, error) {
	if strings.EqualFold(strings.TrimSpace(value), "English") {
		return 0x409, nil
	}
	if strings.EqualFold(strings.TrimSpace(value), "Chinese") {
		return 0x804, nil
	}
	for _, language := range []uint16{0x409, 0x407, 0x40c, 0x411, 0x412, 0x804} {
		if strings.EqualFold(strings.TrimSpace(value), csrLanguageName(language)) {
			return language, nil
		}
	}
	number, err := strconv.ParseUint(strings.TrimSpace(value), 0, 16)
	if err != nil || number == 0 {
		return 0, errors.New("choose a voice language name or a language ID such as 0x0407")
	}
	if strings.HasPrefix(csrLanguageName(uint16(number)), "Unknown") {
		return 0, errors.New("this voice language is not supported")
	}
	return uint16(number), nil
}

func prepareCSRLanguagePlan(manifest *BuildVector, contents map[string][]byte, protocol int, language uint16) (csrLanguagePlan, error) {
	if manifest == nil || protocol != 17 || language == 0 || manifest.MaxPreloadCount < 1 || manifest.MaxPreloadCount > 10 || len(manifest.Files) == 0 {
		return csrLanguagePlan{}, errors.New("incomplete split-image voice language metadata")
	}
	if _, err := parseVersionTriplet(manifest.Version); err != nil {
		return csrLanguagePlan{}, err
	}
	targets, err := parseTargetPIDs(manifest.TargetUSBPIDs)
	if err != nil || len(targets) != 1 || targets[0] != 0x2549 {
		return csrLanguagePlan{}, errors.New("this language package is not for the supported Evolve3 65 USB model")
	}
	if strings.HasPrefix(csrLanguageName(language), "Unknown") {
		return csrLanguagePlan{}, errors.New("requested voice language is not supported")
	}
	names := map[string]bool{}
	languages := map[uint16]bool{}
	firmwareFiles := 0
	var selected *GnVFile
	var total int64
	for _, file := range manifest.Files {
		if file.Name == "" || filepath.Base(file.Name) != file.Name || !strings.EqualFold(filepath.Ext(file.Name), ".bin") || names[file.Name] || file.Target != "headset" || file.Partition != 0 || file.SitelHidTargetID != "" {
			return csrLanguagePlan{}, errors.New("invalid split-image voice package target or filename")
		}
		names[file.Name] = true
		data := contents[file.Name]
		total += int64(len(data))
		if len(data) == 0 || total > MaxExpandedArchiveSize {
			return csrLanguagePlan{}, errors.New("missing or oversized split-image data")
		}
		if _, err := parseVersionTriplet(file.Version); err != nil {
			return csrLanguagePlan{}, fmt.Errorf("invalid version for %q: %w", file.Name, err)
		}
		if file.CRC != "" {
			crc, err := parseHexCRC(file.CRC)
			if err != nil || csrImageCRC(data) != crc {
				return csrLanguagePlan{}, errors.New("split-image CRC mismatch")
			}
		}
		switch strings.ToLower(strings.TrimSpace(file.Content)) {
		case "firmware":
			firmwareFiles++
			if file.Version != manifest.Version || file.Language.ID != "" {
				return csrLanguagePlan{}, errors.New("main firmware metadata does not match the package")
			}
		case "tunepack":
			value, err := strconv.ParseUint(file.Language.ID, 0, 16)
			if err != nil || value == 0 || languages[uint16(value)] {
				return csrLanguagePlan{}, errors.New("missing or duplicate voice language image")
			}
			languages[uint16(value)] = true
			if uint16(value) == language {
				chosen := file
				selected = &chosen
			}
		default:
			return csrLanguagePlan{}, errors.New("unknown content in split-image voice package")
		}
	}
	if firmwareFiles != 1 || selected == nil {
		return csrLanguagePlan{}, errors.New("package needs one main firmware image and the exact requested voice language")
	}
	version, err := parseVersionTriplet(selected.Version)
	if err != nil {
		return csrLanguagePlan{}, err
	}
	return csrLanguagePlan{Firmware: manifest.Version, Language: language, TuneVersion: version, Partition: byte(selected.Partition), Preload: uint16(manifest.MaxPreloadCount), Image: append([]byte(nil), contents[selected.Name]...)}, nil
}
