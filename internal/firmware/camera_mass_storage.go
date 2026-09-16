package firmware

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

const udisksService = "org.freedesktop.UDisks2"
const udisksFilesystem = udisksService + ".Filesystem"
const udisksBlock = udisksService + ".Block"

var errCameraStoragePending = errors.New("camera firmware volume is not ready")

type cameraFilesystem struct {
	device             USBDevice
	blockPath, sysPath string
	info               os.FileInfo
	number             uint64
	object             dbus.BusObject
}

func readDeviceNumber(filename string) (uint64, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return 0, err
	}
	parts := strings.Split(strings.TrimSpace(string(data)), ":")
	if len(parts) != 2 {
		return 0, errors.New("invalid block device number")
	}
	major, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return 0, err
	}
	minor, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return 0, err
	}
	return unix.Mkdev(uint32(major), uint32(minor)), nil
}
func (c cameraFilesystem) validate() error {
	if err := validateUSBDevice(c.device); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(c.sysPath)
	if err != nil || !strings.HasPrefix(resolved, c.device.attachment.realPath+string(filepath.Separator)) {
		return errors.New("camera storage no longer belongs to the selected USB device")
	}
	info, err := os.Stat(c.sysPath)
	if err != nil || !os.SameFile(info, c.info) {
		return errors.New("camera block attachment changed")
	}
	number, err := readDeviceNumber(filepath.Join(c.sysPath, "dev"))
	if err != nil || number != c.number {
		return errors.New("camera block device number changed")
	}
	info, err = os.Lstat(c.blockPath)
	if err != nil || info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice != 0 {
		return errors.New("camera storage is not a direct block node")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Rdev) != c.number {
		return errors.New("camera storage node was replaced")
	}
	return nil
}

func udisksProperties(ctx context.Context, object dbus.BusObject, intf string) (map[string]dbus.Variant, error) {
	var properties map[string]dbus.Variant
	err := object.CallWithContext(ctx, "org.freedesktop.DBus.Properties.GetAll", 0, intf).Store(&properties)
	return properties, err
}

func findCameraFilesystem(ctx context.Context, connection *dbus.Conn, device USBDevice) (cameraFilesystem, error) {
	if device.VendorID != JabraVendorID || device.ProductID != 0x3010 || device.ViaDongle {
		return cameraFilesystem{}, errors.New("camera is not in its expected storage mode")
	}
	if err := validateUSBDevice(device); err != nil {
		return cameraFilesystem{}, err
	}
	nodes, err := os.ReadDir("/sys/class/block")
	if err != nil {
		return cameraFilesystem{}, err
	}
	var matches []cameraFilesystem
	for _, node := range nodes {
		name := node.Name()
		valid := name != ""
		for _, b := range []byte(name) {
			allowed := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
			if !allowed {
				valid = false
			}
		}
		if !valid {
			continue
		}
		sysPath := filepath.Join("/sys/class/block", name)
		resolved, err := filepath.EvalSymlinks(sysPath)
		if err != nil || !strings.HasPrefix(resolved, device.attachment.realPath+string(filepath.Separator)) {
			continue
		}
		info, err := os.Stat(sysPath)
		if err != nil {
			return cameraFilesystem{}, err
		}
		number, err := readDeviceNumber(filepath.Join(sysPath, "dev"))
		if err != nil {
			return cameraFilesystem{}, err
		}
		object := connection.Object(udisksService, dbus.ObjectPath("/org/freedesktop/UDisks2/block_devices/"+name))
		properties, err := udisksProperties(ctx, object, udisksBlock)
		if err != nil {
			continue
		}
		usage, _ := properties["IdUsage"].Value().(string)
		kind, _ := properties["IdType"].Value().(string)
		readOnly, readOnlyOK := properties["ReadOnly"].Value().(bool)
		reported, numberOK := properties["DeviceNumber"].Value().(uint64)
		if usage != "filesystem" || kind != "vfat" && kind != "exfat" || !readOnlyOK || readOnly || !numberOK || reported != number {
			continue
		}
		candidate := cameraFilesystem{device: device, blockPath: filepath.Join("/dev", name), sysPath: sysPath, info: info, number: number, object: object}
		if err := candidate.validate(); err != nil {
			return cameraFilesystem{}, err
		}
		matches = append(matches, candidate)
	}
	if len(matches) == 0 {
		return cameraFilesystem{}, errCameraStoragePending
	}
	if len(matches) != 1 {
		return cameraFilesystem{}, fmt.Errorf("need exactly one FAT firmware volume on this camera; found %d", len(matches))
	}
	return matches[0], nil
}

type cameraMountResult struct {
	path    string
	cleanup bool
	reused  bool
}

