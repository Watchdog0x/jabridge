package firmware

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

type sitelRequest interface {
	request(context.Context, byte, []byte) ([]byte, error)
}

func sitelAreaKind(target byte) (byte, error) {
	switch target {
	case 3:
		return 0, nil
	case 29:
		return 4, nil
	case 27:
		return 3, nil
	default:
		return 0, errors.New("unknown Sitel target")
	}
}

// Probe every area and validate every image before permitting the first erase.
// All geometry comes from the bootloader, never from runtime MCU addresses.
func prepareSitelTransfer(ctx context.Context, peer sitelRequest, images []sitelPlannedImage, wanted string) (sitelDeviceInfo, []sitelPreparedImage, error) {
	body, err := peer.request(ctx, 0, nil)
	if err != nil {
		return sitelDeviceInfo{}, nil, err
	}
	info, err := decodeSitelInfo(body)
	if err != nil {
		return info, nil, err
	}
	if info.Mode == 1 {
		return info, nil, errors.New("sitel endpoint is still in application mode")
	}
	profile, ok := sitelProfileForPID(uint16(info.ID))
	if !ok || info.ID != uint32(JabraVendorID)<<16|uint32(profile.BootPID) {
		return info, nil, errors.New("unsupported Sitel bootloader image identity")
	}
	if err := profile.validateImages(images, wanted); err != nil {
		return info, nil, err
	}
	var prepared []sitelPreparedImage
	seen := map[byte]bool{}
	for _, image := range images {
		// Manifest target IDs are decimal; 29 is secondary-controller target
		// 0x1d, not a GNP address and not hexadecimal target 0x29.
		n, err := strconv.ParseUint(image.File.SitelHidTargetID, 10, 8)
		if err != nil || seen[byte(n)] || image.File.GNPAddress != "1" {
			return info, nil, errors.New("invalid or duplicate Sitel target")
		}
		target := byte(n)
		seen[target] = true
		kind, err := sitelAreaKind(target)
		if err != nil {
			return info, nil, err
		}
		body, err := peer.request(ctx, 1, []byte{3, kind})
		if err != nil {
			return info, nil, err
		}
		area, err := decodeSitelArea(body)
		if err != nil {
			return info, nil, err
		}
		value, err := prepareSitelImage(target, image.Segments, area, info, wanted)
		if err != nil {
			return info, nil, fmt.Errorf("target %d: %w", target, err)
		}
		for _, prior := range prepared {
			if uint64(area.Address) < uint64(prior.Area.Address)+uint64(prior.Area.Size) && uint64(prior.Area.Address) < uint64(area.Address)+uint64(area.Size) {
				return info, nil, errors.New("overlapping Sitel target areas")
			}
		}
		prepared = append(prepared, value)
	}
	return info, prepared, nil
}

func sitelCRCMatches(ctx context.Context, peer sitelRequest, segment hexImageSegment) (bool, error) {
	payload := make([]byte, 8)
	binary.LittleEndian.PutUint32(payload, segment.Address)
	binary.LittleEndian.PutUint32(payload[4:], uint32(len(segment.Data)))
	body, err := peer.request(ctx, 4, payload)
	if err != nil {
		return false, err
	}
	if len(body) != 2 {
		return false, errors.New("invalid Sitel CRC reply")
	}
	got := binary.LittleEndian.Uint16(body)
	return got == sitelCRC(segment.Data, true) || got == sitelCRC(segment.Data, false), nil
}

func sitelStatus(body []byte, err error) error {
	if err != nil {
		return err
	}
	if len(body) != 1 || body[0] != 0 {
		return fmt.Errorf("sitel flash request failed: status %x", body)
	}
	return nil
}

