package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	firmwaretool "github.com/Watchdog0x/jabridge/internal/firmware"
)

type verifiedLink390Change struct {
	Result link390ConfigResult
	Device *jabra_DeviceInfo
}

// A generation identifies a kernel USB attachment, never an open handle.
func link390USBIdentity(port, hid string) (link390Identity, error) {
	identity := link390Identity{Port: port, HIDPath: hid}
	info, err := os.Stat(port)
	if err != nil {
		return identity, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() {
		return identity, errLink390Identity
	}
	vid, err := readHexFile(filepath.Join(port, "idVendor"))
	if err != nil {
		return identity, err
	}
	pid, err := readHexFile(filepath.Join(port, "idProduct"))
	if err != nil {
		return identity, err
	}
	if vid != jabraVendorID || !scopedControlPID(pid) {
		return identity, errLink390Identity
	}
	bus, busErr := strconv.Atoi(readTextFile(filepath.Join(port, "busnum")))
	address, addressErr := strconv.Atoi(readTextFile(filepath.Join(port, "devnum")))
	if busErr != nil || addressErr != nil || bus < 1 || address < 1 || address > 127 {
		return identity, errLink390Identity
	}
	identity.VendorID, identity.ProductID = vid, pid
	identity.USBSerial = readTextFile(filepath.Join(port, "serial"))
	identity.Generation = fmt.Sprintf("%d:%d:%d:%d", stat.Dev, stat.Ino, bus, address)
	return identity, nil
}

func uniqueLink390USB(identity link390Identity) error {
	devices, err := enumerateJabraUSB()
	if err != nil {
		return err
	}
	matches := 0
	for _, device := range devices {
		if device.vendorID == identity.VendorID && device.productID == identity.ProductID && device.serial == identity.USBSerial {
			matches++
			port, err := filepath.EvalSymlinks(device.sysPath)
			if err != nil || port != identity.Port {
				return errLink390Identity
			}
		}
	}
	if matches != 1 {
		return errLink390Identity
	}
	return nil
}

type link390HIDSource struct {
	owner      *controlPermit
	definition link390ConfigDefinition
	device     *jabra_DeviceInfo
	operation  uint64
}

func (s *link390HIDSource) Now() time.Time { return time.Now() }
func (s *link390HIDSource) Sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *link390HIDSource) Open(target link390Identity, readOnly bool) (link390ConfigSession, error) {
	if s.owner == nil || !s.owner.valid() || s.owner.port != target.Port {
		return nil, errControlClosed
	}
	current, err := link390USBIdentity(target.Port, target.HIDPath)
	if err != nil {
		return nil, err
	}
	if current.USBSerial != target.USBSerial {
		return nil, errLink390Identity
	}
	if err := uniqueLink390USB(current); err != nil {
		return nil, err
	}
	probe := &jabra_DeviceInfo{vendorID: target.VendorID, productID: target.ProductID, usbDevicePath: target.Port}
	paths := findHidrawPathsForDevice(probe)
	lastErr := errors.New("the dongle's management interface is not ready")
	for _, path := range paths {
		h, err := openHidrawWithPermit(path, s.owner)
		if err != nil {
			lastErr = err
			continue
		}
		if h.controlPort != target.Port {
			h.close()
			return nil, errLink390Identity
		}
		h.controlOwner = s.owner
		session := &link390HIDSession{h: h, source: s, target: target, generation: current.Generation, readOnly: readOnly}
		if _, err := session.Identity(); err != nil {
			h.close()
			if errors.Is(err, errLink390Identity) {
				return nil, err
			}
			lastErr = err
			continue
		}
		return session, nil
	}
	return nil, lastErr
}

type link390HIDSession struct {
	attachmentProbe func() (string, error)
	h               *hidrawConn
	source          *link390HIDSource
	target          link390Identity
	generation      string
	readOnly        bool
}

func (s *link390HIDSession) kernelIdentity() (link390Identity, error) {
	if !s.source.owner.valid() {
		return link390Identity{}, errControlClosed
	}
	identity, err := link390USBIdentity(s.target.Port, s.h.path)
	if err != nil {
		return identity, err
	}
	parent, err := controlUSBParent(s.h.path)
	if err != nil {
		return identity, err
	}
	if parent != s.target.Port || identity.USBSerial != s.target.USBSerial {
		return identity, errLink390Identity
	}
	return identity, nil
}