// A successful method reply establishes cleanup before decoding its body.
// Transport failures can also hide a completed mount; explicit D-Bus method
// rejections do not transfer ownership of a concurrent process's mount.
func cameraMountReply(call *dbus.Call) (cameraMountResult, error) {
	result := cameraMountResult{cleanup: true}
	if call.Err != nil {
		var rejection dbus.Error
		var rejectionPointer *dbus.Error
		if errors.As(call.Err, &rejection) || errors.As(call.Err, &rejectionPointer) {
			result.cleanup = false
		}
		return result, fmt.Errorf("mount camera firmware volume: %w", call.Err)
	}
	err := call.Store(&result.path)
	return result, err
}

func (c cameraFilesystem) mount(ctx context.Context) (cameraMountResult, error) {
	if err := requireHardwareWrites(); err != nil {
		return cameraMountResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return cameraMountResult{}, err
	}
	if err := c.validate(); err != nil {
		return cameraMountResult{}, err
	}
	properties, err := udisksProperties(ctx, c.object, udisksFilesystem)
	if err != nil {
		return cameraMountResult{}, err
	}
	points, ok := properties["MountPoints"].Value().([][]byte)
	if !ok || len(points) > 1 {
		return cameraMountResult{}, errors.New("camera volume has ambiguous mount points")
	}
	if len(points) == 1 {
		// Desktop automounters commonly mount the camera's update drive.
		// Adopt that mount only after the staging session validates and opens
		// it. Earlier failures leave the existing mount untouched.
		return cameraMountResult{path: strings.TrimSuffix(string(points[0]), "\x00"), reused: true}, nil
	}
	options := map[string]dbus.Variant{"auth.no_user_interaction": dbus.MakeVariant(true), "options": dbus.MakeVariant("nodev,nosuid,noexec")}
	return cameraMountReply(c.object.CallWithContext(ctx, udisksFilesystem+".Mount", 0, options))
}
func (c cameraFilesystem) unmount(ctx context.Context) error {
	if err := requireHardwareWrites(); err != nil {
		return err
	}
	if err := c.validate(); err != nil {
		return err
	}
	options := map[string]dbus.Variant{"auth.no_user_interaction": dbus.MakeVariant(true)}
	// No force option: a busy volume must not be detached underneath another
	// process, and no erase/format/ownership operation is ever requested.
	return c.object.CallWithContext(ctx, udisksFilesystem+".Unmount", 0, options).Err
}

func openCameraMount(mount string, number uint64) (*os.File, error) {
	info, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	valid, readErr := cameraRootMount(info, mount, number)
	_ = info.Close()
	if readErr != nil {
		return nil, readErr
	}
	if !valid {
		return nil, errors.New("camera staging path is not the root mount of the selected block device")
	}
	fd, err := unix.Open(mount, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), mount)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || uint64(stat.Dev) != number {
		_ = directory.Close()
		return nil, errors.New("camera mount is not on the selected block device")
	}
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(fd, &filesystem); err != nil || filesystem.Type != unix.MSDOS_SUPER_MAGIC && filesystem.Type != unix.EXFAT_SUPER_MAGIC {
		_ = directory.Close()
		return nil, errors.New("camera staging filesystem is not FAT")
	}
	return directory, nil
}

func cameraRootMount(reader io.Reader, mount string, number uint64) (bool, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	matches := 0
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			return false, errors.New("invalid mount table entry")
		}
		if unescape.Replace(fields[4]) != mount {
			continue
		}
		device := fmt.Sprintf("%d:%d", unix.Major(number), unix.Minor(number))
		if fields[2] != device || unescape.Replace(fields[3]) != "/" {
			return false, nil
		}
		matches++
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return matches == 1, nil
}

type cameraUpgradeReceipt struct {
	stat unix.Stat_t
}

func (r cameraUpgradeReceipt) validate(directory *os.File) error {
	var named unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), "upgrade.zip", &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if named.Mode&unix.S_IFMT != unix.S_IFREG || named.Nlink != 1 || named.Dev != r.stat.Dev || named.Ino != r.stat.Ino || named.Size != r.stat.Size || named.Mtim != r.stat.Mtim || named.Ctim != r.stat.Ctim {
		return errors.New("camera upgrade.zip changed after verification")
	}
	return nil
}