func sitelSectorPieces(image sitelPreparedImage, sectorSize uint32) (map[uint32][]hexImageSegment, []uint32, error) {
	if sectorSize == 0 || sectorSize&(sectorSize-1) != 0 {
		return nil, nil, errors.New("invalid Sitel sector size")
	}
	sectors := map[uint32][]hexImageSegment{}
	for _, segment := range image.Segments {
		for offset := 0; offset < len(segment.Data); {
			address := uint64(segment.Address) + uint64(offset)
			sector := address / uint64(sectorSize) * uint64(sectorSize)
			if sector < uint64(image.Area.Address) || sector+uint64(sectorSize) > uint64(image.Area.Address)+uint64(image.Area.Size) {
				return nil, nil, errors.New("sitel erase would extend outside the target area")
			}
			count := min(len(segment.Data)-offset, int(sector+uint64(sectorSize)-address))
			piece := hexImageSegment{Address: uint32(address), Data: segment.Data[offset : offset+count]}
			prior := sectors[uint32(sector)]
			if len(prior) > 0 && uint64(prior[len(prior)-1].Address)+uint64(len(prior[len(prior)-1].Data)) == address {
				prior[len(prior)-1].Data = append(prior[len(prior)-1].Data, piece.Data...)
			} else {
				piece.Data = append([]byte(nil), piece.Data...)
				prior = append(prior, piece)
			}
			sectors[uint32(sector)] = prior
			offset += count
		}
	}
	var addresses []uint32
	for address := range sectors {
		addresses = append(addresses, address)
	}
	sort.Slice(addresses, func(i, j int) bool { return addresses[i] < addresses[j] })
	return sectors, addresses, nil
}

// Re-running after interruption reads CRCs again, skips complete sectors and
// restores all supplied bytes of a changed sector after erasing it once.
func transferSitelImage(ctx context.Context, peer sitelRequest, image sitelPreparedImage, info sitelDeviceInfo, progress func(int, int)) error {
	if info.WriteSize == 0 || info.WriteSize > 1014 {
		return errors.New("invalid Sitel write size")
	}
	sectors, addresses, err := sitelSectorPieces(image, info.SectorSize)
	if err != nil {
		return err
	}
	for index, address := range addresses {
		if err := ctx.Err(); err != nil {
			return err
		}
		pieces := sectors[address]
		changed := false
		for _, piece := range pieces {
			match, err := sitelCRCMatches(ctx, peer, piece)
			if err != nil {
				return err
			}
			changed = changed || !match
		}
		if changed {
			payload := make([]byte, 4)
			binary.LittleEndian.PutUint32(payload, address)
			if err := sitelStatus(peer.request(ctx, 2, payload)); err != nil {
				return fmt.Errorf("erase sector %x: %w", address, err)
			}
			for _, piece := range pieces {
				for offset := 0; offset < len(piece.Data); {
					count := min(int(info.WriteSize), len(piece.Data)-offset)
					payload := make([]byte, 4+count)
					binary.LittleEndian.PutUint32(payload, piece.Address+uint32(offset))
					copy(payload[4:], piece.Data[offset:offset+count])
					if err := sitelStatus(peer.request(ctx, 3, payload)); err != nil {
						return fmt.Errorf("write %x: %w", piece.Address+uint32(offset), err)
					}
					offset += count
				}
				match, err := sitelCRCMatches(ctx, peer, piece)
				if err != nil {
					return err
				}
				if !match {
					return fmt.Errorf("sitel verification failed at %x", piece.Address)
				}
			}
		}
		if progress != nil {
			progress(index+1, len(addresses))
		}
	}
	return nil
}

func verifySitelImage(ctx context.Context, peer sitelRequest, image sitelPreparedImage, sectorSize uint32) error {
	sectors, addresses, err := sitelSectorPieces(image, sectorSize)
	if err != nil {
		return err
	}
	for _, address := range addresses {
		for _, piece := range sectors[address] {
			match, err := sitelCRCMatches(ctx, peer, piece)
			if err != nil {
				return err
			}
			if !match {
				return fmt.Errorf("final Sitel image verification failed at %x", piece.Address)
			}
		}
	}
	return nil
}
