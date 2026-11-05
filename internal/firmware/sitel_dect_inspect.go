package firmware

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"
)

const (
	dectHeadsetAddress     byte = 10
	engage75DisplayAddress byte = 3
	engage75RadioAddress   byte = 2
)

// Inspection uses the published Engage 75 component map, never a sibling PID.
func DECTInspectionSupported(pid uint16) bool { return engage75Profile.runtime(pid) }

// Addresses describe roles only for this family, not for every GNP device.
func DECTComponentRole(pid uint16, address byte) string {
	if !DECTInspectionSupported(pid) {
		return ""
	}
	switch address {
	case 1, 8:
		return "base"
	case dectHeadsetAddress:
		return "headset"
	case engage75DisplayAddress:
		return "display controller"
	case engage75RadioAddress:
		return "radio"
	}
	return ""
}

type dectParsedError struct {
	label string
	cause error
}

func (e *dectParsedError) Error() string { return e.label }
func (e *dectParsedError) Unwrap() error { return e.cause }

type dectValueError string

func (e dectValueError) Error() string { return string(e) }

type dectIdentityError struct {
	role        string
	address, op byte
	cause       error
}

func (e *dectIdentityError) Unwrap() error { return e.cause }
func (e *dectIdentityError) Error() string {
	cause := "transport error"
	var value dectValueError
	var parsed *dectParsedError
	var errno syscall.Errno
	switch {
	case errors.As(e.cause, &value):
		cause = string(value)
	case errors.As(e.cause, &parsed):
		cause = parsed.label
	case errors.Is(e.cause, context.DeadlineExceeded):
		cause = "timeout"
	case errors.Is(e.cause, context.Canceled):
		cause = "cancelled"
	case errors.Is(e.cause, errSitelRejected):
		cause = "rejected"
	case e.cause.Error() == "invalid identity string length", e.cause.Error() == "invalid identity string":
		cause = e.cause.Error()
	case strings.HasPrefix(e.cause.Error(), "malformed Sitel management packet"):
		cause = "malformed management packet"
	case e.cause.Error() == "sitel firmware event queue overflow":
		cause = "event queue overflow"
	case errors.Is(e.cause, errManagementNotSent):
		cause = "request not sent"
	case errors.As(e.cause, &errno):
		cause = "transport error: " + errno.Error()
	}
	return fmt.Sprintf("DECT %s (address %d) %s (0x%02x): %s", e.role, e.address, dectQueryName(e.op), e.op, cause)
}
func dectFailure(role string, address, op byte, err error) error {
	return &dectIdentityError{role: role, address: address, op: op, cause: err}
}
func dectQueryName(op byte) string {
	switch op {
	case 0x11:
		return "product ID"
	case 1:
		return "serial"
	case 2:
		return "variant"
	case 3:
		return "version"
	case 0x13:
		return "boot ID"
	case 0x14:
		return "protocols"
	case 9:
		return "graphics"
	case 6:
		return "language"
	case 7:
		return "language region"
	case 0x22:
		return "sound-prompt region"
	case 0x21:
		return "sound-prompt version"
	}
	return "identity query"
}
func decodeDECTProductID(data []byte) (uint16, error) {
	if len(data) != 2 {
		return 0, dectValueError(fmt.Sprintf("returned %d bytes, expected 2", len(data)))
	}
	pid := binary.LittleEndian.Uint16(data)
	if pid == 0 || pid == 0xffff {
		return 0, dectValueError("invalid product ID")
	}
	return pid, nil
}
func decodeDECTBootID(data []byte, expected uint16) (uint16, error) {
	pid, err := decodeDECTProductID(data)
	if err != nil {
		return 0, err
	}
	if pid != expected {
		return 0, dectValueError("firmware identity does not match its archive")
	}
	return pid, nil
}
func decodeDECTVariant(data []byte) (string, error) {
	// Preserve the existing Sitel decoder's accepted layout.
	if len(data) < 3 || len(data) > 16 {
		return "", dectValueError("invalid variant reply")
	}
	return hex.EncodeToString(data[1:]), nil
}
func validateDECTVersion(value string) error {
	if _, err := parseVersionTriplet(value); err != nil {
		return &dectParsedError{label: "invalid version format", cause: err}
	}
	return nil
}
func validateDECTProtocols(data []byte) error {
	if !bytes.Contains(data, []byte{4}) {
		return dectValueError("base does not report Sitel firmware support")
	}
	return nil
}
func decodeDECTRegion(data []byte) (byte, error) {
	if len(data) != 1 {
		return 0, dectValueError(fmt.Sprintf("returned %d bytes, expected 1", len(data)))
	}
	return data[0], nil
}