func (s *link390HIDSession) Identity() (link390Identity, error) {
	before, err := s.kernelIdentity()
	if err != nil {
		return before, err
	}
	payload, err := gnpQueryPayloadWithDataTimeout(s.h, gnpSrcDongle, nextSeq(), gnpClassDevInfo, gnpOpSerialNumber, nil, 750*time.Millisecond)
	if err != nil {
		return before, err
	}
	serial, valid := decodeLengthPrefixedString(payload)
	if !valid || serial != s.target.USBSerial {
		return before, errLink390Identity
	}
	firmware, firmwareErr := gnpQueryPayloadWithDataTimeout(s.h, gnpSrcDongle, nextSeq(), gnpClassDevInfo, gnpOpFirmwareVer, nil, 750*time.Millisecond)
	if firmwareErr != nil {
		return before, firmwareErr
	}
	version, firmwareErr := decodeFirmwareVersionPayload(firmware)
	if firmwareErr != nil || version != s.target.Firmware {
		return before, errors.Join(errLink390Identity, firmwareErr, errors.New("dongle firmware changed"))
	}
	info, infoErr := gnpQueryPayloadWithDataTimeout(s.h, gnpSrcDongle, nextSeq(), gnpClassDevInfo, gnpOpDeviceInfo, nil, 750*time.Millisecond)
	variant, validVariant := decodeDeviceVariant(info)
	if infoErr != nil {
		return before, infoErr
	}
	if !validVariant || variant != s.target.Variant {
		return before, errLink390Identity
	}
	after, err := s.kernelIdentity()
	if err != nil {
		return after, err
	}
	if before.Generation != after.Generation {
		return after, errLink390Changed
	}
	after.GNPSerial = serial
	after.Firmware, after.Variant = version, variant
	if !after.valid() {
		return after, errLink390Identity
	}
	return after, nil
}

func (s *link390HIDSession) Attachment() (string, error) {
	if s.attachmentProbe != nil {
		return s.attachmentProbe()
	}
	identity, err := s.kernelIdentity()
	return identity.Generation, err
}

func (s *link390HIDSession) Read() (byte, error) {
	d := s.source.definition
	payload, err := gnpQueryPayloadWithDataTimeout(s.h, gnpSrcDongle, nextSeq(), d.Class, d.Op, d.Request, 750*time.Millisecond)
	if err != nil {
		return 0, err
	}
	return decodeLink390ConfigValue(d.Key, payload)
}

func (s *link390HIDSession) canWrite() error {
	if s.readOnly {
		return errors.New("reconnected handles are read-only")
	}
	generation, err := s.Attachment()
	if err != nil || generation != s.generation {
		return errors.Join(errLink390Changed, err)
	}
	return nil
}

func (s *link390HIDSession) Enter() (bool, error) {
	if err := s.canWrite(); err != nil {
		return false, err
	}
	payload, err := gnpQueryPayloadWithDataTimeout(s.h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, 0x11, nil, time.Second)
	if err != nil {
		return !strings.Contains(err.Error(), "GNP NAK"), err
	}
	if len(payload) == 1 && payload[0] == 1 {
		return true, nil
	}
	if len(payload) == 1 && payload[0] == 0 {
		return false, errors.New("the dongle denied configuration permission")
	}
	return true, errors.New("the dongle returned an invalid configuration permission reply")
}

func (s *link390HIDSession) Write(value byte) error {
	if err := s.canWrite(); err != nil {
		return err
	}
	d := s.source.definition
	payload, err := link390ConfigPayload(d, value)
	if err != nil {
		return err
	}
	err = gnpCommand(s.h, gnpSrcDongle, nextSeq(), d.Class, d.Op, payload)
	if s.source.device != nil {
		recordSettingEvidence(s.source.device, d.Key, s.source.operation, "setting-ack", err)
	}
	return err
}

func (s *link390HIDSession) End() error {
	if err := s.canWrite(); err != nil {
		return err
	}
	err := gnpCommand(s.h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, 0x12, nil)
	if err != nil && !strings.Contains(err.Error(), "GNP NAK") &&
		(errors.Is(err, firmwaretool.ErrControlReadTimeout) || errors.Is(err, io.EOF) || errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrDeadlineExceeded) ||
			strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "timed out") || strings.Contains(err.Error(), "disconnected") || strings.Contains(err.Error(), "poll failed")) {
		return errors.Join(errLink390Uncertain, err)
	}
	return err
}

func (s *link390HIDSession) Close() { s.h.close() }

