package firmware

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type sitelDECTProfile struct {
	Name                     string
	RuntimePIDs              []uint16
	BootPID, HeadsetImagePID uint16
	USBImagePID              uint16
	MMIImagePID              uint16
	Legacy                   bool
	Targets                  []byte
}

var sitelDECTProfiles = []sitelDECTProfile{
	{Name: "Jabra Engage 45 SE", RuntimePIDs: []uint16{0x1126}, BootPID: 0x1125, HeadsetImagePID: 0x1116, Targets: []byte{1, 28, 21, 27}},
	{Name: "Jabra Engage 65 / 65 SE", RuntimePIDs: []uint16{0x1118, 0x111a, 0x111b, 0x1121, 0x1122, 0x1123, 0x1124, 0x1146, 0x1147, 0x1148, 0x114e, 0x114f, 0x1150, 0x1151}, BootPID: 0x1119, HeadsetImagePID: 0x1116, Targets: []byte{1, 28, 21, 27}},
	{Name: "Jabra Pro 9450", RuntimePIDs: []uint16{0x1021, 0x1022, 0x1023}, BootPID: 0x1020, HeadsetImagePID: 0x1010, Legacy: true, Targets: []byte{1, 3}},
	{Name: "Jabra Pro 920 / 930", RuntimePIDs: []uint16{0x1016, 0x1017, 0x1018}, BootPID: 0x1015, HeadsetImagePID: 0x1011, USBImagePID: 0x1015, Legacy: true, Targets: []byte{1, 7, 6}},
}

func (p sitelDECTProfile) carrier() byte {
	if p.USBImagePID != 0 {
		return 2
	}
	return 1
}
func (p sitelDECTProfile) endpoints() []byte {
	if p.MMIImagePID != 0 {
		return []byte{10, 3, 1}
	}
	if p.USBImagePID != 0 {
		return []byte{10, 1, 2}
	}
	return []byte{10, 1}
}

func (p sitelDECTProfile) runtime(pid uint16) bool { return containsPID(p.RuntimePIDs, pid) }
func sitelDECTProfileForPID(pid uint16) (sitelDECTProfile, bool) {
	if engage75Profile.runtime(pid) || pid == engage75Profile.BootPID {
		return engage75Profile, true
	}
	for _, p := range sitelDECTProfiles {
		if p.runtime(pid) || p.BootPID == pid {
			return p, true
		}
	}
	return sitelDECTProfile{}, false
}
func isSitelDECTManifest(manifest *BuildVector) bool {
	if manifest == nil {
		return false
	}
	pids, err := parseTargetPIDs(manifest.TargetUSBPIDs)
	if err != nil || len(pids) != 1 {
		return false
	}
	profile, ok := sitelDECTProfileForPID(pids[0])
	return ok && profile.BootPID == pids[0]
}

type sitelDECTArchive struct {
	Manifest *BuildVector
	Profile  sitelDECTProfile
	Images   []sitelPlannedImage
	Engage75 *engage75Archive
}

