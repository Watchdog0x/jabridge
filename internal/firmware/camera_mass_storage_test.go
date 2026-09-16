package firmware

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/godbus/dbus/v5"
)

func TestCameraMountMustBeWholeSelectedFilesystem(t *testing.T) {
	for _, test := range []struct {
		name, text, mount string
		ok                bool
	}{
		{"matching", `34 20 8:17 / /run/media/zero/Jabra rw - vfat /dev/sdb1 rw`, "/run/media/zero/Jabra", true},
		{"escaped", `34 20 8:17 / /run/media/zero/Jabra\040FW rw - vfat /dev/sdb1 rw`, "/run/media/zero/Jabra FW", true},
		{"wrong-device", `34 20 8:18 / /run/media/zero/Jabra rw - vfat /dev/sdc1 rw`, "/run/media/zero/Jabra", false},
		{"subdirectory-bind", `34 20 8:17 /subdir /run/media/zero/Jabra rw - vfat /dev/sdb1 rw`, "/run/media/zero/Jabra", false},
		{"wrong-mount", `34 20 8:17 / /run/media/zero/Other rw - vfat /dev/sdb1 rw`, "/run/media/zero/Jabra", false},
		{"overlay", "34 20 8:17 / /run/media/zero/Jabra rw - vfat /dev/sdb1 rw\n35 20 8:18 / /run/media/zero/Jabra rw - vfat /dev/sdc1 rw", "/run/media/zero/Jabra", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := cameraRootMount(strings.NewReader(test.text), test.mount, unix.Mkdev(8, 17))
			if err != nil || got != test.ok {
				t.Fatal(got, err)
			}
		})
	}
	if _, err := cameraRootMount(strings.NewReader("not mountinfo"), "/camera", unix.Mkdev(8, 17)); err == nil {
		t.Fatal("malformed mount table accepted")
	}
}

func TestCameraMassStorageWritesOnlyVerifiedUpgradeFile(t *testing.T) {
	root := t.TempDir()
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	data := bytes.Repeat([]byte{3, 8, 2, 7}, 170000)
	checks := 0
	last := int64(0)
	if _, err := writeCameraUpgradeFile(context.Background(), directory, data, md5.Sum(data), func() error { checks++; return nil }, func(done, total int64) {
		if done < last || done > total || total != int64(len(data)) {
			t.Error("invalid copy progress")
		}
		last = done
	}); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(root, "upgrade.zip"))
	if err != nil || !bytes.Equal(actual, data) || last != int64(len(data)) || checks < 4 {
		t.Fatal("camera volume copy/readback failed", err, checks)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "upgrade.zip" {
		t.Fatal("unexpected volume write", err)
	}
}

func TestCameraMassStorageRejectsRedirectedTargets(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory", "changed-device", "cancelled", "wrong-digest"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			outside := filepath.Join(t.TempDir(), "keep.txt")
			original := []byte("do not replace this")
			if err := os.WriteFile(outside, original, 0600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "upgrade.zip")
			switch kind {
			case "symlink":
				if err := os.Symlink(outside, target); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(outside, target); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(target, original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			directory, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = directory.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancelled" {
				cancel()
			}
			checks := 0
			validate := func() error {
				checks++
				if kind == "changed-device" && checks == 2 {
					return errors.New("different camera")
				}
				return nil
			}
			data := []byte("new firmware")
			digest := md5.Sum(data)
			if kind == "wrong-digest" {
				digest[0] ^= 1
			}
			if _, err := writeCameraUpgradeFile(ctx, directory, data, digest, validate, nil); err == nil {
				t.Fatal("unsafe target accepted")
			}
			actual, err := os.ReadFile(outside)
			if err != nil || !bytes.Equal(actual, original) {
				t.Fatal("redirected file was changed", err)
			}
			if kind == "changed-device" || kind == "cancelled" || kind == "wrong-digest" {
				actual, err := os.ReadFile(target)
				if err != nil || !bytes.Equal(actual, original) {
					t.Fatal("target was truncated before validation", err)
				}
			}
		})
	}
}

func TestCameraMassStorageDetectsNameReplacementDuringCopy(t *testing.T) {
	for _, mode := range []string{"rename", "unlink", "symlink", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			directory, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = directory.Close() }()
			data := bytes.Repeat([]byte{0x56}, 400000)
			target := filepath.Join(root, "upgrade.zip")
			replaced := false
			_, err = writeCameraUpgradeFile(context.Background(), directory, data, md5.Sum(data), func() error { return nil }, func(_, _ int64) {
				if replaced {
					return
				}
				replaced = true
				original := filepath.Join(root, "moved.zip")
				if err := os.Rename(target, original); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "rename":
					if err := os.WriteFile(target, []byte("wrong image"), 0600); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					if err := os.Symlink(original, target); err != nil {
						t.Fatal(err)
					}
				case "hardlink":
					if err := os.Link(original, target); err != nil {
						t.Fatal(err)
					}
				}
			})
			if err == nil {
				t.Fatal("accepted a replaced directory entry after reading back the old FD")
			}
		})
	}
}

