package firmware

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// Engage75Compatibility is intentionally disabled in ordinary builds. A reviewed
// trial artifact must explicitly enable it at link time. It still needs both
// the preview CLI option and the existing explicit firmware-risk flag.
var Engage75Compatibility = "disabled"

const engage75PreviewFlag = "--preview-engage75-legacy"
const engage75NewAttemptFlag = "--new-attempt"

type engage75PreviewOptions struct{ preview, newAttempt bool }

func extractEngage75Preview(args []string) ([]string, bool, error) {
	remaining, options, err := extractEngage75Options(args)
	return remaining, options.preview, err
}

func extractEngage75Options(args []string) ([]string, engage75PreviewOptions, error) {
	var remaining []string
	options := engage75PreviewOptions{}
	for _, arg := range args {
		if arg == engage75NewAttemptFlag {
			if options.newAttempt {
				return nil, options, errors.New("new attempt option was repeated")
			}
			options.newAttempt = true
			continue
		}
		if arg != engage75PreviewFlag {
			remaining = append(remaining, arg)
			continue
		}
		if options.preview {
			return nil, options, errors.New("engage 75 preview option was repeated")
		}
		options.preview = true
	}
	if options.newAttempt && !options.preview {
		return nil, options, errors.New("new attempt requires the Engage 75 preview option")
	}
	if options.preview {
		_, accepted, err := parseInstallArgs(remaining)
		if err != nil {
			return nil, options, err
		}
		if Engage75Compatibility != "enabled" {
			return nil, options, errors.New("engage 75 compatibility preview is disabled in this build")
		}
		if !accepted {
			return nil, options, fmt.Errorf("engage 75 preview also requires %s", HardwareWriteFlag)
		}
	}
	return remaining, options, nil
}

type engage75PreviewBackend interface {
	sitelDECTBackend
	previewEnter(context.Context, USBDevice) error
	previewVerifyRuntime(context.Context, USBDevice, engage75SelectedIdentity, *sitelDECTArchive) error
}

type engage75PreviewStore struct {
	pending   func(engage75Pending) error
	recovery  func(firmwareRecoveryState) error
	previous  *engage75Pending
	supersede func(engage75Pending, engage75Pending) error
}

func bindEngage75Transition(previous, next USBDevice, selected engage75SelectedIdentity, pid uint16) error {
	stage := "update-mode reconnect"
	if pid == selected.RuntimePID {
		stage = "normal-mode reconnect"
	}
	fail := func(reason string) error { return fmt.Errorf("%s: %s", stage, reason) }
	if selected.validate() != nil {
		return fail("invalid original Engage 75 identity")
	}
	if previous.attachment == nil || next.attachment == nil || previous.attachment.fingerprint == "" || next.attachment.fingerprint == "" {
		return fail("the base USB connection could not be identified")
	}
	if previous.attachment.fingerprint == next.attachment.fingerprint {
		return fail("the base did not restart into a new USB connection")
	}
	if previous.SysPath != next.SysPath || filepath.Base(next.SysPath) != selected.USBPort || previous.attachment.realPath == "" || previous.attachment.realPath != next.attachment.realPath || previous.attachment.bus < 1 || previous.attachment.bus != next.attachment.bus {
		return fail("the base came back on a different USB port")
	}
	if next.VendorID != JabraVendorID || next.ViaDongle || next.ProductID != pid || pid != selected.BootPID && pid != selected.RuntimePID {
		return fail(fmt.Sprintf("a different USB device appeared (product ID 0x%04x)", next.ProductID))
	}
	// The loader may omit iSerialNumber. Keep the original serial for final
	// runtime verification; never invent a loader serial or accept a mismatch.
	if next.Serial != "" && engage75Digest(next.Serial) != selected.USBSerialSHA256 {
		return fail("the base came back with a different USB serial")
	}
	if pid == selected.RuntimePID && next.Serial == "" {
		return fail("the base came back without its USB serial")
	}
	return nil
}

