package firmware

import (
	"context"
	"errors"
	"fmt"
)

type dectAssessmentState uint8

const (
	dectAssessmentBlocked dectAssessmentState = iota
	dectAssessmentNeedsBootloaderIdentity
	dectAssessmentComplete
)

type dectObservationState uint8

const (
	dectNotAttempted dectObservationState = iota
	dectMeasured
	dectRejected
	dectMalformed
	dectTimedOut
	dectTransportFailed
)

// Private evidence, never rendered with raw values: serials stay in memory.
// Query source and outcome remain distinct from expected image identities.
type dectObservation[T any] struct {
	Address, Class, Operation byte
	State                     dectObservationState
	Value                     T
	cause                     error
}

type dectComponentAssessment struct {
	PID, DFUPID     dectObservation[uint16]
	Serial, Variant dectObservation[string]
	Version         dectObservation[string]
	Image           dectObservation[sitelDeviceInfo]
	ExpectedImageID uint32
}

type dectRuntimeAssessment struct {
	State            dectAssessmentState
	Identity         sitelDECTIdentity
	Headset, Display dectComponentAssessment
	Resources        dectResourceAssessment
	// These observations describe the current normal management transport.
	// They are not a persisted identity scheme or installation authorization.
	phase string
}

type dectResourceAssessment struct {
	Graphics, Language, Radio, HeadTunes, BaseTunes dectObservation[string]
	LanguageRegion, HeadRegion, BaseRegion          dectObservation[byte]
}

type dectRuntimeAssessmentError struct {
	Assessment dectRuntimeAssessment
	reason     string
	cause      error
}

func (e *dectRuntimeAssessmentError) Error() string { return e.reason }
func (e *dectRuntimeAssessmentError) Unwrap() error { return e.cause }

func observeDECT[T any](ctx context.Context, r *sitelRuntime, validate func() error, address, class, operation byte, decode func([]byte) (T, error)) dectObservation[T] {
	observation := dectObservation[T]{Address: address, Class: class, Operation: operation}
	if err := ctx.Err(); err != nil {
		observation.State, observation.cause = dectTimedOut, err
		return observation
	}
	if err := validate(); err != nil {
		observation.State, observation.cause = dectTransportFailed, ErrDECTInspectionChanged
		return observation
	}
	data, err := r.exchange(ctx, address, class, 0x40, []byte{operation})
	if validate() != nil {
		observation.State, observation.cause = dectTransportFailed, ErrDECTInspectionChanged
		return observation
	}
	switch {
	case errors.Is(err, errSitelRejected):
		observation.State, observation.cause = dectRejected, err
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		observation.State, observation.cause = dectTimedOut, err
	case err != nil:
		observation.State, observation.cause = dectTransportFailed, err
	default:
		observation.Value, err = decode(data)
		if err != nil {
			observation.State, observation.cause = dectMalformed, err
		} else {
			observation.State = dectMeasured
		}
	}
	return observation
}

func decodeDECTVersionObservation(data []byte) (string, error) {
	value, err := decodeExtendedIdentityString(data)
	if err != nil {
		return "", err
	}
	return value, validateDECTVersion(value)
}