// Each row is one logical installer step; the base probe and base identity
// deliberately read the product ID separately. No failed step is retried.
type DECTComponentCheck struct {
	Role                       string
	Address                    byte
	Query, Step, State, Detail string
	ElapsedMS                  int64
}

type DECTComponentReport struct {
	Checks                                       []DECTComponentCheck
	InstallerIdentity, FirstFailure, LastFailure string
	IdentityPassed                               bool
	HeadsetPresenceState, HeadsetPresence        string
	LaterIdentityBlockers                        string
}

// Summarize replies already collected, never issue another query or use these
// labels to authorize an installation. A cradle version reply is the vendor's
// first presence check; a rejected PID alone says nothing about docking.
func (r *DECTComponentReport) summarizeEvidence(stop error) {
	versions := map[string]string{}
	headAttempts, headAnswers := 0, 0
	for _, check := range r.Checks {
		if check.State == "PASS" && check.Query == "version (0x03)" {
			versions[check.Role] = check.Detail
		}
		if check.Role == "headset" && (check.State == "PASS" || check.State == "BLOCKED") {
			headAttempts++
			if check.State == "PASS" {
				headAnswers++
			}
		}
	}
	r.HeadsetPresenceState = "INFO"
	switch {
	case versions["headset"] != "":
		r.HeadsetPresence = "measured: answered at address 10 (version reply); docked per its version reply"
	case headAttempts > 0 && headAnswers == 0:
		r.HeadsetPresenceState = "BLOCKED"
		r.HeadsetPresence = "no successful headset identity read at address 10; check docking and connection"
	default:
		r.HeadsetPresence = "headset presence unknown; no valid version reply was measured"
	}
	var failure *dectIdentityError
	if !errors.As(stop, &failure) {
		return
	}
	if version := versions[failure.role]; version != "" && errors.Is(failure.cause, errSitelRejected) &&
		(failure.role == "headset" || failure.role == "display") &&
		(failure.op == 0x11 || failure.op == 0x13 || failure.role == "display" && failure.op == 1) {
		r.InstallerIdentity += fmt.Sprintf(". The component answered its version (%s) but does not report this identity value, so the installer cannot confirm its firmware image and stops before writing.", version)
	}
	first := -1
	for i, check := range r.Checks {
		if check.State == "BLOCKED" && check.Role == failure.role && check.Address == failure.address && check.Query == fmt.Sprintf("%s (0x%02x)", dectQueryName(failure.op), failure.op) {
			first = i
			break
		}
	}
	// A failed base probe is an INFO row: either address can establish the
	// base. If both fail, all subsequent BLOCKED component rows are later.
	var later []string
	for _, check := range r.Checks[first+1:] {
		if check.State == "BLOCKED" {
			later = append(later, fmt.Sprintf("%s %d %s", check.Role, check.Address, check.Query))
		}
	}
	if len(later) > 0 {
		r.LaterIdentityBlockers = "measured: " + strings.Join(later, ", ")
	}
}

var ErrDECTInspectionChanged = errors.New("device changed during check")

type dectReadOnlyIO struct {
	next      csrStageIO
	validate  func() error
	usbSerial bool
	changed   bool
}