func TestCameraMassStorageUnmountsAfterEveryMountedExit(t *testing.T) {
	for _, mode := range []string{"success", "mount-failed", "mount-bad-path", "mount-bad-reply", "mount-transport-lost", "mount-device-changed", "open-failed", "invalid-plan", "cancelled", "readback-corrupt", "closed-directory", "unmount-failed", "cancel-and-unmount-failed"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			data := bytes.Repeat([]byte{3, 7}, 170000)
			archive := &panacast50Archive{Data: data, MD5: md5.Sum(data)}
			if mode == "invalid-plan" {
				archive.MD5[0] ^= 1
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var directory *os.File
			var oldFD int
			unmounted := 0
			copyFinished := false
			unmountFailure := errors.New("injected unmount error")
			ops := cameraMountOperations{
				mount: func(context.Context) (cameraMountResult, error) {
					if mode == "mount-failed" {
						return cameraMountResult{}, errors.New("injected mount error")
					}
					switch mode {
					case "mount-bad-path":
						return cameraMountReply(&dbus.Call{Body: []any{"relative/path"}})
					case "mount-bad-reply":
						return cameraMountReply(&dbus.Call{Body: []any{uint32(1)}})
					case "mount-transport-lost":
						return cameraMountReply(&dbus.Call{Err: context.DeadlineExceeded})
					}
					return cameraMountResult{path: root, cleanup: true}, nil
				},
				open: func(string) (*os.File, error) {
					if mode == "open-failed" {
						return nil, errors.New("injected open error")
					}
					var err error
					directory, err = os.Open(root)
					if err == nil {
						oldFD = int(directory.Fd())
					}
					return directory, err
				},
				validate: func() error {
					if mode == "mount-device-changed" {
						return errors.New("device changed after mounting")
					}
					if mode == "closed-directory" && copyFinished {
						return directory.Close()
					}
					return nil
				},
				unmount: func(cleanup context.Context) error {
					unmounted++
					if cleanup.Err() != nil {
						t.Fatal("cancelled copy prevented cleanup")
					}
					if _, ok := cleanup.Deadline(); !ok {
						t.Fatal("unbounded unmount")
					}
					if directory != nil {
						var stat unix.Stat_t
						if err := unix.Fstat(oldFD, &stat); !errors.Is(err, unix.EBADF) {
							t.Fatal("directory remained open during unmount", err)
						}
					}
					if strings.Contains(mode, "unmount-failed") {
						return unmountFailure
					}
					return nil
				},
			}
			err := stageMountedCameraUpgrade(ctx, archive, ops, func(done, total int64) {
				if done != total {
					return
				}
				copyFinished = true
				if mode == "cancelled" || mode == "cancel-and-unmount-failed" {
					cancel()
				}
				if mode == "readback-corrupt" {
					if err := os.WriteFile(filepath.Join(root, "upgrade.zip"), bytes.Repeat([]byte{1}, len(data)), 0600); err != nil {
						t.Fatal(err)
					}
				}
			})
			wantUnmounts := 1
			if mode == "mount-failed" {
				wantUnmounts = 0
			}
			if unmounted != wantUnmounts || (err == nil) != (mode == "success") {
				t.Fatal("wrong cleanup or result", unmounted, err)
			}
			if mode == "cancel-and-unmount-failed" && (!errors.Is(err, context.Canceled) || !errors.Is(err, unmountFailure)) {
				t.Fatal("cleanup discarded one of the errors", err)
			}
		})
	}
}

func TestCameraMassStorageReceiptRejectsLaterNameReplacement(t *testing.T) {
	root := t.TempDir()
	data := []byte("camera upgrade")
	archive := &panacast50Archive{Data: data, MD5: md5.Sum(data)}
	var directory *os.File
	var err error
	directory, err = os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := writeCameraUpgradeFile(context.Background(), directory, data, archive.MD5, func() error { return nil }, nil)
	if err != nil {
		_ = directory.Close()
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	if err := os.Rename(filepath.Join(root, "upgrade.zip"), filepath.Join(root, "moved.zip")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "upgrade.zip"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := receipt.validate(directory); err == nil {
		t.Fatal("a different file with identical bytes reused the old receipt")
	}
}

func TestCameraMountReplyDistinguishesRejectionFromUncertainTransmission(t *testing.T) {
	rejection := dbus.Error{Name: "org.freedesktop.UDisks2.Error.AlreadyMounted", Body: []any{"already mounted"}}
	for _, err := range []error{rejection, &rejection} {
		result, got := cameraMountReply(&dbus.Call{Err: err})
		if got == nil || result.cleanup || result.reused {
			t.Fatal("explicit method rejection adopted another mount", result, got)
		}
	}
	result, err := cameraMountReply(&dbus.Call{Err: context.DeadlineExceeded})
	if !errors.Is(err, context.DeadlineExceeded) || !result.cleanup {
		t.Fatal("uncertain mount lost its cleanup obligation", result, err)
	}
}

func TestCameraAutomountedVolumeIsAdoptedOnlyAfterValidation(t *testing.T) {
	for _, mode := range []string{"success", "wrong-device", "bad-path", "open-error"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			data := []byte("verified camera firmware")
			archive := &panacast50Archive{Data: data, MD5: md5.Sum(data)}
			unmounted := 0
			ops := cameraMountOperations{
				mount: func(context.Context) (cameraMountResult, error) {
					path := root
					if mode == "bad-path" {
						path = "invalid/relative"
					}
					return cameraMountResult{path: path, reused: true}, nil
				},
				open: func(path string) (*os.File, error) {
					if mode == "open-error" {
						return nil, errors.New("injected open failure")
					}
					return os.Open(path)
				},
				validate: func() error {
					if mode == "wrong-device" {
						return errors.New("device changed")
					}
					return nil
				},
				unmount: func(context.Context) error { unmounted++; return nil },
			}
			err := stageMountedCameraUpgrade(context.Background(), archive, ops, nil)
			if mode == "success" {
				actual, readErr := os.ReadFile(filepath.Join(root, "upgrade.zip"))
				if err != nil || readErr != nil || unmounted != 1 || !bytes.Equal(actual, data) {
					t.Fatal("automounted update drive was not staged and ejected", err, readErr, unmounted)
				}
			} else if err == nil || unmounted != 0 {
				t.Fatal("unverified existing mount was changed", err, unmounted)
			}
		})
	}
}
