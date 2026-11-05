package firmware

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

type sitelDECTIdentity struct {
	Base, Headset, USB, MMI          sitelIdentity
	BaseRegion, HeadsetRegion        byte
	BaseTunes, HeadsetTunes          string
	RadioVersion, Graphics, Language string
	LanguageRegion                   byte
}

func (r *sitelRuntime) identifyDECT(ctx context.Context, device USBDevice, archive *sitelDECTArchive) (sitelDECTIdentity, error) {
	var identity sitelDECTIdentity
	profile := archive.Profile
	if !profile.runtime(device.ProductID) || device.VendorID != JabraVendorID || device.ViaDongle || device.attachment == nil {
		return identity, errors.New("not a bound DECT base")
	}
	var address byte
	var attempts []error
	for _, candidate := range []byte{1, 8} {
		data, err := r.query(ctx, candidate, 0x11)
		if err == nil {
			var pid uint16
			pid, err = decodeDECTProductID(data)
			if err == nil && pid != device.ProductID && pid != profile.BootPID {
				err = dectValueError("base identity does not match USB")
			}
		}
		if err == nil {
			address = candidate
			break
		}
		attempts = append(attempts, dectFailure("base", candidate, 0x11, err))
	}
	if address == 0 {
		return identity, fmt.Errorf("DECT base identity does not match USB: %w", errors.Join(attempts...))
	}
	readComponent := func(address byte, expectedBoot uint16, usb bool) (sitelIdentity, error) {
		role := "base"
		switch address {
		case dectHeadsetAddress:
			role = "headset"
		case engage75DisplayAddress:
			role = "display"
		case engage75RadioAddress:
			role = "USB controller"
		}
		fail := func(op byte, err error) error { return dectFailure(role, address, op, err) }
		id := sitelIdentity{Address: address, Port: device.SysPath, Instance: device.attachment.fingerprint}
		pid, err := r.query(ctx, address, 0x11)
		if err != nil {
			return id, fail(0x11, err)
		}
		id.PID, err = decodeDECTProductID(pid)
		if err != nil {
			return id, fail(0x11, err)
		}
		if usb {
			id.PID = device.ProductID
			id.Serial = device.Serial
		}
		serialRejected := false
		if id.Serial == "" {
			data, readErr := r.query(ctx, address, 1)
			if errors.Is(readErr, errSitelRejected) {
				serialRejected = true
			} else {
				if readErr != nil {
					return id, fail(1, readErr)
				}
				id.Serial, err = decodeDECTSerial(data)
				if err != nil {
					return id, fail(1, err)
				}
			}
		}
		if id.Serial == "" && !profile.Legacy {
			if serialRejected {
				return id, fail(1, errSitelRejected)
			}
			return id, fail(1, dectValueError("component has no readable serial"))
		}
		variant, err := r.query(ctx, address, 2)
		if err != nil {
			return id, fail(2, err)
		}
		id.Variant, err = decodeDECTVariant(variant)
		if err != nil {
			return id, fail(2, err)
		}
		id.Version, err = r.text(ctx, address, 3)
		if err != nil {
			return id, fail(3, err)
		}
		if err = validateDECTVersion(id.Version); err != nil {
			return id, fail(3, err)
		}
		boot, err := r.query(ctx, address, 0x13)
		if profile.Legacy && errors.Is(err, errSitelRejected) {
			id.BootPID = expectedBoot
		} else {
			if err != nil {
				return id, fail(0x13, err)
			}
			id.BootPID, err = decodeDECTBootID(boot, expectedBoot)
			if err != nil {
				return id, fail(0x13, err)
			}
		}
		return id, nil
	}
	var err error
	identity.Base, err = readComponent(address, profile.BootPID, true)
	if err != nil {
		return identity, err
	}
	protocols, err := r.query(ctx, address, 0x14)
	if !profile.Legacy || !errors.Is(err, errSitelRejected) {
		if err == nil {
			err = validateDECTProtocols(protocols)
		}
		if err != nil {
			return identity, dectFailure("base", address, 0x14, err)
		}
	}
	identity.Headset, err = readComponent(dectHeadsetAddress, profile.HeadsetImagePID, false)
	if err != nil {
		return identity, err
	}
	if profile.USBImagePID != 0 {
		identity.USB, err = readComponent(engage75RadioAddress, profile.USBImagePID, false)
		if err != nil {
			return identity, fmt.Errorf("DECT USB controller identity: %w", err)
		}
	}
	if archive.Engage75 != nil {
		identity.MMI, err = readComponent(engage75DisplayAddress, profile.MMIImagePID, false)
		if err != nil {
			return identity, fmt.Errorf("engage 75 display identity: %w", err)
		}
		for _, item := range []struct {
			address, op byte
			value       *string
		}{{engage75RadioAddress, 3, &identity.RadioVersion}, {address, 9, &identity.Graphics}, {address, 6, &identity.Language}} {
			*item.value, err = r.text(ctx, item.address, item.op)
			if err != nil && item.address == engage75RadioAddress {
				// An interrupted radio may not start its application. Only an
				// already-bound recovery may accept this missing version later.
				identity.RadioVersion = ""
				continue
			}
			role := "base"
			if item.address == engage75RadioAddress {
				role = "radio"
			}
			if err != nil {
				return identity, dectFailure(role, item.address, item.op, err)
			}
			if err := validateDECTVersion(*item.value); err != nil {
				return identity, dectFailure(role, item.address, item.op, err)
			}
		}
		region, err := r.query(ctx, address, 7)
		if err != nil {
			return identity, dectFailure("base", address, 7, err)
		}
		identity.LanguageRegion, err = decodeDECTRegion(region)
		if err != nil {
			return identity, dectFailure("base", address, 7, err)
		}
		if identity.LanguageRegion != 7 {
			return identity, dectFailure("base", address, 7, dectValueError("engage 75 language region does not match its archive"))
		}
	}
	for _, image := range archive.Images {
		if image.File.Content != "tunepack" {
			continue
		}
		component := identity.Base.Address
		if image.File.Target == "headset" {
			component = dectHeadsetAddress
		}
		region, err := r.query(ctx, component, 0x22)
		role := "base"
		if component == dectHeadsetAddress {
			role = "headset"
		}
		if err != nil {
			return identity, dectFailure(role, component, 0x22, err)
		}
		observedRegion, err := decodeDECTRegion(region)
		if err != nil {
			return identity, dectFailure(role, component, 0x22, err)
		}
		wanted, err := strconv.ParseUint(image.File.RegionID, 10, 8)
		if err != nil || observedRegion != byte(wanted) {
			return identity, dectFailure(role, component, 0x22, dectValueError("sound-prompt region does not match this firmware"))
		}
		version, err := r.text(ctx, component, 0x21)
		if err != nil {
			return identity, dectFailure(role, component, 0x21, err)
		}
		if err := validateDECTVersion(version); err != nil {
			return identity, dectFailure(role, component, 0x21, err)
		}
		if component == dectHeadsetAddress {
			identity.HeadsetRegion, identity.HeadsetTunes = observedRegion, version
		} else {
			identity.BaseRegion, identity.BaseTunes = observedRegion, version
		}
	}
	return identity, nil
}

