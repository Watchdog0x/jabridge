package firmware

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakePanaCast50Backend struct {
	device                                  USBDevice
	info                                    panacast50Info
	runtimePID                              uint16
	runtimeSerial, wanted                   string
	gnp                                     bool
	actions                                 []string
	commandError                            map[byte]error
	stageError, resetError, permissionError error
	video                                   []byte
	replacement                             bool
	directRuntime                           bool
}

func (b *fakePanaCast50Backend) Inspect(context.Context, USBDevice) (panacast50Info, error) {
	return b.info, nil
}
func (b *fakePanaCast50Backend) FileTransport(context.Context, USBDevice, cameraIdentity) (bool, error) {
	return b.gnp, nil
}
func (b *fakePanaCast50Backend) StorageReady(context.Context) error                    { return nil }
func (b *fakePanaCast50Backend) Busy(context.Context, USBDevice, cameraIdentity) error { return nil }
func (b *fakePanaCast50Backend) Stage(_ context.Context, device USBDevice, _ cameraIdentity, _ *panacast50Archive, method string, _ func(int64, int64)) error {
	if method == "mass" && device.ProductID != 0x3010 || method == "gnp" && device.ProductID != b.runtimePID {
		return errors.New("wrong staging mode")
	}
	b.actions = append(b.actions, "stage-"+method)
	return b.stageError
}
func (b *fakePanaCast50Backend) transition(pid uint16, fingerprint int, version string) {
	b.device.ProductID = pid
	b.device.attachment = &usbAttachment{fingerprint: fmt.Sprintf("%064x", fingerprint)}
	b.info.Identity.PID = pid
	b.info.Identity.Version = version
	b.device.Serial = b.runtimeSerial
	if pid == 0x3010 {
		b.device.Serial = "BOOT-mode-serial"
	}
	if b.replacement {
		b.info.Identity.Serial = "different-camera"
	}
}
func (b *fakePanaCast50Backend) Command(_ context.Context, _ USBDevice, _ cameraIdentity, step byte) error {
	b.actions = append(b.actions, fmt.Sprintf("command-%d", step))
	err := b.commandError[step]
	if errors.Is(err, errCameraCommandNotStarted) {
		return err
	}
	if step == 0 {
		b.transition(0x3010, 2, "0.24.1")
		b.info.FWState, b.info.VideoState = 0, 0
	} else {
		b.transition(0x3010, 3, "0.24.1")
		b.info.FWState, b.info.VideoState = 7, 0xf1
		b.video = []byte{0xf2, 0xf0}
		if b.directRuntime {
			b.transition(b.runtimePID, 3, b.wanted)
			b.info.FWState, b.info.VideoState = 16, 0
			b.video = nil
		}
	}
	return err
}
func (b *fakePanaCast50Backend) Permission(context.Context, USBDevice, cameraIdentity) error {
	b.actions = append(b.actions, "permission")
	return b.permissionError
}
func (b *fakePanaCast50Backend) Reset(context.Context, USBDevice, cameraIdentity) error {
	b.actions = append(b.actions, "reset")
	if !errors.Is(b.resetError, errCameraCommandNotStarted) {
		b.transition(b.runtimePID, 4, b.wanted)
		b.info.FWState, b.info.FWDetail, b.info.VideoState = 16, 0, 0
	}
	return b.resetError
}
func (b *fakePanaCast50Backend) Current(context.Context, USBDevice) (USBDevice, error) {
	return b.device, nil
}
func (b *fakePanaCast50Backend) Sleep(ctx context.Context, _ time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(b.video) > 0 {
		b.info.VideoState = b.video[0]
		b.video = b.video[1:]
	}
	return nil
}

