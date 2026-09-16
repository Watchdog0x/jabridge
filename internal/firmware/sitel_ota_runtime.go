package firmware

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

type sitelOTATarget struct {
	Parent         USBDevice
	ParentAddress  byte
	ParentIdentity string
	Child          sitelIdentity
	Region         byte
	TunesVersion   string
}

func (t sitelOTATarget) childIdentity() string {
	return WirelessFirmwareIdentity(t.Child.PID, t.Child.Serial, t.Child.Variant)
}

func (r *sitelRuntime) identifyOTA(ctx context.Context, parent USBDevice) (sitelOTATarget, error) {
	target := sitelOTATarget{Parent: parent}
	if parent.VendorID != JabraVendorID || parent.ViaDongle || parent.attachment == nil || !sitelOTAParentPID(parent.ProductID) {
		return target, errors.New("not a bound wireless Engage adapter or base")
	}
	for _, address := range []byte{1, 8} {
		data, err := r.query(ctx, address, 0x11)
		if err == nil && len(data) == 2 && (binary.LittleEndian.Uint16(data) == parent.ProductID || binary.LittleEndian.Uint16(data) == sitelOTAParentImagePID(parent.ProductID)) {
			target.ParentAddress = address
			break
		}
	}
	if target.ParentAddress == 0 {
		return target, errors.New("wireless Engage parent identity does not match USB")
	}
	serial := parent.Serial
	var err error
	if serial == "" {
		serial, err = r.serial(ctx, target.ParentAddress)
	}
	if err != nil || serial == "" {
		return target, errors.New("wireless Engage parent has no readable serial")
	}
	variant, err := r.variant(ctx, target.ParentAddress)
	if err != nil {
		return target, err
	}
	target.ParentIdentity = sitelIdentityHash(sitelIdentity{PID: parent.ProductID, Port: parent.SysPath, Serial: serial, Variant: variant})
	pid, err := r.query(ctx, 4, 0x11)
	if err != nil || len(pid) != 2 {
		return target, errors.New("wireless Engage headset is not responding")
	}
	child := sitelIdentity{PID: binary.LittleEndian.Uint16(pid), Address: 4, Port: parent.SysPath, Instance: parent.attachment.fingerprint}
	if !SupportsWirelessFirmware(parent.ProductID, child.PID) {
		return target, errors.New("this headset and adapter do not have a supported wireless firmware route")
	}
	child.Serial, err = r.serial(ctx, 4)
	if err != nil || child.Serial == "" {
		return target, errors.New("wireless Engage headset has no readable serial")
	}
	child.Variant, err = r.variant(ctx, 4)
	if err != nil {
		return target, err
	}
	child.Version, err = r.text(ctx, 4, 3)
	if err != nil {
		return target, err
	}
	if _, err := parseVersionTriplet(child.Version); err != nil {
		return target, err
	}
	boot, err := r.query(ctx, 4, 0x13)
	if err != nil || len(boot) != 2 || binary.LittleEndian.Uint16(boot) != sitelOTAImagePID {
		return target, errors.New("wireless Engage firmware identity does not match")
	}
	child.BootPID = sitelOTAImagePID
	protocols, err := r.query(ctx, 4, 0x14)
	if err != nil || !bytes.Contains(protocols, []byte{12}) {
		return target, errors.New("headset does not report wireless Sitel update support")
	}
	target.Child = child
	if target.childIdentity() == "" {
		return target, errors.New("incomplete wireless Engage identity")
	}
	region, err := r.query(ctx, 4, 0x22)
	if err != nil || len(region) != 1 || region[0] == 0 {
		return target, errors.New("wireless Engage sound-prompt region is unavailable")
	}
	target.Region = region[0]
	target.TunesVersion, err = r.text(ctx, 4, 0x21)
	if err != nil {
		return target, err
	}
	if _, err := parseVersionTriplet(target.TunesVersion); err != nil {
		return target, err
	}
	return target, nil
}

type sitelOTABackend interface {
	inspect(context.Context, USBDevice) (sitelOTATarget, error)
	conditions(context.Context, sitelOTATarget) (byte, error)
	enter(context.Context, sitelOTATarget) error
	channel(context.Context, sitelOTATarget) (sitelRequest, func() error, error)
	exit(context.Context, sitelOTATarget) error
	wait(context.Context, time.Duration) error
}

