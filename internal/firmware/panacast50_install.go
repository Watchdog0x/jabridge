package firmware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type panacast50Recovery struct {
	Method           string `json:"method"`
	RebootFrom       string `json:"rebootFrom,omitempty"`
	ProgramFrom      string `json:"programFrom,omitempty"`
	ProgramReplugged bool   `json:"programReplugged,omitempty"`
	RetryAfterReboot bool   `json:"retryAfterReboot,omitempty"`
}

func validPanaCast50Recovery(state firmwareRecoveryState) error {
	p := state.PanaCast50
	targets, err := parseTargetPIDs(state.TargetUSBPIDs)
	if p == nil || !panacast50RuntimePID(state.RuntimePID) || state.BootPID != 0x3010 || state.USBPort == "" || err != nil || len(targets) != 1 || targets[0] != 0x3010 || state.ControllerIdentitySHA256 != "" {
		return errors.New("invalid PanaCast 50 recovery target")
	}
	if p.Method != "mass" && p.Method != "gnp" || p.Method == "mass" && state.Protocol != 10 || p.Method == "gnp" && state.Protocol != 13 {
		return errors.New("invalid PanaCast 50 recovery transport")
	}
	for index, value := range []string{state.TargetIdentitySHA256, state.USBSerialSHA256, p.RebootFrom, p.ProgramFrom} {
		if index > 0 && value == "" {
			continue
		}
		data, err := hex.DecodeString(value)
		if err != nil || len(data) != sha256.Size {
			return errors.New("invalid PanaCast 50 recovery identity")
		}
	}
	switch state.Phase {
	case "ready", "staging":
	case "entering-mass":
		if p.Method != "mass" || p.RebootFrom == "" {
			return errors.New("invalid camera storage transition")
		}
	case "activating", "programming", "exiting", "verifying", "failed":
		if p.ProgramFrom == "" {
			return errors.New("camera programming intent is missing")
		}
	case "rebooting":
		if p.RebootFrom == "" || p.ProgramFrom == "" {
			return errors.New("camera reboot intent is missing")
		}
	default:
		return errors.New("invalid PanaCast 50 recovery phase")
	}
	return nil
}

func bindPanaCast50Recovery(state *firmwareRecoveryState, device USBDevice, info panacast50Info) error {
	if state == nil || state.PanaCast50 == nil || device.attachment == nil || device.VendorID != JabraVendorID || device.ViaDongle || info.Identity.PID != device.ProductID || info.Identity.Port != device.SysPath || info.Identity.Serial == "" {
		return errors.New("PanaCast 50 target is not bound")
	}
	if device.ProductID != state.RuntimePID && device.ProductID != 0x3010 {
		return errors.New("PanaCast 50 runtime model changed")
	}
	if filepath.Base(device.SysPath) != state.USBPort || panacast50IdentityHash(info.Identity, state.RuntimePID) != state.TargetIdentitySHA256 {
		return errors.New("PanaCast 50 serial identity or USB port changed")
	}
	if device.ProductID == state.RuntimePID && state.USBSerialSHA256 != "" && fmt.Sprintf("%x", sha256.Sum256([]byte(device.Serial))) != state.USBSerialSHA256 {
		return errors.New("PanaCast 50 runtime USB serial changed")
	}
	return nil
}

func panaCast50InitialReady(info panacast50Info) error {
	if !info.HaveFWState {
		return nil
	} // Explicitly unsupported by older firmware.
	if info.FWState == 16 || info.FWState == 0 && info.FWDetail == 0 {
		return nil
	}
	if info.FWState == 13 && info.HaveVideoState && info.VideoState == 0 {
		return nil
	}
	return fmt.Errorf("PanaCast 50 is not ready for a new update (state %d, detail %d)", info.FWState, info.FWDetail)
}

func panaCast50ComponentsReady(info panacast50Info) bool {
	if !info.HaveFWState && !info.HaveVideoState {
		return false
	}
	if info.HaveVideoState && info.VideoState != 0 {
		return false
	}
	return !info.HaveFWState || info.FWState == 16 || info.FWState == 0 && info.FWDetail == 0 || info.FWState == 13 && info.HaveVideoState
}

