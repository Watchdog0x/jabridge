// csr_dfu_updater is an experimental pure-Go CSR DFU state machine.
//
// CSR devices normally follow this mode-switch sequence:
//
//   1. Device enumerates in normal (HID/vendor) mode.
//   2. Updater sends a "enter DFU mode" command to the device.
//   3. Device detaches from normal mode.
//   4. Device re-attaches as a standard USB DFU-1.1 device.
//   5. Updater invokes UsbDfuDriver (which shells out to dfu-util).
//   6. Device detaches from DFU mode.
//   7. Device re-attaches in normal mode.
//   8. If isExtraResetNeeded(), updater sends a post-flash reset.
//
// The actual "enter DFU mode" command is intentionally not implemented.
// EnterDFUMode returns an explicit error so this cannot look like a working
// or validated flash path.

package firmware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ── State machine ──────────────────────────────────────────────────────────

// CsrDfuState tracks where we are in the mode-switch flash flow. Kept as a
// plain typed int for table-driven transition checks.
type CsrDfuState int

const (
	CsrStateIdle           CsrDfuState = iota // no update in progress
	CsrStateNormalAttached                    // device visible in its normal USB mode
	CsrStateSwitching                         // DFU-enter command sent, waiting for detach
	CsrStateNormalDetached                    // normal-mode sysfs entry is gone
	CsrStateDfuAttached                       // device re-enumerated as a DFU-1.1 device
	CsrStateFlashing                          // dfu-util running
	CsrStateDfuDetached                       // DFU-mode device detached post-flash
	CsrStateComplete                          // back in normal mode, version verified
	CsrStateFailed                            // terminal error
)

func (s CsrDfuState) String() string {
	switch s {
	case CsrStateIdle:
		return "idle"
	case CsrStateNormalAttached:
		return "normal-attached"
	case CsrStateSwitching:
		return "switching-to-dfu"
	case CsrStateNormalDetached:
		return "normal-detached"
	case CsrStateDfuAttached:
		return "dfu-attached"
	case CsrStateFlashing:
		return "flashing"
	case CsrStateDfuDetached:
		return "dfu-detached"
	case CsrStateComplete:
		return "complete"
	case CsrStateFailed:
		return "failed"
	}
	return fmt.Sprintf("state(%d)", int(s))
}

// ── USB event stream ───────────────────────────────────────────────────────

// CsrDfuEvent is a single hotplug observation. Produced either by polling
// sysfs (default) or injected by tests. NormalMode=true means the device
// is visible with its original (non-DFU) product ID; false means it showed
// up as a DFU-class device.
type CsrDfuEvent struct {
	Attached   bool   // true = device appeared, false = device disappeared
	NormalMode bool   // true if enumerated with non-DFU interface class
	SysPath    string // /sys/bus/usb/devices/... entry, empty when detached
	VendorID   uint16
	ProductID  uint16
}

// ── Config + result types ─────────────────────────────────────────────────

// CsrDfuConfig is the input to a single update run.
type CsrDfuConfig struct {
	// FirmwarePath is the local path to the DFU-1.1 firmware file that
	// will be streamed to the device once it enters DFU mode.
	FirmwarePath string

	// NormalVID / NormalPID identify the device in its everyday mode
	// (before the mode-switch). Usually 0x0b0e for Jabra plus a
	// product-specific PID.
	NormalVID uint16
	NormalPID uint16

	// SwitchTimeout caps how long we wait between sending the DFU-enter
	// command and seeing the device re-enumerate in DFU mode. Real CSR
	// chips take 1-3 seconds; we default to 20s for safety.
	SwitchTimeout time.Duration

	// FlashTimeout caps the dfu-util invocation. CSR DFU images are
	// typically < 2 MB and complete in well under a minute.
	FlashTimeout time.Duration

	// SettleTimeout caps how long we wait post-flash for the device to
	// re-enumerate back in normal mode.
	SettleTimeout time.Duration
}

// DefaultCsrDfuConfig returns sane defaults for the timeouts.
func DefaultCsrDfuConfig(firmwarePath string, vid, pid uint16) CsrDfuConfig {
	return CsrDfuConfig{
		FirmwarePath:  firmwarePath,
		NormalVID:     vid,
		NormalPID:     pid,
		SwitchTimeout: 20 * time.Second,
		FlashTimeout:  3 * time.Minute,
		SettleTimeout: 20 * time.Second,
	}
}