type nativeSitelOTABackend struct{}

func (nativeSitelOTABackend) inspect(ctx context.Context, parent USBDevice) (sitelOTATarget, error) {
	r, closeConnection, err := openSitelRuntime(parent)
	if err != nil {
		return sitelOTATarget{}, err
	}
	defer func() { _ = closeConnection() }()
	return r.identifyOTA(ctx, parent)
}

func (nativeSitelOTABackend) conditions(ctx context.Context, target sitelOTATarget) (byte, error) {
	r, closeConnection, err := openSitelRuntime(target.Parent)
	if err != nil {
		return 0, err
	}
	defer func() { _ = closeConnection() }()
	data, err := r.exchange(ctx, target.ParentAddress, 15, 0x40, []byte{0x35})
	if err != nil {
		return 0, err
	}
	if len(data) != 1 {
		return 0, errors.New("invalid wireless Engage update conditions")
	}
	return data[0], nil
}

func (nativeSitelOTABackend) enter(ctx context.Context, target sitelOTATarget) error {
	if err := requireHardwareWrites(); err != nil {
		return err
	}
	if err := waitDFU(ctx, time.Second); err != nil {
		return err
	}
	r, closeConnection, err := openSitelRuntime(target.Parent)
	if err != nil {
		return err
	}
	defer func() { _ = closeConnection() }()
	// This permission READ changes device mode and therefore needs the same
	// authorization and durable checkpoint as an ordinary write command.
	data, err := r.exchangeTimeout(ctx, target.ParentAddress, 13, 0x40, []byte{0x69}, 15*time.Second)
	if err != nil {
		return err
	}
	if len(data) != 1 || data[0] != 1 {
		return errors.New("wireless Engage update permission was denied")
	}
	return nil
}

func (nativeSitelOTABackend) exit(ctx context.Context, target sitelOTATarget) error {
	if err := requireHardwareWrites(); err != nil {
		return err
	}
	r, closeConnection, err := openSitelRuntime(target.Parent)
	if err != nil {
		return err
	}
	defer func() { _ = closeConnection() }()
	_, err = r.exchangeTimeout(ctx, target.ParentAddress, 13, 0x80, []byte{0x6a}, 10*time.Second)
	return err
}

type boundSitelOTARequest struct {
	peer   sitelRequest
	parent USBDevice
}

func (s boundSitelOTARequest) request(ctx context.Context, op byte, data []byte) ([]byte, error) {
	if op > 4 {
		return nil, errors.New("wireless Engage transfer cannot reboot the USB parent")
	}
	if op == 2 || op == 3 {
		if err := requireHardwareWrites(); err != nil {
			return nil, err
		}
	}
	if err := validateUSBDevice(s.parent); err != nil {
		return nil, err
	}
	return s.peer.request(ctx, op, data)
}

func (nativeSitelOTABackend) channel(ctx context.Context, target sitelOTATarget) (sitelRequest, func() error, error) {
	if !SupportsWirelessFirmware(target.Parent.ProductID, target.Child.PID) {
		return nil, nil, errors.New("unsupported wireless Engage route")
	}
	ready, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	for {
		raw, err := openSitelFirmwareHID(target.Parent)
		if err == nil {
			link := &sitelLink{io: raw, in: raw.in, out: raw.out, timeout: 2 * time.Second}
			if err := link.start(ready); err != nil {
				_ = raw.file.Close()
				return nil, nil, err
			}
			peer := &sitelRequester{link: link, address: 4, timeout: 30 * time.Second}
			return boundSitelOTARequest{peer: peer, parent: target.Parent}, raw.file.Close, nil
		}
		if err := validateUSBDevice(target.Parent); err != nil {
			return nil, nil, err
		}
		if waitErr := waitDFU(ready, 100*time.Millisecond); waitErr != nil {
			return nil, nil, fmt.Errorf("wireless Engage firmware channel did not open: %v: %w", err, waitErr)
		}
	}
}

func (nativeSitelOTABackend) wait(ctx context.Context, d time.Duration) error { return waitDFU(ctx, d) }
