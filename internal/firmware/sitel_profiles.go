package firmware

import (
	"fmt"
	"strconv"
)

// A protocol number identifies the wire format, not the model or its images.
// Runtime PIDs come from Jabra's product catalog; boot PIDs and image order
// come from the corresponding official archives. Live identity and official
// release checks still run before installation. See docs/FIRMWARE.md.
type sitelProfile struct {
	Name         string
	RuntimePIDs  []uint16
	BootPID      uint16
	ImageTargets []byte
}

var sitelProfiles = []sitelProfile{
	{Name: "Jabra Engage 50 II", RuntimePIDs: []uint16{0x4051, 0x4052, 0x4053, 0x4054, 0x4055, 0x4056}, BootPID: 0x4050, ImageTargets: []byte{3, 29, 27}},
	{Name: "Jabra Evolve2 40", RuntimePIDs: []uint16{0x0e40, 0x0e41, 0x0e42, 0x0e43}, BootPID: 0x0e44, ImageTargets: []byte{3, 27}},
	{Name: "Jabra Evolve2 40 SE", RuntimePIDs: []uint16{0x2e40, 0x2e41, 0x2e42, 0x2e43}, BootPID: 0x2e44, ImageTargets: []byte{3, 27}},
	{Name: "Jabra Evolve2 30 / Connect 4h", RuntimePIDs: []uint16{0x0e30, 0x0e31, 0x0e32, 0x0e33, 0x0e35}, BootPID: 0x0e34, ImageTargets: []byte{3, 27}},
	{Name: "Jabra Evolve2 30 SE", RuntimePIDs: []uint16{0x0e36, 0x0e37, 0x0e38, 0x0e39}, BootPID: 0x0e3a, ImageTargets: []byte{3, 27}},
}

func (p sitelProfile) runtime(pid uint16) bool { return containsPID(p.RuntimePIDs, pid) }

func sitelProfileForPID(pid uint16) (sitelProfile, bool) {
	for _, profile := range sitelProfiles {
		if profile.runtime(pid) || profile.BootPID == pid {
			return profile, true
		}
	}
	return sitelProfile{}, false
}

func sitelProfileForManifest(manifest *BuildVector) (sitelProfile, error) {
	if manifest == nil {
		return sitelProfile{}, fmt.Errorf("missing Sitel firmware manifest")
	}
	pids, err := parseTargetPIDs(manifest.TargetUSBPIDs)
	if err != nil || len(pids) != 1 {
		return sitelProfile{}, fmt.Errorf("sitel firmware needs one bootloader target")
	}
	profile, ok := sitelProfileForPID(pids[0])
	if !ok || profile.BootPID != pids[0] {
		return sitelProfile{}, fmt.Errorf("firmware installation is not implemented for %s (bootloader 0b0e:%04x)", manifest.ProductName, pids[0])
	}
	return profile, nil
}

func (p sitelProfile) validateImages(images []sitelPlannedImage, wanted string) error {
	if _, err := parseVersionTriplet(wanted); err != nil {
		return err
	}
	if len(p.ImageTargets) == 0 || len(images) != len(p.ImageTargets) {
		return fmt.Errorf("%s firmware needs %d images; got %d", p.Name, len(p.ImageTargets), len(images))
	}
	for i, image := range images {
		target, err := strconv.ParseUint(image.File.SitelHidTargetID, 10, 8)
		if err != nil || byte(target) != p.ImageTargets[i] || image.File.GNPAddress != "1" || image.File.Version != wanted || len(image.Segments) == 0 || image.File.UpdateOrder < 0 || (i > 0 && image.File.UpdateOrder <= images[i-1].File.UpdateOrder) {
			return fmt.Errorf("unsupported image order or metadata for %s", p.Name)
		}
	}
	return nil
}

func loadSitelImages(path string) (*BuildVector, []sitelPlannedImage, error) {
	manifest, files, err := parseGnVArchive(path)
	if err != nil {
		return nil, nil, err
	}
	profile, err := sitelProfileForManifest(manifest)
	if err != nil {
		return nil, nil, err
	}
	images, err := planSitelImages(manifest, files)
	if err != nil {
		return nil, nil, err
	}
	if err := profile.validateImages(images, manifest.Version); err != nil {
		return nil, nil, err
	}
	return manifest, images, nil
}