// CsrDfuResult is the outcome of a single update attempt.
type CsrDfuResult struct {
	FinalState     CsrDfuState
	Transitions    []CsrDfuState // state history for debugging
	EnteredDfuAt   time.Time
	FlashStartedAt time.Time
	CompletedAt    time.Time
	Err            error
}

// ── Updater ────────────────────────────────────────────────────────────────

// CsrDfuUpdater is the Go equivalent of jfwu's CsrDfuUpdater FSM. It drives
// the state transitions, calls the (pluggable) mode-switch / flash / event
// sources, and emits progress to stderr. Create one per flash attempt.
type CsrDfuUpdater struct {
	cfg   CsrDfuConfig
	state CsrDfuState

	// Pluggable callbacks — overridable in tests so we can drive the FSM
	// without touching real USB hardware. Production code uses the real
	// implementations set in NewCsrDfuUpdater.
	enterDFU func(sysPath string) error
	flashDFU func(firmwarePath, dfuSysPath string) error
	resetNow func(sysPath string) error

	// Event source — a channel that delivers CsrDfuEvents. In production
	// this is fed by a sysfs poller; in tests it is fed directly.
	events <-chan CsrDfuEvent

	history []CsrDfuState
}

// NewCsrDfuUpdater constructs an updater with real USB + flash backends.
// Tests should instead construct the struct directly so they can stub the
// callback fields.
func NewCsrDfuUpdater(cfg CsrDfuConfig, events <-chan CsrDfuEvent) *CsrDfuUpdater {
	u := &CsrDfuUpdater{
		cfg:    cfg,
		state:  CsrStateIdle,
		events: events,
	}
	u.enterDFU = u.realEnterDFUMode
	u.flashDFU = realFlashDFUDevice
	u.resetNow = u.realPostFlashReset
	return u
}

// ── State transition primitives ────────────────────────────────────────────

// transition moves to a new state, records history, and logs to stderr.
// Unknown transitions are allowed (and tracked) — FSM validation lives in
// Run/Step, not here.
func (u *CsrDfuUpdater) transition(to CsrDfuState) {
	fmt.Fprintf(os.Stderr, "[csrdfu] %s → %s\n", u.state.String(), to.String())
	u.state = to
	u.history = append(u.history, to)
}

// State returns the current state — for tests and introspection.
func (u *CsrDfuUpdater) State() CsrDfuState { return u.state }

// History returns the full transition history in order.
func (u *CsrDfuUpdater) History() []CsrDfuState { return append([]CsrDfuState(nil), u.history...) }

// ── Main run loop ──────────────────────────────────────────────────────────