func allowedDECTIdentityQuery(address, op byte, usbSerial bool) bool {
	switch address {
	case 1, 8:
		switch op {
		case 0x11, 2, 3, 0x13, 0x14, 9, 6, 7, 0x22, 0x21:
			return true
		case 1:
			return !usbSerial
		}
	case dectHeadsetAddress:
		switch op {
		case 0x11, 1, 2, 3, 0x13, 0x22, 0x21:
			return true
		}
	case engage75DisplayAddress:
		switch op {
		case 0x11, 1, 2, 3, 0x13:
			return true
		}
	case engage75RadioAddress:
		return op == 3
	}
	return false
}
func (io *dectReadOnlyIO) Write(ctx context.Context, report []byte) error {
	if io.changed {
		return managementNotSent(ErrDECTInspectionChanged)
	}
	if len(report) < 7 || report[0] != 5 || report[4] != 0x46 || report[5] != 2 || !allowedDECTIdentityQuery(report[1], report[6], io.usbSerial) {
		return managementNotSent(errors.New("request is outside the DECT identity read allowlist"))
	}
	if err := io.validate(); err != nil {
		io.changed = true
		return managementNotSent(ErrDECTInspectionChanged)
	}
	return io.next.Write(ctx, report)
}
func (io *dectReadOnlyIO) Read(ctx context.Context) ([]byte, error) {
	if io.changed {
		return nil, ErrDECTInspectionChanged
	}
	data, err := io.next.Read(ctx)
	if changed := io.validate(); changed != nil {
		io.changed = true
		return nil, ErrDECTInspectionChanged
	}
	return data, err
}

// InspectDECTComponents never authorizes installation or enters a bootloader.
// Its attachment is freshly bound by the service; results are not cached.
func InspectDECTComponents(ctx context.Context, binding *ControlBinding) (DECTComponentReport, error) {
	if binding == nil || binding.device.VendorID != JabraVendorID || binding.device.ViaDongle || !DECTInspectionSupported(binding.device.ProductID) {
		return DECTComponentReport{}, errors.New("DECT component inspection requires a bound Engage 75 runtime base")
	}
	if err := binding.Validate(); err != nil {
		return DECTComponentReport{}, ErrDECTInspectionChanged
	}
	transport, err := binding.Open()
	if err != nil {
		return DECTComponentReport{}, err
	}
	defer func() { _ = transport.Close() }()
	r := sitelRuntimeFromManagement(transport)
	return inspectDECTComponents(ctx, binding.device, r.io, binding.Validate, 3*time.Second, 25*time.Second)
}

type dectInspector struct {
	ctx          context.Context
	runtime      *sitelRuntime
	queryTimeout time.Duration
	report       DECTComponentReport
	timedOut     map[byte]bool
	invalidBase  map[byte]bool
	stop         error
}

