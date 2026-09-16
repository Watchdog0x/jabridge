package firmware

import (
	"bytes"
	"context"
	"encoding/binary"
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
	for _, candidate := range []byte{1, 8} {
		data, err := r.query(ctx, candidate, 0x11)
		if err == nil && len(data) == 2 && (binary.LittleEndian.Uint16(data) == device.ProductID || binary.LittleEndian.Uint16(data) == profile.BootPID) {
			address = candidate
			break
		}
	}
	if address == 0 {
		return identity, errors.New("DECT base identity does not match USB")
	}
	readComponent := func(address byte, expectedBoot uint16, usb bool) (sitelIdentity, error) {
		id := sitelIdentity{Address: address, Port: device.SysPath, Instance: device.attachment.fingerprint}
		pid, err := r.query(ctx, address, 0x11)
		if err != nil || len(pid) != 2 {
			return id, errors.New("DECT component identity is unavailable; dock the headset in its base")
		}
		id.PID = binary.LittleEndian.Uint16(pid)
		if id.PID == 0 || id.PID == 0xffff {
			return id, errors.New("DECT component returned an invalid product ID")
		}
		if usb {
			id.PID = device.ProductID
			id.Serial = device.Serial
		}
		if id.Serial == "" {
			id.Serial, err = r.serial(ctx, address)
			if err != nil {
				return id, err
			}
		}
		if id.Serial == "" && !profile.Legacy {
			return id, errors.New("DECT component has no readable serial")
		}
		id.Variant, err = r.variant(ctx, address)
		if err != nil {
			return id, err
		}
		id.Version, err = r.text(ctx, address, 3)
		if err != nil {
			return id, err
		}
		if _, err := parseVersionTriplet(id.Version); err != nil {
			return id, err
		}
		boot, err := r.query(ctx, address, 0x13)
		if profile.Legacy && errors.Is(err, errSitelRejected) {
			id.BootPID = expectedBoot
		} else if err != nil || len(boot) != 2 {
			return id, errors.New("DECT bootloader identity is unavailable")
		} else {
			id.BootPID = binary.LittleEndian.Uint16(boot)
		}
		if id.BootPID != expectedBoot {
			return id, errors.New("DECT component firmware identity does not match its archive")
		}
		return id, nil
	}
	var err error
	identity.Base, err = readComponent(address, profile.BootPID, true)
	if err != nil {
		return identity, err
	}
	protocols, err := r.query(ctx, address, 0x14)
	if (!profile.Legacy || !errors.Is(err, errSitelRejected)) && (err != nil || !bytes.Contains(protocols, []byte{4})) {
		return identity, errors.New("DECT base does not report Sitel firmware support")
	}
	identity.Headset, err = readComponent(10, profile.HeadsetImagePID, false)
	if err != nil {
		return identity, err
	}
	if profile.USBImagePID != 0 {
		identity.USB, err = readComponent(2, profile.USBImagePID, false)
		if err != nil {
			return identity, fmt.Errorf("DECT USB controller identity: %w", err)
		}
	}
	if archive.Engage75 != nil {
		identity.MMI, err = readComponent(3, profile.MMIImagePID, false)
		if err != nil {
			return identity, fmt.Errorf("engage 75 display identity: %w", err)
		}
		for _, item := range []struct {
			address, op byte
			value       *string
		}{{2, 3, &identity.RadioVersion}, {address, 9, &identity.Graphics}, {address, 6, &identity.Language}} {
			*item.value, err = r.text(ctx, item.address, item.op)
			if err != nil && item.address == 2 {
				// An interrupted radio may not start its application. Only an
				// already-bound recovery may accept this missing version later.
				identity.RadioVersion = ""
				continue
			}
			if err != nil {
				return identity, err
			}
			if _, err := parseVersionTriplet(*item.value); err != nil {
				return identity, err
			}
		}
		region, err := r.query(ctx, address, 7)
		if err != nil || len(region) != 1 || region[0] != 7 {
			return identity, errors.New("engage 75 language region does not match its archive")
		}
		identity.LanguageRegion = region[0]
	}
	for _, image := range archive.Images {
		if image.File.Content != "tunepack" {
			continue
		}
		component := identity.Base.Address
		if image.File.Target == "headset" {
			component = 10
		}
		region, err := r.query(ctx, component, 0x22)
		if err != nil || len(region) != 1 {
			return identity, errors.New("DECT sound-prompt region is unavailable")
		}
		wanted, err := strconv.ParseUint(image.File.RegionID, 10, 8)
		if err != nil || region[0] != byte(wanted) {
			return identity, errors.New("DECT sound-prompt region does not match this firmware")
		}
		version, err := r.text(ctx, component, 0x21)
		if err != nil {
			return identity, err
		}
		if _, err := parseVersionTriplet(version); err != nil {
			return identity, err
		}
		if component == 10 {
			identity.HeadsetRegion, identity.HeadsetTunes = region[0], version
		} else {
			identity.BaseRegion, identity.BaseTunes = region[0], version
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
	for {
		r, closeConnection, err := openSitelRuntime(device)
		if err == nil {
			id, readErr := r.identifyDECT(ready, device, archive)
			_ = closeConnection()
			if readErr == nil {
				return id, nil
			}
			err = readErr
		}
		if waitErr := waitDFU(ready, 100*time.Millisecond); waitErr != nil {
			return sitelDECTIdentity{}, fmt.Errorf("DECT base and docked headset did not become ready: %v: %w", err, waitErr)
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
