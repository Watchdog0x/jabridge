package main

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/history"
)

func validResetInstance(instance string) bool {
	if len(instance) != 32 {
		return false
	}
	_, err := hex.DecodeString(instance)
	return err == nil
}

var errResetAlreadyAttempted = errors.New("reset was already attempted on this attachment")

func eligibleLink390Reset(device *jabra_DeviceInfo) bool {
	return device != nil && device.vendorID == jabraVendorID && device.productID == 0x2e57 && device.variantType == "04-10" && device.firmwareVersion == "1.3.0" && device.isDongle && device.deviceConnection == deviceConnectionType_USB && device.parentDeviceID == 0 && validResetInstance(device.instance) && device.usbGeneration != "" && device.serialNumber != "" && filepath.IsAbs(device.usbDevicePath)
}

type link390ResetAttempt struct {
	Generation, Serial string
	Outcome            link390ResetOutcome
}

var resetAttempts = struct {
	sync.Mutex
	ports map[string]link390ResetAttempt
}{ports: map[string]link390ResetAttempt{}}

type link390ResetHIDSource struct{ *link390HIDSource }

func (s *link390ResetHIDSource) BeginResetAttempt(target link390Identity) error {
	resetAttempts.Lock()
	defer resetAttempts.Unlock()
	if old, ok := resetAttempts.ports[target.Port]; ok && old.Generation == target.Generation && old.Serial == target.USBSerial {
		return errResetAlreadyAttempted
	}
	if _, exists := resetAttempts.ports[target.Port]; !exists && len(resetAttempts.ports) >= 256 {
		return errors.New("reset attempt record is full; restart the service before another reset")
	}
	resetAttempts.ports[target.Port] = link390ResetAttempt{Generation: target.Generation, Serial: target.USBSerial, Outcome: resetUnknown}
	return nil
}
func (s *link390ResetHIDSource) SetResetOutcome(target link390Identity, outcome link390ResetOutcome) {
	resetAttempts.Lock()
	defer resetAttempts.Unlock()
	old := resetAttempts.ports[target.Port]
	if old.Generation == target.Generation && old.Serial == target.USBSerial {
		old.Outcome = outcome
		resetAttempts.ports[target.Port] = old
	}
}
func (s *link390ResetHIDSource) OpenReset(target link390Identity, readOnly bool) (link390ResetSession, error) {
	session, err := s.Open(target, readOnly)
	if err != nil {
		return nil, err
	}
	native, ok := session.(*link390HIDSession)
	if !ok {
		session.Close()
		return nil, errLink390Identity
	}
	return &link390ResetHIDSession{link390HIDSession: native}, nil
}

type link390ResetHIDSession struct{ *link390HIDSession }

func (s *link390ResetHIDSession) Reset() error {
	if err := s.canWrite(); err != nil {
		return err
	}
	return gnpCommand(s.h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, gnpOpFactoryDefaultBT, nil)
}
func (s *link390ResetHIDSession) Summary() link390ResetSummary {
	summary := link390ResetSummary{}
	deadline := time.Now().Add(5 * time.Second)
	query := func(class, op byte, payload []byte) ([]byte, error) {
		if !time.Now().Before(deadline) {
			return nil, context.DeadlineExceeded
		}
		generation, err := s.Attachment()
		if err != nil || generation != s.generation {
			return nil, errLink390Changed
		}
		return gnpQueryPayloadWithDataTimeout(s.h, gnpSrcDongle, nextSeq(), class, op, payload, 400*time.Millisecond)
	}
	if payload, err := query(gnpClassConfig, 0x0e, nil); err == nil && len(payload) == 1 && payload[0] >= 1 && payload[0] <= 3 {
		v := payload[0]
		summary.WirelessRange = &v
	}
	for _, item := range []struct {
		key   string
		value **bool
	}{{"auto-pairing", &summary.AutoPairing}, {"bluetooth-radio", &summary.BluetoothRadio}, {"softphone-integration", &summary.Softphone}} {
		def, ok := findBoolSettingDefinition(settingScopeDongle, item.key)
		if !ok {
			continue
		}
		payload, err := query(def.Class, def.Op, def.Request)
		if err == nil {
			value, err := decodeBoolSettingPayload(def, payload)
			if err == nil {
				*item.value = &value
			}
		}
	}
	summary.Pairings, _ = readPairingSummary([]byte{0, 1}, func(op byte, payload []byte) ([]byte, error) { return query(gnpClassPairingDevice, op, payload) })
	return summary
}