func changeLink390Config(device *jabra_DeviceInfo, definition link390ConfigDefinition, value byte, expected *byte) (verified verifiedLink390Change, actionErr error) {
	verified.Result = link390ConfigResult{Key: definition.Key, Status: link390NotApplied}
	if !exactDongleSettingProfile(device) || !definition.valid() || !definition.allows(value) {
		return verified, ErrNotSupported
	}
	release, err := beginSettingTransaction()
	if err != nil {
		return verified, err
	}
	defer release()
	if err := currentSettingDevice(device); err != nil {
		return verified, err
	}
	port, err := filepath.EvalSymlinks(device.usbDevicePath)
	if err != nil {
		return verified, errLink390Identity
	}
	acquireCtx, acquireCancel := context.WithTimeout(context.Background(), 5*time.Second)
	owner, err := link390ControlGate.exclusive(acquireCtx, port, device.usbDevicePath)
	acquireCancel()
	if err != nil {
		return verified, err
	}
	defer owner.release()
	var recoveryTarget link390Identity
	source := &link390HIDSource{owner: owner, definition: definition}
	defer func() {
		if actionErr == nil {
			return
		}
		if !recoveryTarget.valid() {
			invalidateLink390Registry(device)
			return
		}
		fresh, err := source.Open(recoveryTarget, true)
		if err != nil {
			invalidateLink390Registry(device)
			return
		}
		identity, err := fresh.Identity()
		fresh.Close()
		if err != nil || !recoveryTarget.sameDevice(identity) {
			invalidateLink390Registry(device)
			return
		}
		current, err := link390USBIdentity(identity.Port, identity.HIDPath)
		result := verified.Result
		result.Identity = identity
		updated, publishErr := publishLink390ConfigResult(device, recoveryTarget, result, current, err)
		if publishErr != nil {
			invalidateLink390Registry(device)
			return
		}
		verified.Device = updated.Device
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	target, err := link390USBIdentity(port, device.hidrawPath)
	if err != nil {
		return verified, err
	}
	if target.ProductID != device.productID || target.USBSerial == "" || target.USBSerial != device.serialNumber {
		return verified, errLink390Identity
	}
	target.GNPSerial = target.USBSerial // Expected value; verified over every fresh handle.
	target.Firmware, target.Variant = device.firmwareVersion, device.variantType
	if !target.valid() {
		return verified, errLink390Identity
	}
	recoveryTarget = target
	if err := uniqueLink390USB(target); err != nil {
		return verified, err
	}
	operation := startSettingWriteEvidence(device, definition.Key)
	source.device, source.operation = device, operation
	result, err := runLink390ConfigChange(ctx, source, target, definition.Key, value, expected)
	verified.Result = result
	recordSettingEvidence(device, definition.Key, operation, "setting-readback", err)
	if err != nil {
		return verified, err
	}
	// Publish only while the verified attachment still exists. Do not perform a
	// generic PID-based refresh or replace this result with another device read.
	current, err := link390USBIdentity(port, result.Identity.HIDPath)
	if err == nil {
		parent, parentErr := controlUSBParent(result.Identity.HIDPath)
		if parentErr != nil || parent != port {
			err = errLink390Identity
		}
	}
	return publishLink390ConfigResult(device, target, result, current, err)
}

// Do not leave a reusable node name or stale target after unverified recovery.
func invalidateLink390Registry(device *jabra_DeviceInfo) {
	deviceStateMu.Lock()
	stored := deviceManager[int(device.deviceID)]
	if stored == nil || stored.instance != device.instance {
		deviceStateMu.Unlock()
		return
	}
	delete(deviceManager, int(device.deviceID))
	for id, child := range deviceManager {
		if child.deviceConnection == deviceConnectionType_BT && child.parentDeviceID == device.deviceID {
			delete(deviceManager, id)
		}
	}
	selectedDongle, selectedHeadset = firstDeviceIndexLocked(true), firstDeviceIndexLocked(false)
	deviceStateMu.Unlock()
	requestUIRedraw()
}

func publishLink390ConfigResult(device *jabra_DeviceInfo, target link390Identity, result link390ConfigResult, current link390Identity, captureErr error) (verifiedLink390Change, error) {
	verified := verifiedLink390Change{Result: result}
	failure := func(cause error) (verifiedLink390Change, error) {
		result.Status = link390Unknown
		var err error
		verified.Result, err = result.failure(cause)
		return verified, err
	}
	if captureErr != nil || current.Port != target.Port || current.VendorID != target.VendorID || current.ProductID != target.ProductID || current.Generation != result.Identity.Generation || current.USBSerial != target.USBSerial {
		return failure(errors.Join(errLink390Changed, captureErr))
	}
	deviceStateMu.Lock()
	stored := deviceManager[int(device.deviceID)]
	if stored == nil || stored.instance != device.instance || stored.serialNumber != device.serialNumber || stored.productID != device.productID || stored.usbDevicePath != device.usbDevicePath {
		deviceStateMu.Unlock()
		return failure(errLink390Identity)
	}
	stored.hidrawPath = result.Identity.HIDPath
	if result.Identity.Generation != target.Generation {
		stored.instance = newDeviceInstance()
	}
	verified.Device = cloneDeviceInfo(stored)
	deviceStateMu.Unlock()
	verified.Result = result
	return verified, nil
}