type sitelDECTBackend interface {
	runtime(context.Context, USBDevice, *sitelDECTArchive) (sitelDECTIdentity, error)
	enter(context.Context, USBDevice, byte) error
	boot(context.Context, USBDevice) (*sitelDECTConnection, error)
	wait(context.Context, USBDevice, uint16) (USBDevice, error)
	sleep(context.Context, time.Duration) error
}
type nativeSitelDECTBackend struct{}

func (nativeSitelDECTBackend) runtime(ctx context.Context, device USBDevice, archive *sitelDECTArchive) (sitelDECTIdentity, error) {
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return retryDECTIdentity(ready, func(ctx context.Context) (sitelDECTIdentity, error) {
		r, closeConnection, err := openSitelRuntime(device)
		if err != nil {
			return sitelDECTIdentity{}, err
		}
		defer func() { _ = closeConnection() }()
		assessment, err := r.assessDECTRuntime(ctx, device, archive, func() error { return validateUSBDevice(device) })
		return assessment.Identity, err
	}, func(ctx context.Context) error { return waitDFU(ctx, 100*time.Millisecond) })
}

func retryDECTIdentity(ctx context.Context, probe func(context.Context) (sitelDECTIdentity, error), pause func(context.Context) error) (sitelDECTIdentity, error) {
	var first, last error
	finish := func(cause error) (sitelDECTIdentity, error) {
		guidance := ""
		var failure *dectIdentityError
		if errors.As(first, &failure) && failure.role == "headset" {
			guidance = ". Run jabridge debug for a read-only component check."
		}
		if first == nil {
			return sitelDECTIdentity{}, fmt.Errorf("DECT components did not become ready within 15s: %w", cause)
		}
		if sameDECTFailure(first, last) {
			return sitelDECTIdentity{}, fmt.Errorf("DECT components did not become ready within 15s: failure: %v: %w%s", first, cause, guidance)
		}
		return sitelDECTIdentity{}, fmt.Errorf("DECT components did not become ready within 15s: first failure: %v; last failure: %v: %w%s", first, last, cause, guidance)
	}
	for {
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		id, err := probe(ctx)
		if err == nil {
			return id, nil
		}
		var assessment *dectRuntimeAssessmentError
		if errors.As(err, &assessment) && assessment.Assessment.State == dectAssessmentNeedsBootloaderIdentity {
			// A recognized incomplete assessment is not a transient dock
			// timeout and cannot become eligible by repeating the same NAKs.
			return id, err
		}
		if first == nil {
			first = err
		}
		last = err
		if err := pause(ctx); err != nil {
			return finish(err)
		}
	}
}
func (nativeSitelDECTBackend) enter(ctx context.Context, device USBDevice, address byte) error {
	if err := requireHardwareWrites(); err != nil {
		return err
	}
	r, closeConnection, err := openSitelRuntime(device)
	if err != nil {
		return err
	}
	defer func() { _ = closeConnection() }()
	_, err = r.exchange(ctx, address, 7, 0x80, nil)
	if sitelDisconnect(err) {
		return nil
	}
	return err
}
func (nativeSitelDECTBackend) wait(ctx context.Context, device USBDevice, pid uint16) (USBDevice, error) {
	return waitSitelDevice(ctx, nativeSitelBackend{}, device, pid)
}
func (nativeSitelDECTBackend) sleep(ctx context.Context, d time.Duration) error {
	return waitDFU(ctx, d)
}

