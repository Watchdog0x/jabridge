package firmware

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"path"
)

const uvcImageLimit = 64 << 20

var uvcCameraRuntimePIDs = []uint16{0x3020, 0x3021, 0x3029, 0x302a}

func uvcCameraPID(pid uint16) bool { return containsPID(uvcCameraRuntimePIDs, pid) }
func isUVCCameraManifest(manifest *BuildVector) bool {
	if manifest == nil {
		return false
	}
	pids, err := parseTargetPIDs(manifest.TargetUSBPIDs)
	return err == nil && len(pids) == 1 && pids[0] == 0x3020
}

type uvcCameraImage struct {
	File GnVFile
	Data []byte
	CRC  uint32
	Boot bool
}
type uvcCameraArchive struct {
	Manifest   *BuildVector
	Boot, Main uvcCameraImage
}

func loadUVCCameraArchive(filename string) (*uvcCameraArchive, error) {
	manifest, contents, err := parseGnVArchive(filename)
	if err != nil {
		return nil, err
	}
	if !isUVCCameraManifest(manifest) || len(manifest.Files) != 2 || len(contents) != 3 {
		return nil, errors.New("not a complete PanaCast 20 archive")
	}
	if _, err := parseVersionTriplet(manifest.Version); err != nil {
		return nil, err
	}
	archive := &uvcCameraArchive{Manifest: manifest}
	for _, file := range manifest.Files {
		if file.Target != "base" || path.Ext(file.Name) != ".mvcmd" {
			return nil, errors.New("invalid PanaCast 20 component")
		}
		if _, err := parseVersionTriplet(file.Version); err != nil {
			return nil, err
		}
		image, err := parseUVCCameraImage(file, contents[file.Name])
		if err != nil {
			return nil, err
		}
		switch file.Content {
		case "bootloader":
			if archive.Boot.Data != nil {
				return nil, errors.New("duplicate camera boot image")
			}
			image.Boot = true
			archive.Boot = image
		case "firmware":
			if archive.Main.Data != nil || file.Version != manifest.Version {
				return nil, errors.New("invalid camera main image version")
			}
			archive.Main = image
		default:
			return nil, errors.New("unknown PanaCast 20 image type")
		}
	}
	if archive.Boot.Data == nil || archive.Main.Data == nil {
		return nil, errors.New("PanaCast 20 archive is missing a component")
	}
	return archive, nil
}
func parseUVCCameraImage(file GnVFile, data []byte) (uvcCameraImage, error) {
	image := uvcCameraImage{File: file, Data: data}
	if len(data) < 4 || len(data) > uvcImageLimit {
		return image, errors.New("invalid PanaCast 20 image size")
	}
	body := data
	if !bytes.HasPrefix(body, []byte("MA2x")) {
		if len(body) <= 1028 || !bytes.HasPrefix(body[1024:], []byte("MA2x")) {
			return image, errors.New("PanaCast 20 image has no valid command header")
		}
		// Keep the vendor signature bytes intact. This host does not verify
		// them; it requires Jabra's exact official archive checksum instead.
		body = body[1024:]
	}
	image.CRC = crc32.ChecksumIEEE(body)
	return image, nil
}

func uvcBootRequired(pid uint16, current string) bool {
	return containsPID([]uint16{0x3020, 0x3021, 0x3022, 0x3023}, pid) && (current == "2.7.17" || current == "2.7.18" || current == "2.7.19")
}
func uvcUpdateHeader(image uvcCameraImage, page int) ([]byte, error) {
	if len(image.Data) < 4 || len(image.Data) > uvcImageLimit || (page != 256 && page != 1024) {
		return nil, errors.New("invalid camera image or page size")
	}
	header := make([]byte, 256)
	base := uint32(0x04000000)
	name := "main"
	if image.Boot {
		base = 0
		name = "boot"
	}
	binary.LittleEndian.PutUint32(header, base)
	binary.LittleEndian.PutUint32(header[4:], uint32(len(image.Data)))
	binary.LittleEndian.PutUint32(header[8:], uvcImageLimit)
	binary.LittleEndian.PutUint32(header[12:], image.CRC)
	binary.LittleEndian.PutUint16(header[16:], uint16(page))
	copy(header[18:], "app")
	copy(header[24:], name)
	return header, nil
}

// Unit-4 vendor commands use a seven-byte generic header and one of three
// fixed-size extension controls, not USB bulk or a raw HID report.
func uvcVendorPacket(op byte, address uint32, data []byte, response int) (byte, []byte, error) {
	if len(data) > 1024 || len(data) > 256 && len(data) != 1024 || response < 0 || response > 256 {
		return 0, nil, errors.New("invalid UVC vendor command size")
	}
	selector, size := byte(9), 7
	if len(data) > 0 {
		selector, size = 11, 263
	}
	if len(data) == 1024 {
		selector, size = 23, 1031
	}
	packet := make([]byte, size)
	packet[0] = op
	binary.LittleEndian.PutUint16(packet[1:], uint16(address>>16))
	binary.LittleEndian.PutUint16(packet[3:], uint16(address))
	length := len(data)
	if length == 0 {
		length = response
	}
	binary.LittleEndian.PutUint16(packet[5:], uint16(length))
	copy(packet[7:], data)
	switch op {
	case 0xc0:
		if address != 0 || len(data) != 0 || response != 3 {
			return 0, nil, errors.New("invalid camera version request")
		}
	case 0xc1:
		if address != 0 || len(data) != 256 || response != 1 {
			return 0, nil, errors.New("invalid camera image-header request")
		}
	case 0xc2:
		if (len(data) != 256 && len(data) != 1024) || response != 0 || uint64(address)+uint64(len(data)) > 2*uvcImageLimit {
			return 0, nil, errors.New("invalid camera page request")
		}
		if address%uint32(len(data)) != 0 {
			return 0, nil, errors.New("unaligned camera page request")
		}
	default:
		return 0, nil, fmt.Errorf("unknown camera command %02x", op)
	}
	return selector, packet, nil
}