func (d *dectInspector) skip(role string, address, op byte, step, reason string) {
	d.report.Checks = append(d.report.Checks, DECTComponentCheck{Role: role, Address: address, Query: fmt.Sprintf("%s (0x%02x)", dectQueryName(op), op), Step: step, State: "NOT TESTED", Detail: reason})
}
func (d *dectInspector) read(role string, address, op byte, step string, blocking bool, decode func([]byte) (string, error)) ([]byte, error) {
	if d.timedOut[address] || d.invalidBase[address] || d.ctx.Err() != nil {
		reason := "skipped after timeout"
		if d.invalidBase[address] {
			reason = "skipped after invalid base component identity"
		}
		if d.ctx.Err() != nil {
			reason = "total check budget ended or check cancelled"
		}
		d.skip(role, address, op, step, reason)
		err := dectFailure(role, address, op, context.DeadlineExceeded)
		if blocking && d.stop == nil {
			d.stop = err
		}
		return nil, err
	}
	started := time.Now()
	data, err := d.runtime.exchangeTimeout(d.ctx, address, 2, 0x40, []byte{op}, d.queryTimeout)
	detail := ""
	if err == nil {
		detail, err = decode(data)
	}
	state := "PASS"
	if err != nil {
		err = dectFailure(role, address, op, err)
		state, detail = "BLOCKED", err.Error()
		if !blocking {
			state = "INFO"
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			d.timedOut[address] = true
		}
		if d.report.FirstFailure == "" {
			d.report.FirstFailure = detail
		}
		d.report.LastFailure = detail
		if blocking && d.stop == nil {
			d.stop = err
		}
	}
	d.report.Checks = append(d.report.Checks, DECTComponentCheck{Role: role, Address: address, Query: fmt.Sprintf("%s (0x%02x)", dectQueryName(op), op), Step: step, State: state, Detail: detail, ElapsedMS: time.Since(started).Milliseconds()})
	return data, err
}
func dectPIDDetail(data []byte) (string, error) {
	pid, err := decodeDECTProductID(data)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("0x%04x", pid), nil
}
func dectVersionDetail(data []byte) (string, error) {
	value, err := decodeExtendedIdentityString(data)
	if err != nil {
		return "", dectValueError("invalid identity string")
	}
	if err := validateDECTVersion(value); err != nil {
		return "", err
	}
	return value, nil
}
func (d *dectInspector) component(role string, address byte, boot uint16, usbSerial bool) {
	const step = "component identity"
	if address == 0 {
		for _, op := range []byte{0x11, 1, 2, 3, 0x13} {
			d.skip(role, address, op, step, "no matching base")
		}
		return
	}
	_, pidErr := d.read(role, address, 0x11, step, true, dectPIDDetail)
	if role == "base" && pidErr != nil {
		d.invalidBase[address] = true
	}
	if usbSerial && (d.timedOut[address] || d.invalidBase[address] || d.ctx.Err() != nil) {
		d.skip(role, address, 1, step, "skipped after invalid or unavailable base identity")
	} else if usbSerial {
		d.report.Checks = append(d.report.Checks, DECTComponentCheck{Role: role, Address: address, Query: "serial", Step: step, State: "INFO", Detail: "present (bound USB serial; no serial query sent)"})
	} else {
		_, _ = d.read(role, address, 1, step, true, func(data []byte) (string, error) {
			value, err := decodeDECTSerial(data)
			if err != nil {
				return "", err
			}
			if value == "" {
				return "", dectValueError("component has no readable serial")
			}
			return "present", nil
		})
	}
	_, _ = d.read(role, address, 2, step, true, func(data []byte) (string, error) { return decodeDECTVariant(data) })
	_, _ = d.read(role, address, 3, step, true, dectVersionDetail)
	_, _ = d.read(role, address, 0x13, step, true, func(data []byte) (string, error) {
		pid, err := decodeDECTBootID(data, boot)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("0x%04x", pid), nil
	})
}
func inspectDECTComponents(ctx context.Context, device USBDevice, io csrStageIO, validate func() error, queryTimeout, totalTimeout time.Duration) (DECTComponentReport, error) {
	if device.VendorID != JabraVendorID || device.ViaDongle || !DECTInspectionSupported(device.ProductID) {
		return DECTComponentReport{}, errors.New("unsupported DECT inspection target")
	}
	if err := validate(); err != nil {
		return DECTComponentReport{}, ErrDECTInspectionChanged
	}
	bounded, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()
	guard := &dectReadOnlyIO{next: io, validate: validate, usbSerial: device.Serial != ""}
	d := &dectInspector{ctx: bounded, runtime: &sitelRuntime{io: guard}, queryTimeout: queryTimeout, timedOut: map[byte]bool{}, invalidBase: map[byte]bool{}}
	var base byte
	var probes []error
	for _, address := range []byte{1, 8} {
		_, err := d.read("base", address, 0x11, "base probe", false, func(data []byte) (string, error) {
			pid, err := decodeDECTProductID(data)
			if err != nil {
				return "", err
			}
			if pid != device.ProductID && pid != engage75Profile.BootPID {
				return "", dectValueError("base identity does not match USB")
			}
			return fmt.Sprintf("0x%04x", pid), nil
		})
		if err == nil {
			base = address
			break
		}
		probes = append(probes, err)
	}
	if base == 0 {
		d.stop = fmt.Errorf("DECT base identity does not match USB: %w", errors.Join(probes...))
	}
	d.component("base", base, engage75Profile.BootPID, device.Serial != "")
	if base != 0 {
		_, _ = d.read("base", base, 0x14, "base protocols", true, func(data []byte) (string, error) {
			if err := validateDECTProtocols(data); err != nil {
				return "", err
			}
			return "protocol 4 advertised", nil
		})
	} else {
		d.skip("base", 0, 0x14, "base protocols", "no matching base")
	}
	d.component("headset", dectHeadsetAddress, engage75Profile.HeadsetImagePID, false)
	d.component("display", engage75DisplayAddress, engage75Profile.MMIImagePID, false)
	_, radioErr := d.read("radio", engage75RadioAddress, 3, "radio version", false, dectVersionDetail)
	var radioValue *dectParsedError
	if errors.As(radioErr, &radioValue) && radioValue.label == "invalid version format" {
		d.report.Checks[len(d.report.Checks)-1].State = "BLOCKED"
		if d.stop == nil {
			d.stop = radioErr
		}
	}
	for _, op := range []byte{9, 6, 7} {
		if base == 0 {
			d.skip("base", 0, op, "base resources", "no matching base")
			continue
		}
		decode := dectVersionDetail
		if op == 7 {
			decode = func(data []byte) (string, error) {
				v, err := decodeDECTRegion(data)
				if err != nil {
					return "", err
				}
				if v != 7 {
					return "", dectValueError("engage 75 language region does not match its archive")
				}
				return fmt.Sprint(v), nil
			}
		}
		_, _ = d.read("base", base, op, "base resources", true, decode)
	}
	for _, target := range []struct {
		role    string
		address byte
	}{{"headset", dectHeadsetAddress}, {"base", base}} {
		for _, op := range []byte{0x22, 0x21} {
			if target.address == 0 {
				d.skip(target.role, 0, op, "sound prompts", "no matching base")
				continue
			}
			decode := dectVersionDetail
			if op == 0x22 {
				decode = func(data []byte) (string, error) {
					v, err := decodeDECTRegion(data)
					if err != nil {
						return "", err
					}
					if v == 0 {
						return "", dectValueError("sound-prompt region is unavailable")
					}
					return fmt.Sprintf("%d (checked against the archive at install time)", v), nil
				}
			}
			_, _ = d.read(target.role, target.address, op, "sound prompts", true, decode)
		}
	}
	if err := validate(); err != nil {
		return DECTComponentReport{}, ErrDECTInspectionChanged
	}
	if guard.changed || errors.Is(d.stop, ErrDECTInspectionChanged) {
		return DECTComponentReport{}, ErrDECTInspectionChanged
	}
	if d.stop != nil {
		d.report.InstallerIdentity = "would stop at " + d.stop.Error()
	} else if radioErr != nil {
		d.report.InstallerIdentity = "identity reads passed, but a new install would stop: the radio version is unavailable (only an unfinished, bound recovery accepts that)"
	} else {
		d.report.IdentityPassed = true
		d.report.InstallerIdentity = "one installer identity attempt would pass with these replies (sound-prompt regions are checked against the archive at install time)"
	}
	d.report.summarizeEvidence(d.stop)
	return d.report, nil
}

func decodeDECTSerial(data []byte) (string, error) {
	if len(data) == 1 && data[0] == 0 {
		return "", nil
	}
	value, err := decodeExtendedIdentityString(data)
	if err != nil {
		return "", &dectParsedError{label: "invalid serial reply", cause: err}
	}
	return value, nil
}

// Compare private causes rather than their redacted presentation. Distinct
// failures can intentionally have identical privacy-safe display strings.
func sameDECTFailure(a, b error) bool {
	var x, y *dectIdentityError
	if errors.As(a, &x) && errors.As(b, &y) {
		return x.role == y.role && x.address == y.address && x.op == y.op && x.cause.Error() == y.cause.Error()
	}
	return a.Error() == b.Error()
}