// Bind both the opened file and its required directory entry. The receipt is
// checked again immediately before the mount session closes its directory.
func writeCameraUpgradeFile(ctx context.Context, directory *os.File, data []byte, digest [16]byte, validate func() error, progress func(int64, int64)) (_ *cameraUpgradeReceipt, resultErr error) {
	if directory == nil || validate == nil || len(data) == 0 || int64(len(data)) > MaxExpandedArchiveSize || md5.Sum(data) != digest {
		return nil, errors.New("invalid camera staging plan")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validate(); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(directory.Fd()), "upgrade.zip", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "upgrade.zip")
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	var target, root unix.Stat_t
	if err := unix.Fstat(fd, &target); err != nil {
		return nil, err
	}
	if err := unix.Fstat(int(directory.Fd()), &root); err != nil {
		return nil, err
	}
	if target.Mode&unix.S_IFMT != unix.S_IFREG || target.Nlink != 1 || target.Dev != root.Dev {
		return nil, errors.New("camera staging target is not a single regular file on the camera volume")
	}
	var space unix.Statfs_t
	if err := unix.Fstatfs(fd, &space); err != nil {
		return nil, err
	}
	if space.Bsize <= 0 || space.Bavail > ^uint64(0)/uint64(space.Bsize) {
		return nil, errors.New("invalid camera free-space report")
	}
	available := space.Bavail * uint64(space.Bsize)
	if available < uint64(len(data)) && uint64(len(data))-available > uint64(max(0, target.Size)) {
		return nil, errors.New("camera firmware volume does not have enough free space")
	}
	if err := validate(); err != nil {
		return nil, err
	}
	if err := (cameraUpgradeReceipt{stat: target}).validate(directory); err != nil {
		return nil, err
	}
	if err := file.Truncate(0); err != nil {
		return nil, err
	}
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := validate(); err != nil {
			return nil, err
		}
		end := min(offset+256*1024, len(data))
		n, err := file.Write(data[offset:end])
		if err != nil {
			return nil, err
		}
		if n != end-offset {
			return nil, io.ErrShortWrite
		}
		offset = end
		if progress != nil {
			progress(int64(offset), int64(len(data)))
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	// Capture metadata before readback, so a concurrent rewrite during the
	// hash cannot become the expected file merely by keeping the same name.
	if err := unix.Fstat(fd, &target); err != nil {
		return nil, err
	}
	receipt := &cameraUpgradeReceipt{stat: target}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	hash := md5.New()
	n, err := io.Copy(hash, io.LimitReader(file, int64(len(data))+1))
	if err != nil {
		return nil, err
	}
	if n != int64(len(data)) || !bytes.Equal(hash.Sum(nil), digest[:]) {
		return nil, errors.New("camera volume file readback did not match")
	}
	if err := directory.Sync(); err != nil {
		return nil, err
	}
	if err := validate(); err != nil {
		return nil, err
	}
	if err := receipt.validate(directory); err != nil {
		return nil, err
	}
	return receipt, nil
}

type cameraMountOperations struct {
	mount    func(context.Context) (cameraMountResult, error)
	open     func(string) (*os.File, error)
	validate func() error
	unmount  func(context.Context) error
}

func stageMountedCameraUpgrade(ctx context.Context, archive *panacast50Archive, ops cameraMountOperations, progress func(int64, int64)) (resultErr error) {
	mounted, err := ops.mount(ctx)
	if !mounted.cleanup && !mounted.reused {
		if err == nil {
			return errors.New("camera mount did not establish a staging session")
		}
		return err
	}
	var directory *os.File
	var receipt *cameraUpgradeReceipt
	cleanupNeeded := mounted.cleanup
	// A successful mount always has a matching bounded, non-forced unmount,
	// including open/copy/readback errors and a cancelled staging context.
	defer func() {
		if directory != nil {
			if resultErr == nil && receipt != nil {
				resultErr = receipt.validate(directory)
			}
			resultErr = errors.Join(resultErr, directory.Close())
		}
		if cleanupNeeded {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := ops.unmount(cleanup); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close files using the camera volume, then retry this firmware file: %w", err))
			}
		}
	}()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(mounted.path) || strings.ContainsRune(mounted.path, 0) || filepath.Clean(mounted.path) != mounted.path {
		return errors.New("invalid camera mount path")
	}
	if err := ops.validate(); err != nil {
		return err
	}
	directory, err = ops.open(mounted.path)
	if err != nil {
		return err
	}
	// Updating this verified camera volume includes flushing and safely
	// ejecting it before activation, even if the desktop mounted it first.
	// The non-forced unmount still refuses a volume held by another process.
	cleanupNeeded = true
	receipt, err = writeCameraUpgradeFile(ctx, directory, archive.Data, archive.MD5, ops.validate, progress)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return ops.validate()
}

func stageCameraMassStorage(ctx context.Context, device USBDevice, archive *panacast50Archive, progress func(int64, int64)) error {
	if archive == nil {
		return errors.New("missing camera bundle")
	}
	if err := requireHardwareWrites(); err != nil {
		return err
	}
	// Keep the bus alive through cleanup even if the copy is cancelled.
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("camera storage needs the UDisks2 system service: %w", err)
	}
	defer func() { _ = connection.Close() }()
	ready, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var filesystem cameraFilesystem
	for {
		filesystem, err = findCameraFilesystem(ready, connection, device)
		if err == nil {
			break
		}
		if !errors.Is(err, errCameraStoragePending) {
			return err
		}
		if err := waitDFU(ready, 250*time.Millisecond); err != nil {
			return fmt.Errorf("camera firmware volume did not become ready: %w", err)
		}
	}
	return stageMountedCameraUpgrade(ctx, archive, cameraMountOperations{
		mount:    filesystem.mount,
		open:     func(mount string) (*os.File, error) { return openCameraMount(mount, filesystem.number) },
		validate: filesystem.validate,
		unmount:  filesystem.unmount,
	}, progress)
}