func (a *sitelDECTArchive) validate() error {
	if a != nil && a.Engage75 != nil {
		return a.validateEngage75()
	}
	if a == nil || a.Manifest == nil || !isSitelDECTManifest(a.Manifest) || len(a.Images) != len(a.Profile.Targets) {
		return errors.New("incomplete DECT base firmware image set")
	}
	pids, err := parseTargetPIDs(a.Manifest.TargetUSBPIDs)
	canonical, ok := sitelDECTProfileForPID(a.Profile.BootPID)
	if err != nil || len(pids) != 1 || pids[0] != a.Profile.BootPID || !ok || canonical.HeadsetImagePID != a.Profile.HeadsetImagePID || canonical.USBImagePID != a.Profile.USBImagePID || canonical.Legacy != a.Profile.Legacy || !slices.Equal(canonical.Targets, a.Profile.Targets) || !slices.Equal(canonical.RuntimePIDs, a.Profile.RuntimePIDs) {
		return errors.New("DECT archive profile does not match its model")
	}
	if _, err := parseVersionTriplet(a.Manifest.Version); err != nil {
		return err
	}
	for i, image := range a.Images {
		file := image.File
		target := a.Profile.Targets[i]
		address, role := "1", "base"
		if target == 1 || target == 28 {
			address, role = "10", "headset"
		}
		if target == 7 {
			address = "2"
		}
		if file.SitelHidTargetID != strconv.Itoa(int(target)) || file.GNPAddress != address || file.Target != role || len(image.Segments) == 0 || file.UpdateOrder < 0 || i > 0 && file.UpdateOrder <= a.Images[i-1].File.UpdateOrder {
			return errors.New("DECT firmware image has the wrong component, address or order")
		}
		if _, err := parseVersionTriplet(file.Version); err != nil {
			return err
		}
		if target == 27 || target == 28 {
			region, err := strconv.ParseUint(file.RegionID, 10, 8)
			if err != nil || region == 0 || file.Content != "tunepack" {
				return errors.New("DECT sound-prompt image has invalid region metadata")
			}
		} else {
			if file.Content != "firmware" {
				return errors.New("DECT component is not a firmware image")
			}
			if role == "base" && file.Version != a.Manifest.Version {
				return errors.New("DECT base image version does not match the archive")
			}
		}
	}
	return nil
}

func loadSitelDECTArchive(path string) (*sitelDECTArchive, error) {
	manifest, files, err := parseGnVArchive(path)
	if err != nil {
		return nil, err
	}
	if isEngage75Manifest(manifest) {
		engage, err := loadEngage75Archive(path)
		if err != nil {
			return nil, err
		}
		return &sitelDECTArchive{Manifest: engage.Manifest, Profile: engage75Profile, Images: engage.HEX, Engage75: engage}, nil
	}
	if !isSitelDECTManifest(manifest) {
		return nil, errors.New("not a supported DECT base archive")
	}
	pids, _ := parseTargetPIDs(manifest.TargetUSBPIDs)
	profile, _ := sitelDECTProfileForPID(pids[0])
	if profile.Legacy {
		if len(manifest.Files) != len(profile.Targets) {
			return nil, errors.New("legacy DECT archive is missing a required component")
		}
		for i := range manifest.Files {
			file := &manifest.Files[i]
			if file.SitelHidTargetID != "" || file.GNPAddress != "" || file.UpdateOrder != 0 || file.Content != "firmware" {
				return nil, errors.New("invalid legacy DECT component metadata")
			}
			extension := strings.ToLower(filepath.Ext(file.Name))
			if profile.USBImagePID != 0 {
				switch {
				case file.Target == "headset" && file.Subtarget == "dect" && extension == ".hex":
					file.SitelHidTargetID, file.GNPAddress, file.UpdateOrder = "1", "10", 1
				case file.Target == "base" && file.Subtarget == "usb" && extension == ".bin":
					file.SitelHidTargetID, file.GNPAddress, file.UpdateOrder = "7", "2", 10
				case file.Target == "base" && file.Subtarget == "dect" && extension == ".hex":
					file.SitelHidTargetID, file.GNPAddress, file.UpdateOrder = "6", "1", 20
				default:
					return nil, errors.New("invalid Pro 920/930 component type")
				}
				continue
			}
			if extension != ".hex" {
				return nil, errors.New("legacy DECT component must be Intel HEX")
			}
			switch file.Target {
			case "headset":
				file.SitelHidTargetID, file.GNPAddress, file.UpdateOrder = "1", "10", 1
			case "base":
				file.SitelHidTargetID, file.GNPAddress, file.UpdateOrder = "3", "1", 20
			default:
				return nil, fmt.Errorf("unknown Pro 9450 component %q", file.Target)
			}
		}
	}
	images, err := planSitelImages(manifest, files)
	if err != nil {
		return nil, err
	}
	archive := &sitelDECTArchive{Manifest: manifest, Profile: profile, Images: images}
	if err := archive.validate(); err != nil {
		return nil, err
	}
	return archive, nil
}
