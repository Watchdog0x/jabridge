package firmware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const sitelOTAImagePID uint16 = 0x1116

func WirelessFirmwareParent(pid uint16) bool { return sitelOTAParentPID(pid) }

func sitelOTAChildPID(pid uint16) bool {
	switch pid {
	case 0x1116, 0x111c, 0x1145, 0x1149:
		return true
	}
	return false
}

// These are physical parents, not the headset's logical product ID. Base
// variants and their SE counterparts are kept separate from Link 400.
func sitelOTAParentPID(pid uint16) bool {
	switch pid {
	case 0x1131, 0x1132, 0x1133, 0x1134, 0x1135, 0x1136,
		0x1110, 0x1112, 0x1113, 0x111d, 0x111e, 0x111f, 0x1120,
		0x1118, 0x111a, 0x111b, 0x1121, 0x1122, 0x1123, 0x1124,
		0x1140, 0x1141, 0x1142, 0x114a, 0x114b, 0x114c, 0x114d,
		0x1146, 0x1147, 0x1148, 0x114e, 0x114f, 0x1150, 0x1151:
		return true
	}
	return false
}

func SupportsWirelessFirmware(parentPID, childPID uint16) bool {
	if !sitelOTAParentPID(parentPID) || !sitelOTAChildPID(childPID) {
		return false
	}
	if parentPID >= 0x1131 && parentPID <= 0x1136 {
		return true
	}
	return (parentPID >= 0x1140) == (childPID == 0x1145 || childPID == 0x1149)
}

func sitelOTAParentImagePID(pid uint16) uint16 {
	if !sitelOTAParentPID(pid) {
		return 0
	}
	if pid >= 0x1131 && pid <= 0x1136 {
		return 0x1130
	}
	switch pid {
	case 0x1110, 0x1112, 0x1113, 0x111d, 0x111e, 0x111f, 0x1120, 0x1140, 0x1141, 0x1142, 0x114a, 0x114b, 0x114c, 0x114d:
		return 0x1111
	default:
		return 0x1119
	}
}

// The service can carry this digest across its handoff to the installer without
// exposing a serial number over IPC. The installer computes it from fresh reads.
func WirelessFirmwareIdentity(pid uint16, serial, variant string) string {
	variant = strings.ToLower(strings.ReplaceAll(variant, "-", ""))
	decoded, err := hex.DecodeString(variant)
	if !sitelOTAChildPID(pid) || serial == "" || err != nil || len(decoded) < 2 || len(decoded) > 8 {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%04x\n%s\n%s", pid, serial, variant))))
}

func isSitelOTAManifest(manifest *BuildVector) bool {
	if manifest == nil {
		return false
	}
	pids, err := parseTargetPIDs(manifest.TargetUSBPIDs)
	return err == nil && len(pids) == 1 && pids[0] == sitelOTAImagePID
}

type sitelOTAArchive struct {
	Manifest *BuildVector
	Images   []sitelPlannedImage
	Region   byte
}

func validateSitelOTAImages(images []sitelPlannedImage, wanted string) (byte, error) {
	if _, err := parseVersionTriplet(wanted); err != nil {
		return 0, err
	}
	if len(images) != 2 {
		return 0, errors.New("wireless Engage firmware needs the headset and sound-prompt images")
	}
	for i, image := range images {
		file := image.File
		if file.Target != "headset" || file.GNPAddress != "4" || len(image.Segments) == 0 || file.UpdateOrder < 0 || i > 0 && file.UpdateOrder <= images[i-1].File.UpdateOrder {
			return 0, errors.New("invalid wireless Engage image route or order")
		}
		if _, err := parseVersionTriplet(file.Version); err != nil {
			return 0, err
		}
	}
	if images[0].File.SitelHidTargetID != "1" || images[0].File.Content != "firmware" || images[0].File.Version != wanted || images[1].File.SitelHidTargetID != "28" || images[1].File.Content != "tunepack" {
		return 0, errors.New("invalid wireless Engage image set")
	}
	region, err := strconv.ParseUint(images[1].File.RegionID, 10, 8)
	if err != nil || region == 0 {
		return 0, errors.New("wireless Engage sound-prompt region is missing")
	}
	return byte(region), nil
}

func loadSitelOTAArchive(path string) (*sitelOTAArchive, error) {
	manifest, files, err := parseGnVArchive(path)
	if err != nil {
		return nil, err
	}
	if !isSitelOTAManifest(manifest) {
		return nil, errors.New("not a supported wireless Engage archive")
	}
	images, err := planSitelImages(manifest, files)
	if err != nil {
		return nil, err
	}
	region, err := validateSitelOTAImages(images, manifest.Version)
	if err != nil {
		return nil, err
	}
	return &sitelOTAArchive{Manifest: manifest, Images: images, Region: region}, nil
}

func prepareSitelOTATransfer(ctx context.Context, peer sitelRequest, archive *sitelOTAArchive) (sitelDeviceInfo, []sitelPreparedImage, error) {
	if archive == nil || archive.Manifest == nil || !isSitelOTAManifest(archive.Manifest) {
		return sitelDeviceInfo{}, nil, errors.New("missing wireless Engage archive")
	}
	if _, err := validateSitelOTAImages(archive.Images, archive.Manifest.Version); err != nil {
		return sitelDeviceInfo{}, nil, err
	}
	data, err := peer.request(ctx, 0, nil)
	if err != nil {
		return sitelDeviceInfo{}, nil, err
	}
	info, err := decodeSitelInfo(data)
	if err != nil {
		return info, nil, err
	}
	if info.ID != uint32(JabraVendorID)<<16|uint32(sitelOTAImagePID) || info.Mode != 1 {
		return info, nil, errors.New("wireless Engage endpoint identity or update mode does not match")
	}
	images, err := prepareSitelTargets(ctx, peer, archive.Images, info, 1, "4")
	return info, images, err
}