// assessDECTRuntime preserves the strict path. The narrowly recognized old
// firmware pattern can only produce an incomplete result, never Ready with
// invented display identity or an authorization to enter the bootloader.
func (r *sitelRuntime) assessDECTRuntime(ctx context.Context, device USBDevice, archive *sitelDECTArchive, validate func() error) (dectRuntimeAssessment, error) {
	assessment := dectRuntimeAssessment{State: dectAssessmentBlocked, phase: "runtime-management"}
	checkBinding := validate
	changed := false
	validate = func() error {
		if changed || checkBinding() != nil {
			changed = true
			return ErrDECTInspectionChanged
		}
		return nil
	}
	if err := validate(); err != nil {
		return assessment, ErrDECTInspectionChanged
	}
	identity, strictErr := r.identifyDECT(ctx, device, archive)
	if validate() != nil {
		return assessment, ErrDECTInspectionChanged
	}
	assessment.Identity = identity
	if strictErr == nil {
		assessment.State = dectAssessmentComplete
		return assessment, nil
	}
	var failure *dectIdentityError
	base := identity.Base
	if archive.Engage75 == nil || device.VendorID != JabraVendorID || device.ProductID != 0x1113 || device.ViaDongle ||
		base.Address != 1 || base.PID != 0x1113 || base.BootPID != 0x1111 || base.Variant != "001b" || base.Version != "4.4.1" ||
		!errors.As(strictErr, &failure) || failure.role != "headset" || failure.address != 10 || failure.op != 0x11 || !errors.Is(strictErr, errSitelRejected) {
		return assessment, strictErr
	}
	block := func(reason string, cause error) (dectRuntimeAssessment, error) {
		if ctx.Err() != nil {
			cause = ctx.Err()
		}
		if changed {
			assessment = dectRuntimeAssessment{State: dectAssessmentBlocked, phase: "runtime-management"}
			reason, cause = "device binding changed; observations discarded", ErrDECTInspectionChanged
		}
		return assessment, &dectRuntimeAssessmentError{Assessment: assessment, reason: "engage 75 runtime assessment blocked: " + reason, cause: cause}
	}
	readComponent := func(address byte, component *dectComponentAssessment, firstPIDRejected bool) error {
		if firstPIDRejected {
			component.PID = dectObservation[uint16]{Address: address, Class: 2, Operation: 0x11, State: dectRejected, cause: strictErr}
		} else {
			component.PID = observeDECT(ctx, r, validate, address, 2, 0x11, decodeDECTProductID)
		}
		if component.PID.State != dectRejected {
			return errors.New("runtime product ID does not match the reviewed rejection pattern")
		}
		component.Serial = observeDECT(ctx, r, validate, address, 2, 1, decodeDECTSerial)
		if address == 10 {
			if component.Serial.State != dectMeasured || component.Serial.Value == "" {
				return errors.New("headset serial is unavailable")
			}
		} else if component.Serial.State != dectRejected {
			return errors.New("display serial does not match the reviewed rejection pattern")
		}
		component.Variant = observeDECT(ctx, r, validate, address, 2, 2, decodeDECTVariant)
		if component.Variant.State != dectMeasured {
			return errors.New("component variant is unavailable")
		}
		component.Version = observeDECT(ctx, r, validate, address, 2, 3, decodeDECTVersionObservation)
		if component.Version.State != dectMeasured {
			return errors.New("component version is unavailable")
		}
		component.DFUPID = observeDECT(ctx, r, validate, address, 2, 0x13, decodeDECTProductID)
		wanted := "0148"
		if address == 3 {
			wanted = "0304"
		}
		if component.Variant.State != dectMeasured || component.Variant.Value != wanted || component.Version.State != dectMeasured || component.Version.Value != "4.4.1" || component.DFUPID.State != dectRejected {
			return errors.New("component fields do not match the reviewed old-firmware pattern")
		}
		return nil
	}
	if err := readComponent(10, &assessment.Headset, true); err != nil {
		return block(err.Error(), strictErr)
	}
	if err := readComponent(3, &assessment.Display, false); err != nil {
		return block(err.Error(), strictErr)
	}
	assessment.Headset.ExpectedImageID = uint32(JabraVendorID)<<16 | 0x1116
	assessment.Display.ExpectedImageID = uint32(JabraVendorID)<<16 | 0x1117
	assessment.Headset.Image = observeDECT(ctx, r, validate, 10, 15, 0, decodeSitelInfo)
	if assessment.Headset.Image.State != dectMeasured || assessment.Headset.Image.Value.ID != assessment.Headset.ExpectedImageID || assessment.Headset.Image.Value.Mode != 1 {
		return block("headset runtime GetInfo image or mode is not confirmed", assessment.Headset.Image.cause)
	}
	assessment.Display.Image = observeDECT(ctx, r, validate, 3, 15, 0, decodeSitelInfo)
	if assessment.Display.Image.State != dectRejected {
		return block("display runtime GetInfo does not match the reviewed rejection pattern", assessment.Display.Image.cause)
	}
	resources := &assessment.Resources
	resources.Graphics = observeDECT(ctx, r, validate, 1, 2, 9, decodeDECTVersionObservation)
	resources.Language = observeDECT(ctx, r, validate, 1, 2, 6, decodeDECTVersionObservation)
	resources.Radio = observeDECT(ctx, r, validate, 2, 2, 3, decodeDECTVersionObservation)
	resources.LanguageRegion = observeDECT(ctx, r, validate, 1, 2, 7, decodeDECTRegion)
	resources.HeadRegion = observeDECT(ctx, r, validate, 10, 2, 0x22, decodeDECTRegion)
	resources.HeadTunes = observeDECT(ctx, r, validate, 10, 2, 0x21, decodeDECTVersionObservation)
	resources.BaseRegion = observeDECT(ctx, r, validate, 1, 2, 0x22, decodeDECTRegion)
	resources.BaseTunes = observeDECT(ctx, r, validate, 1, 2, 0x21, decodeDECTVersionObservation)
	for _, version := range []dectObservation[string]{resources.Graphics, resources.Language, resources.Radio} {
		if version.State != dectMeasured || version.Value != "4.4.1" {
			return block("base resource or radio version is not confirmed", version.cause)
		}
	}
	if resources.LanguageRegion.State != dectMeasured || resources.LanguageRegion.Value != 7 ||
		resources.HeadRegion.State != dectMeasured || resources.HeadRegion.Value != 1 ||
		resources.BaseRegion.State != dectMeasured || resources.BaseRegion.Value != 1 ||
		resources.HeadTunes.State != dectMeasured || resources.BaseTunes.State != dectMeasured {
		return block("firmware regions or sound-prompt versions are not confirmed", nil)
	}
	// Populate only genuinely measured fields. The unavailable PID/DFUPID
	// fields remain zero; current recovery binding will not accept this as a
	// complete identity even if a caller accidentally ignores the error.
	assessment.Identity.Headset.Serial = assessment.Headset.Serial.Value
	assessment.Identity.Headset.Variant = assessment.Headset.Variant.Value
	assessment.Identity.Headset.Version = assessment.Headset.Version.Value
	assessment.Identity.MMI = sitelIdentity{Address: 3, Port: base.Port, Instance: base.Instance, Variant: assessment.Display.Variant.Value, Version: assessment.Display.Version.Value}
	assessment.Identity.RadioVersion, assessment.Identity.Graphics, assessment.Identity.Language = resources.Radio.Value, resources.Graphics.Value, resources.Language.Value
	assessment.Identity.LanguageRegion = resources.LanguageRegion.Value
	assessment.Identity.HeadsetRegion, assessment.Identity.BaseRegion = resources.HeadRegion.Value, resources.BaseRegion.Value
	assessment.Identity.HeadsetTunes, assessment.Identity.BaseTunes = resources.HeadTunes.Value, resources.BaseTunes.Value
	for _, image := range archive.Images {
		if compareVersions(dectComponentVersion(assessment.Identity, image.File), image.File.Version) > 0 {
			return block("a component is newer than the archive; refusing a downgrade", nil)
		}
	}
	if compareVersions(assessment.Identity.RadioVersion, archive.Manifest.Version) > 0 {
		return block("radio firmware is newer than the archive; refusing a downgrade", nil)
	}
	if err := validate(); err != nil {
		return block("device binding changed", ErrDECTInspectionChanged)
	}
	assessment.State = dectAssessmentNeedsBootloaderIdentity
	return assessment, &dectRuntimeAssessmentError{Assessment: assessment, reason: "engage 75 display image identity needs bootloader verification; headset image is measured by runtime GetInfo; no mode change or firmware write was attempted"}
}

func (a dectRuntimeAssessment) String() string {
	return fmt.Sprintf("DECT runtime assessment state %d (%s)", a.State, a.phase)
}