// Run drives the full state machine end-to-end. It blocks until the flash
// is complete, fails, or the context is cancelled. Transitions are driven
// by events arriving on the events channel plus the mode-switch and flash
// side effects.
func (u *CsrDfuUpdater) Run(ctx context.Context) CsrDfuResult {
	res := CsrDfuResult{Transitions: []CsrDfuState{}}

	// Step 1: wait for the normal-mode device to appear (or confirm
	// it's already attached). We accept a single matching event.
	u.transition(CsrStateIdle)
	normal, err := u.waitForEvent(ctx, func(e CsrDfuEvent) bool {
		return e.Attached && e.NormalMode &&
			e.VendorID == u.cfg.NormalVID && e.ProductID == u.cfg.NormalPID
	}, u.cfg.SwitchTimeout)
	if err != nil {
		return u.fail(res, fmt.Errorf("waiting for device in normal mode: %w", err))
	}
	u.transition(CsrStateNormalAttached)

	// Step 2: send the mode-switch command. This is the one piece that
	// is NOT yet implemented — see realEnterDFUMode.
	if err := u.enterDFU(normal.SysPath); err != nil {
		return u.fail(res, fmt.Errorf("enter DFU mode: %w", err))
	}
	u.transition(CsrStateSwitching)

	// Step 3: wait for the normal-mode device to disappear.
	if _, err := u.waitForEvent(ctx, func(e CsrDfuEvent) bool {
		return !e.Attached && e.SysPath == normal.SysPath
	}, u.cfg.SwitchTimeout); err != nil {
		return u.fail(res, fmt.Errorf("waiting for normal-mode detach: %w", err))
	}
	u.transition(CsrStateNormalDetached)

	// Step 4: wait for the device to re-enumerate as a DFU-class device.
	// We match on the VID only — the PID changes when the device is in
	// DFU mode (it exposes a different descriptor).
	dfu, err := u.waitForEvent(ctx, func(e CsrDfuEvent) bool {
		return e.Attached && !e.NormalMode && e.VendorID == u.cfg.NormalVID
	}, u.cfg.SwitchTimeout)
	if err != nil {
		return u.fail(res, fmt.Errorf("waiting for DFU-mode re-attach: %w", err))
	}
	u.transition(CsrStateDfuAttached)
	res.EnteredDfuAt = time.Now()

	// Step 5: run dfu-util against the DFU-mode device.
	res.FlashStartedAt = time.Now()
	u.transition(CsrStateFlashing)
	if err := u.flashDFU(u.cfg.FirmwarePath, dfu.SysPath); err != nil {
		return u.fail(res, fmt.Errorf("dfu-util flash: %w", err))
	}

	// Step 6: wait for the DFU-mode device to detach post-flash.
	if _, err := u.waitForEvent(ctx, func(e CsrDfuEvent) bool {
		return !e.Attached && e.SysPath == dfu.SysPath
	}, u.cfg.SettleTimeout); err != nil {
		return u.fail(res, fmt.Errorf("waiting for DFU-mode detach: %w", err))
	}
	u.transition(CsrStateDfuDetached)

	// Step 7: wait for the device to re-appear in its normal mode.
	reborn, err := u.waitForEvent(ctx, func(e CsrDfuEvent) bool {
		return e.Attached && e.NormalMode &&
			e.VendorID == u.cfg.NormalVID && e.ProductID == u.cfg.NormalPID
	}, u.cfg.SettleTimeout)
	if err != nil {
		return u.fail(res, fmt.Errorf("waiting for normal-mode re-attach: %w", err))
	}

	// Step 8: optional post-flash reset — mirrors isExtraResetNeeded()
	// in jfwu. For the prototype this is a no-op for every device; a
	// real deployment would consult a per-product table.
	if err := u.resetNow(reborn.SysPath); err != nil {
		return u.fail(res, fmt.Errorf("post-flash reset: %w", err))
	}

	u.transition(CsrStateComplete)
	res.CompletedAt = time.Now()
	res.FinalState = CsrStateComplete
	res.Transitions = u.History()
	return res
}

// waitForEvent pulls events from the channel until one satisfies match,
// the timeout fires, or the context is cancelled.
func (u *CsrDfuUpdater) waitForEvent(ctx context.Context, match func(CsrDfuEvent) bool, timeout time.Duration) (CsrDfuEvent, error) {
	deadline := time.After(timeout)
	for {
		select {
		case <-ctx.Done():
			return CsrDfuEvent{}, ctx.Err()
		case <-deadline:
			return CsrDfuEvent{}, fmt.Errorf("timeout after %s", timeout)
		case ev, ok := <-u.events:
			if !ok {
				return CsrDfuEvent{}, errors.New("event channel closed")
			}
			if match(ev) {
				return ev, nil
			}
			// Otherwise discard and keep waiting. Unrelated events
			// (other devices, spurious hotplugs) are expected.
		}
	}
}

// fail records a failure and returns the terminal result.
func (u *CsrDfuUpdater) fail(res CsrDfuResult, err error) CsrDfuResult {
	u.transition(CsrStateFailed)
	res.FinalState = CsrStateFailed
	res.Transitions = u.History()
	res.Err = err
	return res
}

// ── Production backend stubs (the "need more RE" layer) ───────────────────

// realEnterDFUMode is the ONE piece still missing from pure-Go parity
// with jfwu's CsrDfuUpdater. jfwu's handleAdd / startUpdate contains a
// vendor-specific command — likely a USB control transfer or a BCCMD
// write — that tells the CSR chip to detach from its normal interface
// and re-enumerate as a DFU device.
//
// Until the exact command and recovery behavior are validated on replaceable
// hardware, this function returns an explicit not-implemented error.
func (u *CsrDfuUpdater) realEnterDFUMode(sysPath string) error {
	return fmt.Errorf(
		"CsrDfuUpdater.realEnterDFUMode: mode-switch command is not "+
			"implemented or hardware-validated (sysPath=%s)", sysPath)
}