func panaCast50Fixture(gnp bool) (*fakePanaCast50Backend, *panacast50Archive, *firmwareRecoveryState) {
	d := USBDevice{VendorID: JabraVendorID, ProductID: 0x3011, SysPath: "/sys/bus/usb/devices/1-2", Serial: "runtime-serial", attachment: &usbAttachment{fingerprint: fmt.Sprintf("%064x", 1)}}
	a := &panacast50Archive{Manifest: &BuildVector{ProductName: "Synthetic PanaCast 50", Version: "9.3.6", TargetUSBPIDs: []string{"0x3010"}}}
	b := &fakePanaCast50Backend{device: d, runtimePID: d.ProductID, runtimeSerial: d.Serial, wanted: a.Manifest.Version, gnp: gnp, commandError: map[byte]error{}, info: panacast50Info{Identity: cameraIdentity{PID: d.ProductID, Port: d.SysPath, Address: 1, Serial: "management-serial", Version: "8.1.2"}, HaveFWState: true, HaveVideoState: true}}
	s := &firmwareRecoveryState{FormatVersion: firmwareRecoveryStateVersion, ArchiveSHA256: strings.Repeat("a", 64), ProductName: a.Manifest.ProductName, FirmwareVersion: a.Manifest.Version, TargetUSBPIDs: a.Manifest.TargetUSBPIDs, Attempt: 1}
	return b, a, s
}
func TestPanaCast50BothTransportsAndFullRestartSequence(t *testing.T) {
	for _, gnp := range []bool{true, false} {
		t.Run(fmt.Sprint(gnp), func(t *testing.T) {
			b, a, s := panaCast50Fixture(gnp)
			var phases []string
			err := runPanaCast50Install(context.Background(), b, b.device, a, s, func() error {
				if err := validPanaCast50Recovery(*s); err != nil {
					return err
				}
				phases = append(phases, s.Phase)
				return nil
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			wanted := []string{"stage-gnp", "command-2", "permission", "reset"}
			if !gnp {
				wanted = []string{"command-0", "stage-mass", "command-1", "permission", "reset"}
			}
			if !reflect.DeepEqual(b.actions, wanted) || s.Phase != "verifying" || !s.PanaCast50.ProgramReplugged {
				t.Fatal("wrong lifecycle", b.actions, phases)
			}
		})
	}
}
func TestPanaCast50CanFinishWithoutUnneededExtraReboot(t *testing.T) {
	b, a, s := panaCast50Fixture(true)
	b.directRuntime = true
	if err := runPanaCast50Install(context.Background(), b, b.device, a, s, func() error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.actions, []string{"stage-gnp", "command-2"}) {
		t.Fatal("already complete camera was reset again", b.actions)
	}
}

func TestPanaCast50WaitsForInitialVideoBoot(t *testing.T) {
	b, a, s := panaCast50Fixture(true)
	b.info.FWState, b.info.VideoState = 13, 1
	b.video = []byte{0}
	if err := runPanaCast50Install(context.Background(), b, b.device, a, s, func() error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
}
func TestPanaCast50KnownUnsentCommandsRemainRetryable(t *testing.T) {
	for _, step := range []byte{0, 1, 2} {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			b, a, s := panaCast50Fixture(step == 2)
			b.commandError[step] = cameraCommandNotStarted(errors.New("not sent"))
			if err := runPanaCast50Install(context.Background(), b, b.device, a, s, func() error { return nil }, nil); !errors.Is(err, errCameraCommandNotStarted) {
				t.Fatal(err)
			}
			wanted := "staging"
			if step == 0 {
				wanted = "ready"
			}
			if s.Phase != wanted {
				t.Fatal("not retryable", s.Phase)
			}
			delete(b.commandError, step)
			if err := runPanaCast50Install(context.Background(), b, b.device, a, s, func() error { return nil }, nil); err != nil {
				t.Fatal("retry failed", err)
			}
		})
	}
}
func TestPanaCast50LostRepliesDoNotReplayCommands(t *testing.T) {
	for _, step := range []byte{0, 1, 2} {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			b, a, s := panaCast50Fixture(step == 2)
			b.commandError[step] = context.DeadlineExceeded
			if err := runPanaCast50Install(context.Background(), b, b.device, a, s, func() error { return nil }, nil); err == nil {
				t.Fatal("lost reply was ignored")
			}
			before := 0
			for _, action := range b.actions {
				if action == fmt.Sprintf("command-%d", step) {
					before++
				}
			}
			delete(b.commandError, step)
			if err := runPanaCast50Install(context.Background(), b, b.device, a, s, func() error { return nil }, nil); err != nil {
				t.Fatal(err)
			}
			after := 0
			for _, action := range b.actions {
				if action == fmt.Sprintf("command-%d", step) {
					after++
				}
			}
			if before != 1 || after != before {
				t.Fatal("ambiguous operation was repeated", b.actions)
			}
		})
	}
}
func TestPanaCast50FailuresDoNotActivateBadFiles(t *testing.T) {
	for _, mode := range []string{"save", "staging", "wrong-serial", "permission", "unsent-reset", "lost-reset", "wrong-version"} {
		t.Run(mode, func(t *testing.T) {
			b, a, s := panaCast50Fixture(true)
			save := func() error { return nil }
			switch mode {
			case "save":
				save = func() error { return errors.New("disk full") }
			case "staging":
				b.stageError = errors.New("file digest mismatch")
			case "wrong-serial":
				b.replacement = true
			case "permission":
				b.permissionError = errors.New("permission denied")
			case "unsent-reset":
				b.resetError = cameraCommandNotStarted(errors.New("reset not sent"))
			case "lost-reset":
				b.resetError = context.DeadlineExceeded
			case "wrong-version":
				b.wanted = "8.1.2"
			}
			if err := runPanaCast50Install(context.Background(), b, b.device, a, s, save, nil); err == nil {
				t.Fatal("failed update succeeded")
			}
			if mode == "save" && len(b.actions) != 0 {
				t.Fatal("write before checkpoint")
			}
			if mode == "staging" && !reflect.DeepEqual(b.actions, []string{"stage-gnp"}) {
				t.Fatal("bad file was activated", b.actions)
			}
			if mode == "unsent-reset" {
				if s.Phase != "exiting" {
					t.Fatal("unsent reset is not retryable")
				}
				b.resetError = nil
				if err := runPanaCast50Install(context.Background(), b, b.device, a, s, save, nil); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "lost-reset" {
				if s.Phase != "rebooting" {
					t.Fatal("lost reset intent was forgotten")
				}
				b.resetError = nil
				if err := runPanaCast50Install(context.Background(), b, b.device, a, s, save, nil); err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, action := range b.actions {
					if action == "reset" {
						count++
					}
				}
				if count != 1 {
					t.Fatal("uncertain reset was repeated")
				}
			}
			if mode == "wrong-version" {
				if s.Phase != "ready" {
					t.Fatal("reverted firmware was not left retryable")
				}
				b.wanted = a.Manifest.Version
				if err := runPanaCast50Install(context.Background(), b, b.device, a, s, save, nil); err != nil {
					t.Fatal("reverted firmware retry failed", err)
				}
			}
		})
	}
}
