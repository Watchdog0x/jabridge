package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/buildinfo"
	firmwaretool "github.com/Watchdog0x/jabridge/internal/firmware"
)

var inspectDECTComponents = firmwaretool.InspectDECTComponents
var inspectDECTImageInfo = firmwaretool.InspectDECTImageInfo

func dectDiagnostic(state, detail string) []ipc.DiagnosticCheck {
	return []ipc.DiagnosticCheck{{Feature: "DECT components", State: state, Detail: detail}}
}
func (j *jabraAPIBridge) DECTComponents(id uint16) ([]ipc.DiagnosticCheck, error) {
	return j.withDECTInspection(context.Background(), id, func(ctx context.Context, binding *firmwaretool.ControlBinding) ([]ipc.DiagnosticCheck, error) {
		report, err := inspectDECTComponents(ctx, binding)
		if err != nil {
			return nil, err
		}
		return dectComponentChecks(report), nil
	})
}

func (j *jabraAPIBridge) DECTImageInfo(ctx context.Context, id uint16) ([]ipc.DiagnosticCheck, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	device := deviceForID(id)
	if device == nil || !firmwaretool.DECTImageInfoSupported(device.productID) {
		return nil, errors.New("runtime GetInfo requires Engage 75 USB 1113")
	}
	return j.withDECTInspection(ctx, id, func(ctx context.Context, binding *firmwaretool.ControlBinding) ([]ipc.DiagnosticCheck, error) {
		observations, err := inspectDECTImageInfo(ctx, binding)
		if err != nil {
			return nil, err
		}
		checks := []ipc.DiagnosticCheck{{Feature: "DECT runtime GetInfo scope", State: "INFO", Detail: "Opt-in information requests only. No restart or update requested. Image IDs are not runtime PIDs or installation approval."}}
		for _, check := range observations {
			checks = append(checks, ipc.DiagnosticCheck{Feature: fmt.Sprintf("DECT %s address %d %s", check.Role, check.Address, check.Query), State: check.State, Detail: fmt.Sprintf("%s; %d ms", check.Detail, check.ElapsedMS)})
		}
		return checks, nil
	})
}

func (j *jabraAPIBridge) withDECTInspection(parent context.Context, id uint16, inspect func(context.Context, *firmwaretool.ControlBinding) ([]ipc.DiagnosticCheck, error)) ([]ipc.DiagnosticCheck, error) {
	device := deviceForID(id)
	if device == nil || device.vendorID != jabraVendorID || device.isDongle || device.deviceConnection != deviceConnectionType_USB || !firmwaretool.DECTInspectionSupported(device.productID) {
		return nil, fmt.Errorf("DECT component check requires a connected Engage 75 runtime base")
	}
	if !deviceDiagnosticMu.TryLock() {
		return dectDiagnostic("BUSY", "another device diagnostic is running; not tested"), nil
	}
	defer deviceDiagnosticMu.Unlock()
	if controlDeviceBusy(device) {
		return dectDiagnostic("BUSY", "device is busy; not tested"), nil
	}
	port, err := filepath.EvalSymlinks(device.usbDevicePath)
	if err != nil {
		return dectDiagnostic("BLOCKED", "cannot bind the selected USB base; no queries sent"), nil
	}
	gateCtx, cancel := context.WithTimeout(parent, 5*time.Second)
	owner, err := link390ControlGate.exclusive(gateCtx, port, device.usbDevicePath)
	cancel()
	if err != nil {
		return dectDiagnostic("BUSY", "control ownership is unavailable; not tested"), nil
	}
	defer owner.release()
	if err := parent.Err(); err != nil {
		return nil, err
	}
	binding, err := firmwaretool.BindControlDevice(firmwaretool.USBDevice{VendorID: device.vendorID, ProductID: device.productID, SysPath: port})
	if err != nil || !binding.MatchesSerial(device.serialNumber) {
		return dectDiagnostic("NOT TESTED", "device changed during check"), nil
	}
	unchanged := func() bool {
		current := deviceForID(id)
		return current != nil && current.instance == device.instance && current.vendorID == device.vendorID && current.productID == device.productID && current.usbDevicePath == device.usbDevicePath && current.serialNumber == device.serialNumber && owner.valid() && binding.MatchesSerial(device.serialNumber) && binding.Validate() == nil
	}
	if !unchanged() {
		return dectDiagnostic("NOT TESTED", "device changed during check"), nil
	}
	ctx, stop := context.WithTimeout(parent, 25*time.Second)
	checks, err := inspect(ctx, binding)
	stop()
	if !unchanged() || errors.Is(err, firmwaretool.ErrDECTInspectionChanged) {
		return dectDiagnostic("NOT TESTED", "device changed during check"), nil
	}
	if err != nil {
		return dectDiagnostic("BLOCKED", "component check unavailable: "+diagnosticError(err)), nil
	}
	return checks, nil
}

