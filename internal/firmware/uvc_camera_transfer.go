package firmware

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

func uvcCameraPageSize(ctx context.Context, transport cameraUVC) (int, error) {
	data, err := transport.Command(ctx, 0xc0, 0, nil, 3)
	if err != nil {
		return 0, err
	}
	if len(data) != 3 || data[0] != 0 {
		return 0, errors.New("camera rejected the firmware interface version query")
	}
	version := binary.LittleEndian.Uint16(data[1:])
	switch version {
	case 1:
		return 256, nil
	case 2:
		return 1024, nil
	default:
		return 0, fmt.Errorf("unsupported PanaCast 20 firmware interface version %d", version)
	}
}

func transferUVCCameraImage(ctx context.Context, transport cameraUVC, image uvcCameraImage, page int, sleep func(context.Context, time.Duration) error, progress func(int, int)) error {
	if transport == nil || sleep == nil {
		return errors.New("missing camera transfer connection")
	}
	header, err := uvcUpdateHeader(image, page)
	if err != nil {
		return err
	}
	response, err := transport.Command(ctx, 0xc1, 0, header, 1)
	if err != nil {
		return err
	}
	// The original Windows updater explicitly checks this status byte. The
	// Linux reference does not; retain the stronger check before page writes.
	if len(response) != 1 || response[0] != 0 {
		return errors.New("camera rejected the firmware image header")
	}
	if err := sleep(ctx, 100*time.Millisecond); err != nil {
		return err
	}
	base := uint32(0x04000000)
	if image.Boot {
		base = 0
	}
	buffer := make([]byte, page)
	for offset := 0; offset < len(image.Data); offset += page {
		if err := ctx.Err(); err != nil {
			return err
		}
		for i := range buffer {
			buffer[i] = 0xff
		}
		end := min(offset+page, len(image.Data))
		copy(buffer, image.Data[offset:end])
		if _, err := transport.Command(ctx, 0xc2, base+uint32(offset), buffer, 0); err != nil {
			return fmt.Errorf("camera image page at %#x: %w", base+uint32(offset), err)
		}
		if progress != nil {
			progress(end, len(image.Data))
		}
	}
	// Vendor updaters allow the camera to finish internal writes before the
	// next image or reset. This is a settling delay, not completion evidence.
	return sleep(ctx, 5*time.Second)
}