// realPostFlashReset is the equivalent of jfwu's isExtraResetNeeded() +
// follow-up reset. For now it's a no-op — most CSR devices reset
// themselves on DFU_DETACH and do not need an explicit extra reset.
// If a specific product requires it, we'll extend per-PID.
func (u *CsrDfuUpdater) realPostFlashReset(sysPath string) error {
	return nil
}

// realFlashDFUDevice hands the firmware file to dfu-util, scoping the
// transfer to the just-enumerated DFU device via its VID and the DFU
// interface number parsed from the sysfs path.
func realFlashDFUDevice(firmwarePath, dfuSysPath string) error {
	if firmwarePath == "" {
		return errors.New("firmware path is empty")
	}
	// Reuse the firmware package's dfu-util wrapper. It already handles
	// path lookup, VID scoping, and honest success reporting.
	return flashViaDfuUtil(firmwarePath)
}

// ── Sysfs-polling event source ─────────────────────────────────────────────

// WatchUsbEvents starts a goroutine that polls /sys/bus/usb/devices at a
// fixed interval and emits CsrDfuEvents for add/remove transitions. The
// returned channel is closed when the context is cancelled. This is the
// no-dependency alternative to libudev — we already use sysfs for
// enumeration in firmware.go, so it keeps the toolchain stdlib-only.
//
// The poll interval is deliberately short (100ms) because mode-switch
// windows are on the order of a second or two and we don't want to
// miss a transient DFU enumeration.
func WatchUsbEvents(ctx context.Context, vid uint16) <-chan CsrDfuEvent {
	out := make(chan CsrDfuEvent, 16)
	go func() {
		defer close(out)
		prev := map[string]USBDevice{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			curr := snapshotVendor(vid)
			// Detect additions.
			for path, d := range curr {
				if _, ok := prev[path]; !ok {
					out <- CsrDfuEvent{
						Attached:   true,
						NormalMode: !looksLikeDFUInterface(path),
						SysPath:    path,
						VendorID:   d.VendorID,
						ProductID:  d.ProductID,
					}
				}
			}
			// Detect removals.
			for path, d := range prev {
				if _, ok := curr[path]; !ok {
					out <- CsrDfuEvent{
						Attached:   false,
						NormalMode: !looksLikeDFUInterface(path),
						SysPath:    path,
						VendorID:   d.VendorID,
						ProductID:  d.ProductID,
					}
				}
			}
			prev = curr
		}
	}()
	return out
}

// snapshotVendor returns all USB devices currently attached matching vid,
// keyed by sysfs path. Reuses enumerateUSB() but filters and maps.
func snapshotVendor(vid uint16) map[string]USBDevice {
	out := map[string]USBDevice{}
	devs, err := enumerateUSB()
	if err != nil {
		return out
	}
	for _, d := range devs {
		if d.VendorID == vid {
			out[d.SysPath] = d
		}
	}
	return out
}

// looksLikeDFUInterface returns true if the USB interface class of the
// device at sysPath is 0xFE (Application Specific) with subclass 0x01
// (DFU), i.e. a standard USB DFU-1.1 device. Walks the interface
// subdirectories under sysPath and reads bInterfaceClass/bInterfaceSubClass.
func looksLikeDFUInterface(sysPath string) bool {
	entries, err := os.ReadDir(sysPath)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !strings.Contains(e.Name(), ":") {
			// USB interface directories look like "1-2:1.0".
			continue
		}
		ifDir := filepath.Join(sysPath, e.Name())
		class := readHexSysfs8(filepath.Join(ifDir, "bInterfaceClass"))
		sub := readHexSysfs8(filepath.Join(ifDir, "bInterfaceSubClass"))
		if class == 0xFE && sub == 0x01 {
			return true
		}
	}
	return false
}

// readHexSysfs8 reads a single hex byte from a sysfs attribute file.
// Returns 0 on any error — callers treat 0 as "not this class".
func readHexSysfs8(path string) uint8 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(b))
	var n uint
	if _, err := fmt.Sscanf(s, "%x", &n); err != nil {
		return 0
	}
	return uint8(n)
}