func runPanaCast50Install(ctx context.Context, backend panacast50Backend, device USBDevice, archive *panacast50Archive, state *firmwareRecoveryState, save func() error, progress func(string, int64, int64)) error {
	if backend == nil || archive == nil || archive.Manifest == nil || state == nil || save == nil {
		return errors.New("incomplete PanaCast 50 installation plan")
	}
	info, err := backend.Inspect(ctx, device)
	if err != nil {
		return err
	}
	if state.PanaCast50 == nil && info.HaveFWState && info.FWState == 13 {
		waiting, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		identity := info.Identity.hash()
		for !info.HaveVideoState || info.VideoState != 0 {
			if progress != nil {
				progress("Waiting for the camera to finish starting", 0, 0)
			}
			if err := backend.Sleep(waiting, time.Second); err != nil {
				return err
			}
			latest, err := backend.Inspect(waiting, device)
			if err != nil {
				return err
			}
			if latest.Identity.hash() != identity {
				return errors.New("camera changed while waiting for startup")
			}
			info = latest
			if info.HaveFWState && info.FWState != 13 {
				break
			}
		}
	}
	if state.PanaCast50 == nil {
		if state.Protocol != 0 || !panacast50RuntimePID(device.ProductID) || device.attachment == nil || device.ViaDongle || device.VendorID != JabraVendorID || info.Identity.Serial == "" || info.Identity.PID != device.ProductID || info.Identity.Port != device.SysPath {
			return errors.New("a new PanaCast 50 update must start from its normal USB mode")
		}
		if err := panaCast50InitialReady(info); err != nil {
			return err
		}
		if compareVersions(info.Identity.Version, archive.Manifest.Version) > 0 {
			return errors.New("PanaCast 50 firmware is newer than this archive; refusing a downgrade")
		}
		fileMode, err := backend.FileTransport(ctx, device, info.Identity)
		if err != nil {
			return err
		}
		method, protocol := "gnp", 13
		if !fileMode {
			method, protocol = "mass", 10
			if err := backend.StorageReady(ctx); err != nil {
				return err
			}
		}
		state.Protocol, state.RuntimePID, state.BootPID = protocol, device.ProductID, 0x3010
		state.USBPort = filepath.Base(device.SysPath)
		state.TargetIdentitySHA256 = panacast50IdentityHash(info.Identity, state.RuntimePID)
		if device.Serial != "" {
			state.USBSerialSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(device.Serial)))
		}
		state.PanaCast50 = &panacast50Recovery{Method: method}
		state.Phase = "ready"
	}
	if err := validPanaCast50Recovery(*state); err != nil {
		return err
	}
	if err := bindPanaCast50Recovery(state, device, info); err != nil {
		return err
	}
	p := state.PanaCast50
	checkpoint := func(phase string) error { state.Phase = phase; return save() }
	report := func(stage string, done, total int64) {
		if progress != nil {
			progress(stage, done, total)
		}
	}
	refresh := func(check context.Context) error {
		current, err := backend.Current(check, device)
		if err != nil {
			return err
		}
		latest, err := backend.Inspect(check, current)
		if err != nil {
			return err
		}
		if err := bindPanaCast50Recovery(state, current, latest); err != nil {
			return err
		}
		device, info = current, latest
		return nil
	}
	waitForChange := func(from string, timeout time.Duration) error {
		wait, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var last error
		for {
			current, err := backend.Current(wait, device)
			if err == nil && current.attachment.fingerprint != from {
				latest, readErr := backend.Inspect(wait, current)
				if readErr == nil {
					if err := bindPanaCast50Recovery(state, current, latest); err != nil {
						return err
					}
					device, info = current, latest
					return nil
				}
				err = readErr
			}
			last = err
			if err := backend.Sleep(wait, 250*time.Millisecond); err != nil {
				return fmt.Errorf("camera did not return on its original USB port: %v: %w", last, err)
			}
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch state.Phase {
		case "ready":
			if device.ProductID != state.RuntimePID {
				return errors.New("camera must return to normal mode before starting this update")
			}
			if err := panaCast50InitialReady(info); err != nil {
				return err
			}
			if compareVersions(info.Identity.Version, archive.Manifest.Version) > 0 {
				return errors.New("PanaCast 50 firmware is newer than this file; refusing a downgrade")
			}
			if err := backend.Busy(ctx, device, info.Identity); err != nil {
				return err
			}
			if p.Method == "gnp" {
				if err := checkpoint("staging"); err != nil {
					return err
				}
				continue
			}
			p.RebootFrom = device.attachment.fingerprint
			if err := checkpoint("entering-mass"); err != nil {
				return err
			}
			if err := backend.Command(ctx, device, info.Identity, 0); err != nil {
				if errors.Is(err, errCameraCommandNotStarted) {
					p.RebootFrom = ""
					if saveErr := checkpoint("ready"); saveErr != nil {
						return errors.Join(err, saveErr)
					}
				}
				return fmt.Errorf("camera storage transition was not confirmed; retry this file to continue: %w", err)
			}
		case "entering-mass":
			report("Waiting for camera storage", 0, 0)
			if err := waitForChange(p.RebootFrom, 3*time.Minute); err != nil {
				return err
			}
			if device.ProductID != 0x3010 {
				return errors.New("PanaCast 50 did not enter storage mode")
			}
			p.RebootFrom = ""
			if err := checkpoint("staging"); err != nil {
				return err
			}
		case "staging":
			if p.Method == "mass" && device.ProductID != 0x3010 || p.Method == "gnp" && device.ProductID != state.RuntimePID {
				return errors.New("camera USB mode changed before file staging")
			}
			if device.ProductID == state.RuntimePID && compareVersions(info.Identity.Version, archive.Manifest.Version) > 0 {
				return errors.New("PanaCast 50 firmware is newer than this file; refusing a downgrade")
			}
			if err := checkpoint("staging"); err != nil {
				return err
			}
			if err := backend.Stage(ctx, device, info.Identity, archive, p.Method, func(done, total int64) { report("Sending PanaCast 50 firmware", done, total) }); err != nil {
				return err
			}
			if err := backend.Busy(ctx, device, info.Identity); err != nil {
				return err
			}
			p.ProgramFrom = device.attachment.fingerprint
			p.ProgramReplugged = false
			if err := checkpoint("activating"); err != nil {
				return err
			}
			step := byte(2)
			if p.Method == "mass" {
				step = 1
			}
			if err := backend.Command(ctx, device, info.Identity, step); err != nil {
				if errors.Is(err, errCameraCommandNotStarted) {
					p.ProgramFrom = ""
					if saveErr := checkpoint("staging"); saveErr != nil {
						return errors.Join(err, saveErr)
					}
				}
				return fmt.Errorf("camera activation was not confirmed; retry this file to continue: %w", err)
			}
			if err := checkpoint("programming"); err != nil {
				return err
			}
		case "activating", "programming":
			report("Waiting for PanaCast 50 to update", 0, 0)
			if !p.ProgramReplugged {
				if err := waitForChange(p.ProgramFrom, 3*time.Minute); err != nil {
					return err
				}
				p.ProgramReplugged = true
				if err := checkpoint("programming"); err != nil {
					return err
				}
			}
			monitor, cancel := context.WithTimeout(ctx, 20*time.Minute)
			for {
				if info.HaveFWState && info.FWState == 9 {
					cancel()
					if err := checkpoint("failed"); err != nil {
						return err
					}
					return fmt.Errorf("PanaCast 50 reported a component update failure (%d); retry this file for recovery", info.FWDetail)
				}
				ready := info.HaveVideoState && info.VideoState == 0
				if info.HaveFWState && info.FWState != 0 && info.FWState != 7 && info.FWState != 16 {
					ready = false
				}
				if info.HaveFWState && info.FWState == 0 && info.FWDetail != 0 {
					ready = false
				}
				if info.HaveFWState && info.FWState == 16 && !info.HaveVideoState {
					ready = true
				}
				if info.Identity.PID == 0x3010 && info.HaveFWState && info.FWState == 7 && info.HaveVideoState && info.VideoState == 0xf0 && compareVersions(info.Identity.Version, "0.24.0") >= 0 {
					ready = true
				}
				if ready {
					cancel()
					next := "exiting"
					if device.ProductID == state.RuntimePID && compareVersions(info.Identity.Version, archive.Manifest.Version) == 0 && (!info.HaveFWState || info.FWState != 7) {
						next = "verifying"
					}
					if err := checkpoint(next); err != nil {
						return err
					}
					break
				}
				if err := backend.Sleep(monitor, time.Second); err != nil {
					cancel()
					return fmt.Errorf("PanaCast 50 component update did not finish: %w", err)
				}
				current, err := backend.Current(monitor, device)
				if err != nil {
					if errors.Is(err, errPanaCast50Absent) {
						continue
					}
					cancel()
					return err
				}
				latest, err := backend.Inspect(monitor, current)
				if err != nil {
					continue
				}
				if err := bindPanaCast50Recovery(state, current, latest); err != nil {
					cancel()
					return err
				}
				device, info = current, latest
			}
		case "failed":
			p.RetryAfterReboot = true
			if err := checkpoint("exiting"); err != nil {
				return err
			}
		case "exiting":
			if err := backend.Permission(ctx, device, info.Identity); err != nil {
				return err
			}
			p.RebootFrom = device.attachment.fingerprint
			if err := checkpoint("rebooting"); err != nil {
				return err
			}
			if err := backend.Reset(ctx, device, info.Identity); err != nil {
				if errors.Is(err, errCameraCommandNotStarted) {
					p.RebootFrom = ""
					if saveErr := checkpoint("exiting"); saveErr != nil {
						return errors.Join(err, saveErr)
					}
				}
				return fmt.Errorf("camera final restart was not confirmed; retry this file to continue: %w", err)
			}
		case "rebooting":
			if err := waitForChange(p.RebootFrom, 3*time.Minute); err != nil {
				return err
			}
			if device.ProductID != state.RuntimePID {
				return errors.New("PanaCast 50 did not return to its original runtime model")
			}
			p.RebootFrom = ""
			if p.RetryAfterReboot {
				p.RetryAfterReboot = false
				p.ProgramFrom = ""
				p.ProgramReplugged = false
				if err := checkpoint("ready"); err != nil {
					return err
				}
			} else if err := checkpoint("verifying"); err != nil {
				return err
			}
		case "verifying":
			ready, cancel := context.WithTimeout(ctx, 90*time.Second)
			for {
				if err := refresh(ready); err != nil {
					cancel()
					return err
				}
				if device.ProductID != state.RuntimePID {
					cancel()
					return errors.New("PanaCast 50 did not return to its original USB model")
				}
				if info.HaveFWState && info.FWState == 9 {
					cancel()
					if err := checkpoint("failed"); err != nil {
						return err
					}
					return errors.New("PanaCast 50 reported an update failure after restart")
				}
				if panaCast50ComponentsReady(info) {
					break
				}
				if err := backend.Sleep(ready, time.Second); err != nil {
					cancel()
					return fmt.Errorf("PanaCast 50 components did not become ready: %w", err)
				}
			}
			cancel()
			if compareVersions(info.Identity.Version, archive.Manifest.Version) != 0 {
				if compareVersions(info.Identity.Version, archive.Manifest.Version) < 0 {
					p.ProgramFrom = ""
					p.ProgramReplugged = false
					if err := checkpoint("ready"); err != nil {
						return err
					}
				}
				return errors.New("PanaCast 50 did not keep the expected firmware; retry this file to transfer it again")
			}
			return nil
		default:
			return errors.New("unknown PanaCast 50 recovery phase")
		}
	}
}

