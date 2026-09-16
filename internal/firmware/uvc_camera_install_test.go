package firmware

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

type fakeUVCCameraBackend struct {
	id                 cameraIdentity
	updated            string
	images             []bool
	resets, reboots    int
	resetErr, imageErr error
	replacement        bool
}

func (b *fakeUVCCameraBackend) Identity(context.Context, USBDevice) (cameraIdentity, error) {
	return b.id, nil
}
func (b *fakeUVCCameraBackend) Image(_ context.Context, _ USBDevice, image uvcCameraImage, _ func(int, int)) error {
	b.images = append(b.images, image.Boot)
	return b.imageErr
}
func (b *fakeUVCCameraBackend) Reset(context.Context, USBDevice, cameraIdentity) error {
	b.resets++
	return b.resetErr
}
func (b *fakeUVCCameraBackend) Reboot(_ context.Context, d USBDevice) (USBDevice, error) {
	b.reboots++
	d.attachment = &usbAttachment{fingerprint: fmt.Sprintf("%064x", 10)}
	b.id.Version = b.updated
	if b.replacement {
		b.id.Serial = "different"
	}
	return d, nil
}
func (b *fakeUVCCameraBackend) Sleep(context.Context, time.Duration) error { return nil }

func uvcInstallFixture() (USBDevice, *uvcCameraArchive, *firmwareRecoveryState, cameraIdentity) {
	d := USBDevice{VendorID: JabraVendorID, ProductID: 0x3020, SysPath: "/sys/bus/usb/devices/1-2", Serial: "USB-camera", attachment: &usbAttachment{fingerprint: fmt.Sprintf("%064x", 1)}}
	a := &uvcCameraArchive{Manifest: &BuildVector{Version: "4.2.9", ProductName: "Synthetic PanaCast 20", TargetUSBPIDs: []string{"0x3020"}}, Boot: uvcCameraImage{Boot: true, Data: []byte("MA2x")}, Main: uvcCameraImage{Data: []byte("MA2x")}}
	s := &firmwareRecoveryState{FormatVersion: firmwareRecoveryStateVersion, ArchiveSHA256: fmt.Sprintf("%064x", 19), ProductName: a.Manifest.ProductName, FirmwareVersion: a.Manifest.Version, TargetUSBPIDs: a.Manifest.TargetUSBPIDs, Attempt: 1}
	id := cameraIdentity{PID: d.ProductID, Port: d.SysPath, Address: 8, Serial: "GNP-camera", Version: "3.1.2"}
	return d, a, s, id
}
func TestUVCCameraInstallLifecycle(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprint(old), func(t *testing.T) {
			d, a, s, id := uvcInstallFixture()
			if old {
				id.Version = "2.7.17"
			}
			b := &fakeUVCCameraBackend{id: id, updated: a.Manifest.Version}
			var phases []string
			err := runUVCCameraInstall(context.Background(), b, d, a, s, func() error {
				if err := validUVCCameraRecovery(*s); err != nil {
					return err
				}
				phases = append(phases, s.Phase)
				return nil
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			wanted := []bool{false}
			if old {
				wanted = []bool{true, false}
			}
			if !reflect.DeepEqual(b.images, wanted) || b.resets != 1 || b.reboots != 1 || s.Phase != "verifying" {
				t.Fatalf("wrong update sequence: images=%v, phases=%v, reset=%d, reboot=%d", b.images, phases, b.resets, b.reboots)
			}
		})
	}
}
func TestUVCCameraRecoveryDoesNotRepeatResetOrCompletedBoot(t *testing.T) {
	for _, phase := range []string{"main", "rebooting", "verifying"} {
		t.Run(phase, func(t *testing.T) {
			d, a, s, id := uvcInstallFixture()
			id.Version = "2.7.18"
			if err := bindUVCCameraRecovery(s, d, id); err != nil {
				t.Fatal(err)
			}
			s.Phase = phase
			if phase == "rebooting" {
				s.UVCCamera.RebootFrom = d.attachment.fingerprint
				d.attachment = &usbAttachment{fingerprint: fmt.Sprintf("%064x", 2)}
			}
			if phase != "main" {
				id.Version = a.Manifest.Version
			}
			b := &fakeUVCCameraBackend{id: id, updated: a.Manifest.Version}
			if err := runUVCCameraInstall(context.Background(), b, d, a, s, func() error { return nil }, nil); err != nil {
				t.Fatal(err)
			}
			if phase == "main" {
				if !reflect.DeepEqual(b.images, []bool{false}) {
					t.Fatal("completed boot was rewritten")
				}
			} else if len(b.images) != 0 || b.resets != 0 || b.reboots != 0 {
				t.Fatal("completed operation was repeated")
			}
		})
	}
}
func TestUVCCameraInstallFailuresStayRecoverable(t *testing.T) {
	for _, mode := range []string{"checkpoint", "image", "lost-reset", "rejected-reset", "reset-error", "wrong-version", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			d, a, s, id := uvcInstallFixture()
			b := &fakeUVCCameraBackend{id: id, updated: a.Manifest.Version}
			save := func() error { return nil }
			switch mode {
			case "checkpoint":
				save = func() error { return errors.New("disk full") }
			case "image":
				b.imageErr = errors.New("write failed")
			case "lost-reset":
				b.resetErr = context.DeadlineExceeded
			case "rejected-reset":
				b.resetErr = errSitelRejected
			case "reset-error":
				b.resetErr = cameraCommandNotStarted(errors.New("identity failed before reset"))
			case "wrong-version":
				b.updated = "3.1.2"
			case "replacement":
				b.replacement = true
			}
			err := runUVCCameraInstall(context.Background(), b, d, a, s, save, nil)
			if mode == "lost-reset" {
				if err != nil || b.resets != 1 || b.reboots != 1 {
					t.Fatalf("lost ACK recovery: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("failed update reported success")
			}
			if (mode == "checkpoint" || mode == "image") && b.resets != 0 {
				t.Fatal("failed image was activated")
			}
			if mode == "checkpoint" && len(b.images) != 0 {
				t.Fatal("write without checkpoint")
			}
			if (mode == "rejected-reset" || mode == "reset-error") && s.Phase != "settling" {
				t.Fatal("reset known not to have started is not retryable")
			}
			if mode == "reset-error" {
				b.resetErr = nil
				if err := runUVCCameraInstall(context.Background(), b, d, a, s, save, nil); err != nil {
					t.Fatal(err)
				}
				if b.resets != 2 || b.reboots != 1 {
					t.Fatalf("known-unsent reset was not retried: resets=%d reboots=%d", b.resets, b.reboots)
				}
			}
			if mode == "wrong-version" && s.Phase != "main" {
				t.Fatal("failed main image is not retryable")
			}
		})
	}
}
