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
	Name              string
	RuntimePIDs       []uint16
	BootPID           uint16
	ImageTargets      []byte
	LegacySingleImage bool
}

var sitelProfiles = []sitelProfile{
	{Name: "Jabra Engage 50 II", RuntimePIDs: []uint16{0x4051, 0x4052, 0x4053, 0x4054, 0x4055, 0x4056}, BootPID: 0x4050, ImageTargets: []byte{3, 29, 27}},
	{Name: "Jabra Evolve2 40", RuntimePIDs: []uint16{0x0e40, 0x0e41, 0x0e42, 0x0e43}, BootPID: 0x0e44, ImageTargets: []byte{3, 27}},
	{Name: "Jabra Evolve2 40 SE", RuntimePIDs: []uint16{0x2e40, 0x2e41, 0x2e42, 0x2e43}, BootPID: 0x2e44, ImageTargets: []byte{3, 27}},
	{Name: "Jabra Evolve2 30 / Connect 4h", RuntimePIDs: []uint16{0x0e30, 0x0e31, 0x0e32, 0x0e33, 0x0e35}, BootPID: 0x0e34, ImageTargets: []byte{3, 27}},
	{Name: "Jabra Evolve2 30 SE", RuntimePIDs: []uint16{0x0e36, 0x0e37, 0x0e38, 0x0e39}, BootPID: 0x0e3a, ImageTargets: []byte{3, 27}},
	{Name: "Jabra Engage 50", RuntimePIDs: []uint16{0x4001, 0x4002, 0x4003, 0x4004, 0x4005, 0x4006}, BootPID: 0x4000, ImageTargets: []byte{3, 29, 27}},
	{Name: "Jabra Engage 40", RuntimePIDs: []uint16{0x4061, 0x4062, 0x4063, 0x4064, 0x4065, 0x4066}, BootPID: 0x4060, ImageTargets: []byte{3, 29, 27}},
	{Name: "Jabra Link 400", RuntimePIDs: []uint16{0x1131, 0x1132, 0x1133, 0x1134, 0x1135, 0x1136}, BootPID: 0x1130, ImageTargets: []byte{3, 27}},
	{Name: "Jabra Evolve 20 / 20SE / 30", RuntimePIDs: []uint16{0x0300, 0x0301, 0x0302, 0x0303}, BootPID: 0x0304, ImageTargets: []byte{3}, LegacySingleImage: true},
	{Name: "Jabra Evolve Link", RuntimePIDs: []uint16{0x0305, 0x0306, 0x0307, 0x0308, 0x030a}, BootPID: 0x0309, ImageTargets: []byte{3}, LegacySingleImage: true},
	{Name: "Jabra Evolve 30 II", RuntimePIDs: []uint16{0x0312, 0x0313, 0x0314, 0x0315}, BootPID: 0x0316, ImageTargets: []byte{3}, LegacySingleImage: true},
	{Name: "Jabra Link 43", RuntimePIDs: []uint16{0x0843}, BootPID: 0x0842, ImageTargets: []byte{3}, LegacySingleImage: true},
	{Name: "Jabra Link 860", RuntimePIDs: []uint16{0x0852}, BootPID: 0x0853, ImageTargets: []byte{3}, LegacySingleImage: true},
	{Name: "Jabra Biz 2300", RuntimePIDs: []uint16{0x2301, 0x2302, 0x2303, 0x2304}, BootPID: 0x2300, ImageTargets: []byte{3}, LegacySingleImage: true},
	{Name: "Jabra Link 265", RuntimePIDs: []uint16{0x2311}, BootPID: 0x2310, ImageTargets: []byte{3}, LegacySingleImage: true},
	{Name: "Jabra Link 230", RuntimePIDs: []uint16{0x2315}, BootPID: 0x2314, ImageTargets: []byte{3}, LegacySingleImage: true},
	{Name: "Jabra Link 260", RuntimePIDs: []uint16{0x2319, 0x231a}, BootPID: 0x2318, ImageTargets: []byte{3}, LegacySingleImage: true},
	{Name: "Jabra Biz 2400 II USB CC", RuntimePIDs: []uint16{0x2321, 0x2322, 0x2323, 0x2324}, BootPID: 0x2320, ImageTargets: []byte{3}, LegacySingleImage: true},
	{Name: "Jabra Biz 1500", RuntimePIDs: []uint16{0x2326, 0x2327}, BootPID: 0x2325, ImageTargets: []byte{3}, LegacySingleImage: true},
	{Name: "Jabra Biz 1100", RuntimePIDs: []uint16{0x2329, 0x232a}, BootPID: 0x2328, ImageTargets: []byte{3}, LegacySingleImage: true},
	{Name: "Jabra Link 950", RuntimePIDs: []uint16{0x4040}, BootPID: 0x4041, ImageTargets: []byte{3}, LegacySingleImage: true},
	{Name: "Jabra Evolve2 Deskstand", RuntimePIDs: []uint16{0xc0dd, 0xc0df, 0xc0e0}, BootPID: 0xc0de, ImageTargets: []byte{3}, LegacySingleImage: true},
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
	// Older single-image packages omit target/address/order. The original
	// updater defines precisely one application image (target 3, address 1).
	// Apply that default only to the known legacy profiles and empty fields;
	// supplied metadata and multi-image archives must validate as written.
	if profile.LegacySingleImage && len(manifest.Files) == 1 {
		file := &manifest.Files[0]
		if file.SitelHidTargetID == "" && file.GNPAddress == "" && file.UpdateOrder == 0 && file.Content == "firmware" && file.Target == "headset" {
			file.SitelHidTargetID, file.GNPAddress = "3", "1"
		}
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