type sitelDECTConnection struct {
	Root     *sitelRequester
	Close    func() error
	Validate func() error
}
type sitelDECTRequest struct {
	peer     *sitelRequester
	validate func() error
}

func (s sitelDECTRequest) request(ctx context.Context, op byte, data []byte) ([]byte, error) {
	if op == 2 || op == 3 || op == 5 {
		if err := requireHardwareWrites(); err != nil {
			return nil, err
		}
	}
	if s.validate != nil {
		if err := s.validate(); err != nil {
			return nil, err
		}
	}
	return s.peer.request(ctx, op, data)
}
func (c *sitelDECTConnection) peer(address byte) sitelRequest {
	peer := c.Root
	if address != 1 {
		peer = &sitelRequester{link: c.Root.link, address: address, timeout: c.Root.timeout}
	}
	return sitelDECTRequest{peer: peer, validate: c.Validate}
}
func (nativeSitelDECTBackend) boot(ctx context.Context, device USBDevice) (*sitelDECTConnection, error) {
	profile, ok := sitelDECTProfileForPID(device.ProductID)
	if !ok || profile.BootPID != device.ProductID || device.VendorID != JabraVendorID || device.ViaDongle {
		return nil, errors.New("not a supported DECT bootloader")
	}
	ready, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		raw, err := openSitelFirmwareHID(device)
		if err == nil {
			link := &sitelLink{io: raw, in: raw.in, out: raw.out, timeout: 2 * time.Second}
			if err := link.start(ready); err != nil {
				_ = raw.file.Close()
				return nil, err
			}
			return &sitelDECTConnection{Root: &sitelRequester{link: link, address: 1, timeout: 30 * time.Second}, Close: raw.file.Close, Validate: func() error { return validateUSBDevice(device) }}, nil
		}
		if waitErr := waitDFU(ready, 100*time.Millisecond); waitErr != nil {
			return nil, fmt.Errorf("DECT bootloader did not become ready: %v: %w", err, waitErr)
		}
	}
}