// A process restart deliberately cannot resume this executor from a pending or
// V2 record: the original docked-headset continuity is no longer live. Saving a
// serial does not prove an unobservable same-model swap has not happened. Even
// within one execution, the user must leave the headset docked throughout.
func runEngage75Preview(ctx context.Context, backend engage75PreviewBackend, device USBDevice, archive *sitelDECTArchive, digest string, store engage75PreviewStore, progress func(byte, int, int)) (resultErr error) {
	if Engage75Compatibility != "enabled" || requireHardwareWrites() != nil || backend == nil || store.pending == nil || store.recovery == nil || !validEngage75Digest(digest) || archive == nil || archive.Engage75 == nil || archive.Manifest.ProductName != "Jabra_Engage_75" || archive.Manifest.Version != "5.20.1" {
		return errors.New("engage 75 preview is not authorized or its plan is incomplete")
	}
	if err := archive.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := backend.runtime(ctx, device, archive)
	var incomplete *dectRuntimeAssessmentError
	if !errors.As(err, &incomplete) {
		return errors.New("engage 75 preview requires the recognized old-firmware assessment")
	}
	selected, err := engage75Selection(incomplete.Assessment, device)
	if err != nil {
		return err
	}
	for _, image := range archive.Images {
		if compareVersions(dectComponentVersion(incomplete.Assessment.Identity, image.File), image.File.Version) > 0 {
			return errors.New("engage 75 preview refuses a component downgrade")
		}
	}
	if compareVersions(incomplete.Assessment.Identity.RadioVersion, archive.Manifest.Version) > 0 {
		return errors.New("engage 75 preview refuses a radio downgrade")
	}
	p := engage75Pending{Version: 2, Attempt: 1, LastPhase: "pending-inspection", BootSerial: "not-observed", Purpose: "engage75-pending-inspection", ArchiveSHA256: digest, Phase: "pending-inspection", Selected: selected, Entry: "not-sent", HeadReturn: "not-sent", BaseReturn: "not-sent"}
	if store.previous != nil {
		if err := validateEngage75NewAttempt(*store.previous, digest, selected); err != nil {
			return err
		}
		if store.supersede == nil {
			return errors.New("new attempt persistence is unavailable")
		}
		p.Attempt = store.previous.attemptNumber() + 1
	}
	var state *firmwareRecoveryState
	started := false
	defer func() {
		if resultErr == nil || !started {
			return
		}
		p.Unresolved = true
		p.advance("unresolved")
		// A not-sent failure before entry keeps a pending inspection, not a false
		// claim that any hardware command was sent.

		if err := store.pending(p); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("save Engage 75 unresolved inspection: %w", err))
		}
		if state != nil {
			state.Phase = "unresolved"
			if err := store.recovery(*state); err != nil {
				resultErr = errors.Join(resultErr, err)
			}
		}
		resultErr = fmt.Errorf("engage 75 preview stopped; keep the archive and leave the base connected and headset docked. The base restarts and reconnects during the update. Automatic retry is disabled: %w", resultErr)
	}()
	// A fresh second assessment detects an observable swap before committing
	// entry intent. The tiny interval after this read is a documented limitation.
	_, err = backend.runtime(ctx, device, archive)
	incomplete = nil
	if !errors.As(err, &incomplete) {
		return errors.New("engage 75 original runtime changed before entry")
	}
	fresh, err := engage75Selection(incomplete.Assessment, device)
	if err != nil || fresh != selected {
		return errors.New("engage 75 selected runtime identity changed before entry")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if store.previous != nil {
		if err := store.supersede(*store.previous, p); err != nil {
			return err
		}
	} else if err := store.pending(p); err != nil {
		return err
	}
	started = true
	p.advance("entry-intent")
	p.Entry = "intent"
	if err := store.pending(p); err != nil {
		return err
	}
	err = backend.previewEnter(ctx, device)
	switch {
	case err == nil:
		p.Entry = "replied"
	case errors.Is(err, errManagementNotSent), errors.Is(err, errSitelRejected):
		// A NAK is an explicit rejection, distinct from an unsent request
		// or an uncertain transport failure.
		if errors.Is(err, errManagementNotSent) {
			p.Entry = "not-sent"
		} else {
			p.Entry = "rejected"
		}
		return err
	default:
		p.Entry = "possibly-sent"
	}
	if saveErr := store.pending(p); saveErr != nil {
		return saveErr
	}
	// Ambiguous entry is observed once; entry itself is never retried.
	if err := ctx.Err(); err != nil {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, 45*time.Second)
	boot, err := backend.wait(wait, device, 0x1111)
	cancel()
	if err != nil {
		return fmt.Errorf("waiting for the base to reconnect in update mode: %w", err)
	}
	if err := bindEngage75Transition(device, boot, selected, 0x1111); err != nil {
		return err
	}
	p.BootSerial = "matched"
	if boot.Serial == "" {
		p.BootSerial = "absent"
	}
	p.advance("bound-boot-attachment")
	if err := store.pending(p); err != nil {
		return err
	}
	connection, err := backend.boot(ctx, boot)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = connection.Close()
		}
	}()
	observations, err := collectEngage75ImageInfo(ctx, connection)
	if err != nil {
		return err
	}
	images := make([]engage75MeasuredImage, 3)
	for n, observed := range observations {
		images[n] = engage75MeasuredImage{Address: observed.Address, ImageID: observed.Info.ID, ReportedMode: observed.Info.Mode}
	}
	// Reported mode0 is the specifically supported loader state; Main1 children
	// may enter their other program. Numeric modes2/3 are not loader permission.
	if !validEngage75Images(images) {
		return errors.New("engage 75 preview observed an unsupported program mode")
	}
	p.advance("images-verified")
	if err := store.pending(p); err != nil {
		return err
	}
	state = &firmwareRecoveryState{FormatVersion: firmwareRecoveryStateVersion, ArchiveSHA256: digest, ProductName: archive.Manifest.ProductName, FirmwareVersion: archive.Manifest.Version, TargetUSBPIDs: []string{"0x1111"}, Attempt: 1, StartedAt: time.Now().UTC().Format(time.RFC3339), USBPort: selected.USBPort, USBSerialSHA256: selected.USBSerialSHA256, Protocol: 4, RuntimePID: selected.RuntimePID, BootPID: selected.BootPID, Phase: "images-verified", SitelDECTV2: &engage75RecoveryV2{Version: 2, Selected: selected, Images: images}}
	save := func() error {
		if err := validEngage75RecoveryV2(*state); err != nil {
			return err
		}
		return store.recovery(*state)
	}
	if err := save(); err != nil {
		return err
	}
	p.advance("recovery-bound")
	if err := store.pending(p); err != nil {
		return err
	}
	// SPI and all child operations also require a current loader base. Latch
	// binding loss so a temporary disappearance cannot renew this authority.
	originalValidate := connection.Validate
	lost := false
	connection.Validate = func() error {
		if lost {
			return ErrDECTInspectionChanged
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if originalValidate != nil {
			if err := originalValidate(); err != nil {
				lost = true
				return err
			}
		}
		probe, finish := context.WithTimeout(ctx, 3*time.Second)
		raw, err := connection.Root.request(probe, 0, nil)
		finish()
		if originalValidate != nil {
			if err := originalValidate(); err != nil {
				lost = true
				return err
			}
		}
		if err != nil {
			return err
		}
		info, err := decodeSitelInfo(raw)
		if err != nil || info.ID != 0x0b0e1111 || info.Mode != 0 {
			lost = true
			return errors.New("engage 75 base loader continuity lost")
		}
		return nil
	}
	prepared, err := prepareEngage75Preview(ctx, backend, connection, archive)
	if err != nil {
		return err
	}
	p.advance("transfer")
	state.Phase = "transfer"
	if err := store.pending(p); err != nil {
		return err
	}
	if err := save(); err != nil {
		return err
	}
	if err := transferEngage75Components(ctx, backend, connection, archive, prepared, state, save, progress); err != nil {
		return err
	}
	for _, image := range prepared {
		if err := verifySitelImage(ctx, image.Peer, image.Image, image.Info.SectorSize); err != nil {
			return err
		}
	}
	if !state.SitelDECTV2.SettingsVerified {
		return errors.New("engage 75 radio settings did not verify")
	}
	p.advance("returning")
	state.Phase = "returning"
	if err := store.pending(p); err != nil {
		return err
	}
	if err := save(); err != nil {
		return err
	}
	// Only after successful CRC/settings verification. There is deliberately no
	// guessed pre-erase MMI restoration or blind mode switch on an abort path.
	for _, address := range []byte{10, 1} {
		outcome := &p.HeadReturn
		if address == 1 {
			outcome = &p.BaseReturn
		}
		*outcome = "intent"
		if err := store.pending(p); err != nil {
			return err
		}
		peer := engage75PreviewPeer{next: connection.peer(address), address: address, transitionFrom: 0}
		_, err := peer.request(ctx, 5, []byte{3})
		if err != nil {
			*outcome = "possibly-sent"
			if errors.Is(err, errManagementNotSent) {
				*outcome = "not-sent"
			} else if errors.Is(err, errSitelRejected) {
				*outcome = "rejected"
			}
			if saveErr := store.pending(p); saveErr != nil {
				return errors.Join(err, saveErr)
			}
			if address == 10 || errors.Is(err, errManagementNotSent) || errors.Is(err, errSitelRejected) {
				return err
			}
		} else {
			*outcome = "replied"
		}
		if err := store.pending(p); err != nil {
			return err
		}
		if address == 10 {
			if err := verifyEngage75HeadMain(ctx, connection.peer(10), connection.Validate); err != nil {
				return err
			}
			p.HeadMainVerified = true
			if err := store.pending(p); err != nil {
				return err
			}
		}
	}
	if err := connection.Close(); err != nil {
		return err
	}
	closed = true
	// No further request uses this connection.
	wait, cancel = context.WithTimeout(ctx, 90*time.Second)
	runtime, err := backend.wait(wait, boot, selected.RuntimePID)
	cancel()
	if err != nil {
		return err
	}
	if err := bindEngage75Transition(boot, runtime, selected, selected.RuntimePID); err != nil {
		return err
	}
	if err := backend.previewVerifyRuntime(ctx, runtime, selected, archive); err != nil {
		return err
	}
	p.advance("final-verified")
	state.Phase = "final-verified"
	if err := save(); err != nil {
		return err
	}
	return store.pending(p)
}