func verifyPanaCast50Release(ctx context.Context, path string, archive *panacast50Archive, pid uint16) error {
	if archive == nil || archive.Manifest == nil || !panacast50RuntimePID(pid) {
		return errors.New("not a matching PanaCast 50 release target")
	}
	evidence, err := firmwareModelCatalog.FirmwareRelease(ctx, pid, archive.Manifest.Version)
	if err != nil {
		return err
	}
	if evidence == nil {
		return errors.New("PanaCast 50 release metadata is unavailable")
	}
	checksum, err := firmwareFileMD5(path)
	if err != nil {
		return err
	}
	known := false
	for _, protocol := range evidence.FirmwareProtocols {
		if protocol == 10 || protocol == 13 {
			known = true
		}
	}
	if !firmwareReleaseMatchesDevice(checksum, pid, evidence) || evidence.HasUnspecifiedFirmwareProtocol || !known {
		return errors.New("PanaCast 50 firmware requires matching official metadata and checksum")
	}
	return nil
}

func installPanaCast50Checked(snapshot *firmwareSnapshot, accepted bool, validateTarget func() error, preferredPID uint16) error {
	archive, err := loadPanaCast50Archive(snapshot.path)
	if err != nil {
		return err
	}
	prepared, err := prepareFirmwareTransfer(snapshot.path, archive.Manifest)
	if err != nil {
		return err
	}
	state := prepared.State
	if prepared.Recovery {
		previous, err := loadFirmwareRecoveryState()
		if err != nil {
			return err
		}
		if err := validPanaCast50Recovery(previous); err != nil {
			return err
		}
		previous.Attempt = state.Attempt
		state = previous
	}
	devices, err := enumerateBoundUSB()
	if err != nil {
		return err
	}
	var matches []USBDevice
	for _, device := range devices {
		if device.VendorID != JabraVendorID || device.ViaDongle || !panacast50ModePID(device.ProductID) {
			continue
		}
		if prepared.Recovery {
			if filepath.Base(device.SysPath) != state.USBPort || device.ProductID != state.RuntimePID && device.ProductID != state.BootPID {
				continue
			}
		} else if device.ProductID == 0x3010 {
			continue
		}
		if preferredPID != 0 && preferredPID != device.ProductID {
			continue
		}
		matches = append(matches, device)
	}
	if len(matches) != 1 {
		return fmt.Errorf("connect exactly one matching PanaCast 50 to its original USB port; found %d", len(matches))
	}
	device := matches[0]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 120*time.Minute)
	defer cancel()
	pid := device.ProductID
	if prepared.Recovery {
		pid = state.RuntimePID
	}
	metadata, finish := context.WithTimeout(ctx, MetadataTimeout)
	err = verifyPanaCast50Release(metadata, snapshot.path, archive, pid)
	finish()
	if err != nil {
		return err
	}
	if !accepted {
		word := "INSTALL"
		if prepared.Recovery {
			word = "RECOVER"
		}
		fmt.Fprintf(os.Stderr, "Firmware: PanaCast 50 %s\nKeep USB and power connected. Close camera apps, end calls and close other Jabra tools.\n", archive.Manifest.Version)
		if !confirmFirmwareAction(os.Stdin, os.Stderr, word) {
			return errors.New("PanaCast 50 firmware install cancelled")
		}
	}
	if validateTarget != nil {
		if err := validateTarget(); err != nil {
			return err
		}
	}
	commandLineRiskAccepted.Store(true)
	defer commandLineRiskAccepted.Store(false)
	last := ""
	err = runPanaCast50Install(ctx, nativePanaCast50Backend{}, device, archive, &state, func() error { return saveFirmwareRecoveryState(state) }, func(stage string, done, total int64) {
		text := stage
		if total > 0 {
			text = fmt.Sprintf("%s: %d%%", stage, done*100/total)
		}
		if text != last {
			fmt.Fprintln(os.Stderr, text)
			last = text
		}
	})
	if err != nil {
		return fmt.Errorf("PanaCast 50 update stopped; keep this exact file and USB port for recovery: %w", err)
	}
	if err := clearFirmwareRecoveryState(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "PanaCast 50 firmware %s is confirmed on the same camera.\n", archive.Manifest.Version)
	return nil
}
