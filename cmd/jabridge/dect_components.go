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
	firmwaretool "github.com/Watchdog0x/jabridge/internal/firmware"
)

var inspectDECTComponents = firmwaretool.InspectDECTComponents

func dectDiagnostic(state, detail string) []ipc.DiagnosticCheck {
	return []ipc.DiagnosticCheck{{Feature: "DECT components", State: state, Detail: detail}}
}
func (j *jabraAPIBridge) DECTComponents(id uint16) ([]ipc.DiagnosticCheck, error) {
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
	gateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	owner, err := link390ControlGate.exclusive(gateCtx, port, device.usbDevicePath)
	cancel()
	if err != nil {
		return dectDiagnostic("BUSY", "control ownership is unavailable; not tested"), nil
	}
	defer owner.release()
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
	ctx, stop := context.WithTimeout(context.Background(), 25*time.Second)
	report, err := inspectDECTComponents(ctx, binding)
	stop()
	if !unchanged() || errors.Is(err, firmwaretool.ErrDECTInspectionChanged) {
		return dectDiagnostic("NOT TESTED", "device changed during check"), nil
	}
	if err != nil {
		return dectDiagnostic("BLOCKED", "component check unavailable: "+diagnosticError(err)), nil
	}
	state := "BLOCKED"
	if report.IdentityPassed {
		state = "INFO"
	}
	checks := []ipc.DiagnosticCheck{{Feature: "DECT installer identity", State: state, Detail: report.InstallerIdentity},
		{Feature: "DECT inspection attempt", State: "INFO", Detail: "The installer repeats this check for up to 15 s; a fault that comes and goes between attempts may not show here."}}
	for _, check := range report.Checks {
		checks = append(checks, ipc.DiagnosticCheck{Feature: fmt.Sprintf("DECT %s address %d %s [%s]", check.Role, check.Address, check.Query, check.Step), State: check.State, Detail: fmt.Sprintf("%s; %d ms", check.Detail, check.ElapsedMS)})
	}
	if report.FirstFailure != "" {
		checks = append(checks, ipc.DiagnosticCheck{Feature: "DECT first read failure", State: "INFO", Detail: report.FirstFailure})
	}
	if report.LastFailure != "" && report.LastFailure != report.FirstFailure {
		checks = append(checks, ipc.DiagnosticCheck{Feature: "DECT last read failure", State: "INFO", Detail: report.LastFailure})
	}
	return checks, nil
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
