package firmware

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// An observation belongs to this already-open FWU connection. Image IDs and
// numeric modes are measured; ExpectedID comes separately from the profile.
// Nothing here authorizes entry, a program switch, target selection or erase.
type sitelDECTImageObservation struct {
	Address    byte
	ExpectedID uint32
	Info       sitelDeviceInfo
	peer       sitelRequest
}

// collectEngage75ImageInfo is an op0-only barrier, not preparation. In
// particular it does not switch a Main child before identifying the display.
func collectEngage75ImageInfo(ctx context.Context, connection *sitelDECTConnection) ([]sitelDECTImageObservation, error) {
	if connection == nil || connection.Root == nil {
		return nil, errors.New("missing Engage 75 information connection")
	}
	validate := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if connection.Validate != nil {
			return connection.Validate()
		}
		return nil
	}
	var observations []sitelDECTImageObservation
	for _, component := range []struct {
		address byte
		image   uint16
		role    string
	}{{1, engage75Profile.BootPID, "base"}, {10, engage75Profile.HeadsetImagePID, "headset"}, {3, engage75Profile.MMIImagePID, "display"}} {
		if err := validate(); err != nil {
			return nil, err
		}
		peer := connection.peer(component.address)
		query, cancel := context.WithTimeout(ctx, 15*time.Second)
		data, err := peer.request(query, 0, nil)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("engage 75 %s image information at address %d: %w", component.role, component.address, err)
		}
		if err := validate(); err != nil {
			return nil, err
		}
		info, err := decodeSitelInfo(data)
		if err != nil {
			return nil, fmt.Errorf("engage 75 %s image information at address %d: %w", component.role, component.address, err)
		}
		expected := uint32(JabraVendorID)<<16 | uint32(component.image)
		if info.ID != expected {
			return nil, fmt.Errorf("engage 75 %s image identity at address %d does not match its archive", component.role, component.address)
		}
		observations = append(observations, sitelDECTImageObservation{Address: component.address, ExpectedID: expected, Info: info, peer: peer})
	}
	if err := validate(); err != nil {
		return nil, err
	}
	return observations, nil
}
