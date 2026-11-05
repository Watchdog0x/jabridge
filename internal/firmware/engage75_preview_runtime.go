package firmware

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// Re-read the schema's selected fields after restart without forcing the
// previously unavailable IDENT fields into the old all-fields identity hash.
// Newly exposed DFU IDs must match measured boot evidence; an unqualified new
// runtime PID stays unresolved rather than being inferred from an image ID.
func (r *sitelRuntime) verifyEngage75SelectedRuntime(ctx context.Context, device USBDevice, selected engage75SelectedIdentity, archive *sitelDECTArchive, validate func() error) error {
	if selected.validate() != nil || device.ProductID != selected.RuntimePID || device.VendorID != JabraVendorID || device.ViaDongle || device.Serial == "" || engage75Digest(device.Serial) != selected.USBSerialSHA256 || filepath.Base(device.SysPath) != selected.USBPort {
		return errors.New("engage 75 selected USB identity changed")
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return validate()
	}
	image := observeDECT(ctx, r, check, 10, 15, 0, decodeSitelInfo)
	if image.State != dectMeasured || image.Value.ID != 0x0b0e1116 || image.Value.Mode != 1 {
		return fmt.Errorf("engage 75 final headset image/Main mode did not verify: %w", errors.Join(errors.New("missing or contradictory image observation"), image.cause))
	}
	query := func(address, op byte) ([]byte, error) {
		if err := check(); err != nil {
			return nil, err
		}
		data, err := r.query(ctx, address, op)
		if changed := check(); changed != nil {
			return nil, changed
		}
		return data, err
	}
	for _, field := range []struct {
		address, op byte
		expected    uint16
	}{{1, 0x11, selected.RuntimePID}, {1, 0x13, selected.BootPID}} {
		data, err := query(field.address, field.op)
		if err != nil {
			return err
		}
		value, err := decodeDECTProductID(data)
		if err != nil || value != field.expected {
			return errors.New("engage 75 base identity did not verify")
		}
	}
	data, err := query(1, 0x14)
	if err != nil {
		return err
	}
	if err := validateDECTProtocols(data); err != nil {
		return err
	}
	for _, field := range []struct {
		address byte
		variant string
	}{{1, selected.BaseVariant}, {10, selected.HeadVariant}, {3, selected.DisplayVariant}} {
		data, err := query(field.address, 2)
		if err != nil {
			return err
		}
		value, err := decodeDECTVariant(data)
		if err != nil || value != field.variant {
			return errors.New("engage 75 component variant changed")
		}
	}
	data, err = query(10, 1)
	if err != nil {
		return err
	}
	serial, err := decodeDECTSerial(data)
	if err != nil || engage75Digest(serial) != selected.HeadSerialSHA256 {
		return errors.New("engage 75 docked headset changed")
	}
	// A new serial on the display is additional evidence, not a replacement for
	// original selected fields. Parsing it is mandatory when it becomes readable.
	data, err = query(3, 1)
	if err == nil {
		if _, err = decodeDECTSerial(data); err != nil {
			return err
		}
	} else if !errors.Is(err, errSitelRejected) {
		return err
	}
	for _, address := range []byte{10, 3} {
		data, err := query(address, 0x11)
		if err == nil {
			if _, err = decodeDECTProductID(data); err != nil {
				return err
			}
			return fmt.Errorf("engage 75 address %d now exposes a runtime product ID; its mapping needs review", address)
		}
		if !errors.Is(err, errSitelRejected) {
			return err
		}
		data, err = query(address, 0x13)
		if err == nil {
			value, decodeErr := decodeDECTProductID(data)
			expected := uint16(0x1116)
			if address == 3 {
				expected = 0x1117
			}
			if decodeErr != nil || value != expected {
				return errors.New("engage 75 newly readable boot identity contradicts the measured image")
			}
		} else if !errors.Is(err, errSitelRejected) {
			return err
		}
	}
	// Check every firmware/resource version from actual runtime, including radio
	// and both sound prompt packs. Base reappearance alone is never completion.
	versions := engage75Versions{}
	for _, field := range []struct {
		address, op byte
		target      *string
	}{{1, 3, &versions.Base}, {10, 3, &versions.Head}, {3, 3, &versions.Display}, {2, 3, &versions.Radio}, {1, 9, &versions.Graphics}, {1, 6, &versions.Language}, {10, 0x21, &versions.HeadTunes}, {1, 0x21, &versions.BaseTunes}} {
		data, err := query(field.address, field.op)
		if err != nil {
			return err
		}
		*field.target, err = decodeDECTVersionObservation(data)
		if err != nil {
			return err
		}
	}
	identity := sitelDECTIdentity{Base: sitelIdentity{Version: versions.Base}, Headset: sitelIdentity{Version: versions.Head}, MMI: sitelIdentity{Version: versions.Display}, RadioVersion: versions.Radio, Graphics: versions.Graphics, Language: versions.Language, HeadsetTunes: versions.HeadTunes, BaseTunes: versions.BaseTunes}
	if !dectVersionsMatch(identity, archive) {
		return errors.New("engage 75 component versions did not verify after restart")
	}
	for _, field := range []struct{ address, op, expected byte }{{1, 7, selected.LanguageRegion}, {1, 0x22, selected.BaseRegion}, {10, 0x22, selected.HeadRegion}} {
		data, err := query(field.address, field.op)
		if err != nil {
			return err
		}
		value, err := decodeDECTRegion(data)
		if err != nil || value != field.expected {
			return errors.New("engage 75 firmware region changed")
		}
	}
	return check()
}

func (nativeSitelDECTBackend) previewEnter(ctx context.Context, device USBDevice) error {
	if err := requireHardwareWrites(); err != nil {
		return managementNotSent(err)
	}
	r, closeConnection, err := openSitelRuntime(device)
	if err != nil {
		return managementNotSent(err)
	}
	defer func() { _ = closeConnection() }()
	// Unlike the old entry adapter, keep an absent reply as ambiguous. The
	// executor may observe the expected attachment, but never replay this entry.
	_, err = r.exchange(ctx, 1, 7, 0x80, nil)
	return err
}

func (nativeSitelDECTBackend) previewVerifyRuntime(ctx context.Context, device USBDevice, selected engage75SelectedIdentity, archive *sitelDECTArchive) error {
	r, closeConnection, err := openSitelRuntime(device)
	if err != nil {
		return err
	}
	defer func() { _ = closeConnection() }()
	return r.verifyEngage75SelectedRuntime(ctx, device, selected, archive, func() error { return validateUSBDevice(device) })
}

// An ACK confirms delivery, not the resulting program. Read the head again on
// the still-bound base transport before issuing the base return command. Do
// not compare loader geometry with Main geometry; firmware can change it.
func verifyEngage75HeadMain(ctx context.Context, peer sitelRequest, validate func() error) error {
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	check := func() error {
		if err := probe.Err(); err != nil {
			return err
		}
		if validate != nil {
			if err := validate(); err != nil {
				return err
			}
		}
		return probe.Err()
	}
	if err := check(); err != nil {
		return err
	}
	raw, err := peer.request(probe, 0, nil)
	if err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	info, err := decodeSitelInfo(raw)
	if err != nil || info.ID != 0x0b0e1116 || info.Mode != 1 {
		return errors.New("engage 75 headset did not verify in Main after return; base return was not sent")
	}
	return nil
}