func dectComponentChecks(report firmwaretool.DECTComponentReport) []ipc.DiagnosticCheck {
	state := "BLOCKED"
	if report.IdentityPassed {
		state = "INFO"
	}
	checks := []ipc.DiagnosticCheck{{Feature: "DECT strict class-2 identity", State: state, Detail: report.InstallerIdentity},
		{Feature: "DECT inspection attempt", State: "INFO", Detail: "The installer repeats this check for up to 15 s; a fault that comes and goes between attempts may not show here."}}
	for _, check := range report.Checks {
		checks = append(checks, ipc.DiagnosticCheck{Feature: fmt.Sprintf("DECT %s address %d %s [%s]", check.Role, check.Address, check.Query, check.Step), State: check.State, Detail: fmt.Sprintf("%s; %d ms", check.Detail, check.ElapsedMS)})
	}
	if report.HeadsetPresence != "" {
		checks = append(checks, ipc.DiagnosticCheck{Feature: "DECT headset presence", State: report.HeadsetPresenceState, Detail: report.HeadsetPresence})
	}
	if report.LaterIdentityBlockers != "" {
		checks = append(checks, ipc.DiagnosticCheck{Feature: "DECT later identity blockers", State: "INFO", Detail: report.LaterIdentityBlockers})
	}
	if report.FirstFailure != "" {
		checks = append(checks, ipc.DiagnosticCheck{Feature: "DECT first read failure", State: "INFO", Detail: report.FirstFailure})
	}
	if report.LastFailure != "" && report.LastFailure != report.FirstFailure {
		checks = append(checks, ipc.DiagnosticCheck{Feature: "DECT last read failure", State: "INFO", Detail: report.LastFailure})
	}
	return checks
}

func writeDECTComponentDiagnostic(parent context.Context, out *bytes.Buffer, client *ipc.Client, device ipc.DeviceInfo, matchingService bool) {
	if device.IsDongle || device.Connection == "dongle" || !firmwaretool.DECTInspectionSupported(device.PID) {
		return
	}
	fmt.Fprintln(out, "DECT components (read-only, performed by the service)")
	if parent.Err() != nil {
		fmt.Fprintln(out, "  NOT TESTED DECT components: diagnostic time budget ended")
		return
	}
	unavailable := func() {
		fmt.Fprintln(out, "  NOT TESTED DECT components: this check needs the candidate service; see docs/TEST_ENGAGE75.md")
	}
	if !matchingService {
		unavailable()
		return
	}
	ctx, cancel := context.WithTimeout(parent, 35*time.Second)
	defer cancel()
	var capabilities ipc.ServiceCapabilities
	if err := client.Call(ctx, "service.capabilities", nil, &capabilities); err != nil || !slices.Contains(capabilities.Reads, "diagnostics.dect_components") {
		unavailable()
		return
	}
	var checks []ipc.DiagnosticCheck
	if err := client.Call(ctx, "diagnostics.dect_components", map[string]uint16{"id": device.ID}, &checks); err != nil {
		var remote *ipc.RemoteError
		if errors.As(err, &remote) && remote.Code == ipc.ErrCodeMethodNF {
			unavailable()
		} else {
			fmt.Fprintln(out, "  BLOCKED DECT components:", ipcDiagnosticFailure(err))
		}
		return
	}
	for _, check := range checks {
		fmt.Fprintf(out, "  %-12s %s: %s\n", check.State, check.Feature, check.Detail)
	}
	fmt.Fprintln(out, "No firmware, settings or modes were changed. Bootloader, erase and transfer steps were not started.")
}

func writeDECTImageInfoDiagnostic(parent context.Context, out *bytes.Buffer, client *ipc.Client, device ipc.DeviceInfo, matchingService bool) {
	if device.IsDongle || device.Connection == "dongle" || !firmwaretool.DECTImageInfoSupported(device.PID) {
		return
	}
	fmt.Fprintln(out, "DECT runtime image information (opt-in, information requests only)")
	if parent.Err() != nil {
		fmt.Fprintln(out, "  NOT TESTED: diagnostic time budget ended; no image request sent")
		return
	}
	ctx, cancel := context.WithTimeout(parent, 35*time.Second)
	defer cancel()
	if !matchingService {
		fmt.Fprintln(out, "  NOT TESTED: this check needs the matching candidate service; see docs/TEST_ENGAGE75.md")
		return
	}
	peer, err := client.DialPeer(ctx)
	if err != nil {
		fmt.Fprintln(out, "  NOT TESTED: a separate diagnostic connection is unavailable")
		return
	}
	defer func() { _ = peer.Close() }()
	var version struct {
		Version string `json:"version"`
	}
	var capabilities ipc.ServiceCapabilities
	if peer.Call(ctx, "version", nil, &version) != nil || version.Version != buildinfo.Version || peer.Call(ctx, "service.capabilities", nil, &capabilities) != nil || !slices.Contains(capabilities.Reads, "diagnostics.dect_image_info") {
		fmt.Fprintln(out, "  NOT TESTED: this check needs the matching candidate service; see docs/TEST_ENGAGE75.md")
		return
	}
	var checks []ipc.DiagnosticCheck
	if err := peer.CallScoped(ctx, "diagnostics.dect_image_info", map[string]uint16{"id": device.ID}, &checks); err != nil {
		var remote *ipc.RemoteError
		if errors.As(err, &remote) && remote.Code == ipc.ErrCodeMethodNF {
			fmt.Fprintln(out, "  NOT TESTED: this service does not support the extra image check")
		} else {
			fmt.Fprintln(out, "  BLOCKED:", ipcDiagnosticFailure(err))
		}
		return
	}
	for _, check := range checks {
		fmt.Fprintf(out, "  %-12s %s: %s\n", check.State, check.Feature, check.Detail)
	}
	fmt.Fprintln(out, "No restart or firmware update was requested. These observations do not authorize firmware installation.")
}