func (j *jabraAPIBridge) FactoryResetBound(binding ipc.SettingTarget) (ipc.BoundResetResult, error) {
	device, err := selectedDongleDevice()
	if err != nil {
		return ipc.BoundResetResult{}, err
	}
	if err := validateSettingTarget(device, &binding); err != nil {
		return ipc.BoundResetResult{}, err
	}
	if !eligibleLink390Reset(device) {
		return ipc.BoundResetResult{}, ErrNotSupported
	}
	entry := historyDeviceEvent(device, "reset")
	entry.Method, entry.Operation, entry.Phase = "device.reset.bound", history.NextOperation(), "start"
	history.Record(entry)
	result := resetLink390Dongle(device)
	entry.Phase, entry.ResetOutcome = "observed", string(result.Outcome)
	if result.cause != nil {
		entry.Error = history.Classify(result.cause)
	}
	history.Record(entry)
	return ipcLink390ResetResult(result), nil
}

func resetLink390Dongle(device *jabra_DeviceInfo) (result link390ResetResult) {
	result.Outcome = resetRefused
	refuse := func(err error) link390ResetResult { result.Detail = err.Error(); result.cause = err; return result }
	if !eligibleLink390Reset(device) {
		return refuse(ErrNotSupported)
	}
	release, err := beginSettingTransaction()
	if err != nil {
		return refuse(err)
	}
	defer release()
	if err := currentSettingDevice(device); err != nil {
		return refuse(err)
	}
	port, err := filepath.EvalSymlinks(device.usbDevicePath)
	if err != nil {
		return refuse(errLink390Identity)
	}
	acquireCtx, cancelAcquire := context.WithTimeout(context.Background(), 5*time.Second)
	owner, err := link390ControlGate.exclusive(acquireCtx, port, device.usbDevicePath)
	cancelAcquire()
	if err != nil {
		return refuse(err)
	}
	defer owner.release()
	target, err := link390USBIdentity(port, device.hidrawPath)
	if err != nil {
		return refuse(err)
	}
	if target.Generation != device.usbGeneration {
		invalidateLink390Registry(device)
		return refuse(errLink390Changed)
	}
	if target.ProductID != device.productID || target.USBSerial != device.serialNumber {
		return refuse(errLink390Identity)
	}
	target.GNPSerial, target.Firmware, target.Variant = target.USBSerial, device.firmwareVersion, device.variantType
	if !resetLink390IdentityAllowed(target) {
		return refuse(errLink390Identity)
	}
	if err := uniqueLink390USB(target); err != nil {
		return refuse(err)
	}
	source := &link390ResetHIDSource{&link390HIDSource{owner: owner}}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	result = runLink390Reset(ctx, source, target)
	if !result.Attempted || result.Outcome == resetRejected {
		return result
	}
	if result.Reconnected {
		current, err := link390USBIdentity(port, result.Identity.HIDPath)
		if err == nil {
			parent, parentErr := controlUSBParent(result.Identity.HIDPath)
			if parentErr != nil || parent != port {
				err = errLink390Identity
			}
		}
		published, publishErr := publishLink390ConfigResult(device, target, link390ConfigResult{Status: link390Unknown, Identity: result.Identity}, current, err)
		if publishErr != nil {
			result.Outcome = resetUnknown
			result.Reconnected = false
			result.Detail = publishErr.Error()
			source.SetResetOutcome(target, result.Outcome)
			invalidateLink390Registry(device)
			return result
		}
		clearLink390ResetCache(published.Device)
		return result
	}
	// An unverified return is followed only by read-only recovery, never
	// another reset frame. Keep a reusable node only after identity is verified.
	fresh, err := source.OpenReset(target, true)
	if err != nil {
		invalidateLink390Registry(device)
		return result
	}
	identity, err := fresh.Identity()
	fresh.Close()
	if err != nil || !target.sameDevice(identity) {
		invalidateLink390Registry(device)
		return result
	}
	current, err := link390USBIdentity(port, identity.HIDPath)
	published, publishErr := publishLink390ConfigResult(device, target, link390ConfigResult{Status: link390Unknown, Identity: identity}, current, err)
	if publishErr != nil {
		invalidateLink390Registry(device)
	} else {
		clearLink390ResetCache(published.Device)
	}
	return result
}
func clearLink390ResetCache(device *jabra_DeviceInfo) {
	deviceStateMu.Lock()
	defer deviceStateMu.Unlock()
	current := deviceManager[int(device.deviceID)]
	if current == nil || current.instance != device.instance {
		return
	}
	current.pairingList = nil
	for id, child := range deviceManager {
		if child.deviceConnection == deviceConnectionType_BT && child.parentDeviceID == device.deviceID {
			delete(deviceManager, id)
		}
	}
	selectedHeadset = firstDeviceIndexLocked(false)
}
