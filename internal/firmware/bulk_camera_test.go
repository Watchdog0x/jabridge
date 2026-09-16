package firmware

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func cameraTestArchive(t *testing.T, change func(map[string][]byte)) string {
	t.Helper()
	header := make([]byte, 24)
	copy(header, "CrAU")
	binary.BigEndian.PutUint64(header[4:12], 2)
	binary.BigEndian.PutUint64(header[12:20], 40)
	binary.BigEndian.PutUint32(header[20:24], 7)
	payload := append(header, bytes.Repeat([]byte{0xab}, 104)...)
	hash := sha256.Sum256(payload)
	metadata := sha256.Sum256(payload[:64])
	entries := map[string][]byte{
		"info.xml":               []byte(`<buildVector version="4.2.0.20" productName="Synthetic U30"><targetUsbPids><usbPid>0x3093</usbPid></targetUsbPids></buildVector>`),
		"payload.bin":            payload,
		"payload_properties.txt": fmt.Appendf(nil, "FILE_SIZE=%d\nFILE_HASH=%s\nMETADATA_SIZE=64\nMETADATA_HASH=%s\n", len(payload), base64.StdEncoding.EncodeToString(hash[:]), base64.StdEncoding.EncodeToString(metadata[:])),
	}
	if change != nil {
		change(entries)
	}
	path := filepath.Join(t.TempDir(), "camera.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(file)
	for name, data := range entries {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBulkCameraArchiveValidation(t *testing.T) {
	path := cameraTestArchive(t, nil)
	archive, err := loadBulkCameraArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if archive.MD5 != md5.Sum(data) || archive.Size != int64(len(data)) || archive.Profile.ArchivePID != 0x3093 {
		t.Fatal("incorrect camera archive identity")
	}
	for _, mode := range []string{"payload-corrupt", "header-version", "metadata-size", "signature-size", "missing-properties", "duplicate-property", "wrong-size", "wrong-target", "extra-image", "path-traversal"} {
		t.Run(mode, func(t *testing.T) {
			path := cameraTestArchive(t, func(entries map[string][]byte) {
				switch mode {
				case "payload-corrupt":
					entries["payload.bin"][100] ^= 1
				case "header-version":
					entries["payload.bin"][11] = 3
				case "metadata-size":
					entries["payload_properties.txt"] = bytes.ReplaceAll(entries["payload_properties.txt"], []byte("METADATA_SIZE=64"), []byte("METADATA_SIZE=200"))
				case "signature-size":
					binary.BigEndian.PutUint32(entries["payload.bin"][20:24], 1000)
				case "missing-properties":
					delete(entries, "payload_properties.txt")
				case "duplicate-property":
					entries["payload_properties.txt"] = append(entries["payload_properties.txt"], []byte("FILE_SIZE=\nFILE_SIZE=128\n")...)
				case "wrong-size":
					entries["payload_properties.txt"] = bytes.ReplaceAll(entries["payload_properties.txt"], []byte("FILE_SIZE=128"), []byte("FILE_SIZE=129"))
				case "wrong-target":
					entries["info.xml"] = bytes.ReplaceAll(entries["info.xml"], []byte("0x3093"), []byte("0x3090"))
				case "extra-image":
					entries["info.xml"] = bytes.ReplaceAll(entries["info.xml"], []byte("</buildVector>"), []byte(`<files><file name="other.bin"/></files></buildVector>`))
				case "path-traversal":
					entries["../other"] = []byte{1}
				}
			})
			if _, err := loadBulkCameraArchive(path); err == nil {
				t.Fatal("unsafe camera archive accepted")
			}
		})
	}
}

func TestBulkCameraOriginalArchives(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_BULK_CAMERA_AUDIT")
	if path == "" {
		t.Skip("original camera archives are private opt-in fixtures")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Path, Version, MD5 string
		Bytes              int64
		Devices            []struct{ PID string }
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if seen[row.Path] {
			continue
		}
		seen[row.Path] = true
		t.Run(filepath.Base(row.Path), func(t *testing.T) {
			archive, err := loadBulkCameraArchive(row.Path)
			if err != nil {
				t.Fatal(err)
			}
			if archive.Manifest.Version != row.Version || archive.Size != row.Bytes || base64.StdEncoding.EncodeToString(archive.MD5[:]) != row.MD5 {
				t.Fatal("original archive identity mismatch")
			}
			t.Logf("verified streamed payload, metadata and official archive checksum: %s, %d bytes", row.Version, row.Bytes)
			if os.Getenv("JABRIDGE_TEST_LIVE_RELEASES") == "1" {
				for _, pid := range archive.Profile.RuntimePIDs {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					err := verifyCameraRelease(ctx, row.Path, archive, pid)
					cancel()
					if err != nil {
						t.Fatalf("official release for %04x: %v", pid, err)
					}
				}
			}
			if os.Getenv("JABRIDGE_TEST_CAMERA_SNAPSHOT") == "1" {
				snapshot, err := freezeFirmwareFile(row.Path)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = snapshot.Close() }()
				again, err := freezeFirmwareFile(snapshot.path)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = again.Close() }()
				one, err := snapshot.file.Stat()
				if err != nil {
					t.Fatal(err)
				}
				two, err := again.file.Stat()
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(one, two) || snapshot.digest != again.digest {
					t.Fatal("large snapshot was copied or changed")
				}
			}
			if os.Getenv("JABRIDGE_TEST_CAMERA_STREAM") == "1" {
				file, err := os.Open(row.Path)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = file.Close() }()
				peer := &cameraStreamingPeer{cameraBulkPeer: newCameraBulkPeer(), digest: md5.New()}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				if err := transferBulkCamera(ctx, peer, file, archive.Size, archive.MD5, nil); err != nil {
					t.Fatal(err)
				}
				if peer.bytes != archive.Size || !peer.ended || !bytes.Equal(peer.digest.Sum(nil), archive.MD5[:]) {
					t.Fatal("full streamed camera archive mismatch")
				}
				t.Logf("full independent stream: %d data packets, %d bytes, exact archive MD5", peer.packets, peer.bytes)
			}
		})
	}
	if len(seen) != 2 {
		t.Fatalf("expected both camera packages, got %d", len(seen))
	}
}

// Constant-memory receiver for multi-GB original fixtures. Packet CRC and
// padding are independently tested above; this checks the full data stream.
type cameraStreamingPeer struct {
	*cameraBulkPeer
	digest         hash.Hash
	bytes, packets int64
}

func (p *cameraStreamingPeer) Write(ctx context.Context, packet []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(packet) != 512 || packet[0] != 0xaa {
		return errors.New("invalid stream packet")
	}
	n := int(binary.LittleEndian.Uint16(packet[2:4]))
	if n > 506 {
		return errors.New("stream packet too long")
	}
	switch packet[1] {
	case 4:
		p.reply(22, p.digest.Sum(nil))
	case 1:
		if p.started || n != 4 {
			return errors.New("duplicate stream start")
		}
		p.started = true
		p.wanted = binary.LittleEndian.Uint32(packet[6:])
		p.reply(23, nil)
	case 2:
		if !p.started || p.ended || n == 0 {
			return errors.New("stream data out of order")
		}
		_, _ = p.digest.Write(packet[6 : 6+n])
		p.bytes += int64(n)
		p.packets++
	case 3:
		if p.bytes != int64(p.wanted) || !p.started || p.ended {
			return errors.New("stream ended at wrong size")
		}
		p.ended = true
		p.reply(24, nil)
	default:
		return errors.New("unknown stream command")
	}
	return nil
}

func TestCameraSnapshotReusesSealedInode(t *testing.T) {
	path := cameraTestArchive(t, nil)
	one, err := freezeFirmwareFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = one.Close() }()
	two, err := freezeFirmwareFile(one.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = two.Close() }()
	a, err := one.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	b, err := two.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(a, b) || one.digest != two.digest {
		t.Fatal("immutable inode was copied")
	}
	if err := one.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBulkCameraArchive(two.path); err != nil {
		t.Fatal("closing original snapshot invalidated independent descriptor", err)
	}
}

func TestCameraSnapshotPinsArchiveBeforeSourceReplacement(t *testing.T) {
	path := cameraTestArchive(t, nil)
	snapshot, err := freezeFirmwareFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snapshot.Close() }()
	archive, err := loadBulkCameraArchive(snapshot.path)
	if err != nil {
		t.Fatal(err)
	}
	// Same-size replacement after preparation must not affect parsing, the
	// checksum approved by metadata, or the already-staged shortcut digest.
	if err := os.WriteFile(path, bytes.Repeat([]byte{0x55}, int(archive.Size)), 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := loadBulkCameraArchive(snapshot.path)
	if err != nil || again.MD5 != archive.MD5 {
		t.Fatal("source replacement changed sealed archive", err)
	}
	checksum, err := firmwareFileMD5(snapshot.path)
	if err != nil || checksum != base64.StdEncoding.EncodeToString(archive.MD5[:]) {
		t.Fatal("metadata input differs from staging digest", err)
	}
	// Even a mismatched archive object must fail before metadata or hardware.
	archive.MD5[0] ^= 1
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := verifyCameraRelease(ctx, snapshot.path, archive, 0x3093); err == nil || !strings.Contains(err.Error(), "checksum changed") {
		t.Fatal("different staging digest was accepted", err)
	}
}

type fakeCameraControl struct {
	id         cameraIdentity
	events     []cameraUpdateEvent
	started    bool
	starts     int
	startError error
}

func (c *fakeCameraControl) Identity() cameraIdentity        { return c.id }
func (c *fakeCameraControl) Subscribe(context.Context) error { return nil }
func (c *fakeCameraControl) Close() error                    { return nil }
func (c *fakeCameraControl) Start(context.Context) error {
	c.starts++
	c.started = !errors.Is(c.startError, errCameraCommandNotStarted) && !errors.Is(c.startError, errSitelRejected)
	return c.startError
}
func (c *fakeCameraControl) Next(ctx context.Context) (cameraUpdateEvent, error) {
	if err := ctx.Err(); err != nil {
		return cameraUpdateEvent{}, err
	}
	if !c.started || len(c.events) == 0 {
		return cameraUpdateEvent{}, context.DeadlineExceeded
	}
	event := c.events[0]
	c.events = c.events[1:]
	return event, nil
}

type fakeCameraBackend struct {
	controls                []*fakeCameraControl
	opened, stages, reboots int
	stageError              error
}

func (b *fakeCameraBackend) Open(context.Context, USBDevice) (cameraControl, error) {
	c := b.controls[min(b.opened, len(b.controls)-1)]
	b.opened++
	return c, nil
}
func (b *fakeCameraBackend) Stage(context.Context, USBDevice, string, *bulkCameraArchive, func(int64, int64)) error {
	b.stages++
	return b.stageError
}
func (b *fakeCameraBackend) Reboot(_ context.Context, d USBDevice) (USBDevice, error) {
	b.reboots++
	d.attachment = &usbAttachment{fingerprint: fmt.Sprintf("%064x", b.reboots+1)}
	return d, nil
}

func cameraInstallFixture() (USBDevice, *bulkCameraArchive, *firmwareRecoveryState, cameraIdentity) {
	d := USBDevice{VendorID: JabraVendorID, ProductID: 0x3092, SysPath: "/sys/bus/usb/devices/1-2", Serial: "USB-test", attachment: &usbAttachment{fingerprint: fmt.Sprintf("%064x", 1)}}
	archive := &bulkCameraArchive{Manifest: &BuildVector{Version: "4.2.0.20", ProductName: "Synthetic U30", TargetUSBPIDs: []string{"0x3093"}}, Profile: bulkCameraProfiles[1]}
	state := &firmwareRecoveryState{FormatVersion: firmwareRecoveryStateVersion, ArchiveSHA256: fmt.Sprintf("%064x", 17), ProductName: archive.Manifest.ProductName, FirmwareVersion: archive.Manifest.Version, TargetUSBPIDs: archive.Manifest.TargetUSBPIDs, Attempt: 1}
	id := cameraIdentity{PID: d.ProductID, Port: d.SysPath, Address: 8, Serial: "GNP-test", Version: "4.1.0.1"}
	return d, archive, state, id
}

func TestCameraInstallLifecycle(t *testing.T) {
	for _, subsystem := range []bool{false, true} {
		t.Run(fmt.Sprint(subsystem), func(t *testing.T) {
			d, a, state, id := cameraInstallFixture()
			updated := id
			updated.Version = a.Manifest.Version
			initial := &fakeCameraControl{id: id, events: []cameraUpdateEvent{{State: 26, Progress: -1}}}
			merge := &fakeCameraControl{id: updated, started: true, events: []cameraUpdateEvent{{State: 16, Progress: -1}}}
			if subsystem {
				merge.events = []cameraUpdateEvent{{Progress: 1}, {Progress: 80}, {State: 26, Progress: -1}}
			}
			backend := &fakeCameraBackend{controls: []*fakeCameraControl{initial, merge, {id: updated, started: true}}}
			var phases []string
			err := runCameraInstall(context.Background(), backend, d, "unused", a, state, func() error {
				if err := validCameraRecovery(*state); err != nil {
					return err
				}
				phases = append(phases, state.Phase)
				return nil
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			wanted := []string{"staging", "activating", "system", "reboot-system", "merging", "verifying"}
			reboots := 1
			if subsystem {
				wanted = []string{"staging", "activating", "system", "reboot-system", "merging", "subsystem", "reboot-subsystem", "verifying"}
				reboots = 2
			}
			if !reflect.DeepEqual(phases, wanted) || backend.reboots != reboots || backend.stages != 1 || initial.starts != 1 {
				t.Fatalf("wrong lifecycle: %v stage=%d reboot=%d start=%d", phases, backend.stages, backend.reboots, initial.starts)
			}
		})
	}
}

func TestCameraActivationKnownNotSentStaysRetryable(t *testing.T) {
	d, a, state, id := cameraInstallFixture()
	initial := &fakeCameraControl{id: id, startError: cameraCommandNotStarted(errors.New("write gate rejected activation"))}
	backend := &fakeCameraBackend{controls: []*fakeCameraControl{initial}}
	if err := runCameraInstall(context.Background(), backend, d, "unused", a, state, func() error { return nil }, nil); err == nil {
		t.Fatal("known-unsent activation reported success")
	}
	if state.Phase != "staging" || initial.starts != 1 || initial.started {
		t.Fatalf("known-unsent activation is not retryable: phase=%s starts=%d", state.Phase, initial.starts)
	}
	updated := id
	updated.Version = a.Manifest.Version
	retry := &fakeCameraControl{id: id, events: []cameraUpdateEvent{{State: 26, Progress: -1}}}
	merge := &fakeCameraControl{id: updated, started: true, events: []cameraUpdateEvent{{State: 16, Progress: -1}}}
	recovery := &fakeCameraBackend{controls: []*fakeCameraControl{retry, merge, {id: updated}}}
	if err := runCameraInstall(context.Background(), recovery, d, "unused", a, state, func() error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if recovery.stages != 1 || retry.starts != 1 {
		t.Fatalf("known-unsent activation was not retried: stages=%d starts=%d", recovery.stages, retry.starts)
	}
}

func TestCameraRecoveryNeverReactivatesAmbiguousUpdate(t *testing.T) {
	d, a, state, id := cameraInstallFixture()
	initial := &fakeCameraControl{id: id, startError: errors.New("lost activation ACK")}
	backend := &fakeCameraBackend{controls: []*fakeCameraControl{initial}}
	if err := runCameraInstall(context.Background(), backend, d, "unused", a, state, func() error { return nil }, nil); err == nil || state.Phase != "activating" {
		t.Fatalf("%v %s", err, state.Phase)
	}
	updated := id
	updated.Version = a.Manifest.Version
	monitor := &fakeCameraControl{id: id, started: true, events: []cameraUpdateEvent{{State: 26, Progress: -1}}}
	merge := &fakeCameraControl{id: updated, started: true, events: []cameraUpdateEvent{{State: 16, Progress: -1}}}
	recovery := &fakeCameraBackend{controls: []*fakeCameraControl{monitor, merge, {id: updated}}}
	if err := runCameraInstall(context.Background(), recovery, d, "unused", a, state, func() error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if recovery.stages != 0 || monitor.starts != 0 {
		t.Fatal("ambiguous update was restarted")
	}
}

func TestCameraRecoveryAfterRecordedReboot(t *testing.T) {
	d, a, state, id := cameraInstallFixture()
	if err := bindCameraRecovery(state, d, id, a); err != nil {
		t.Fatal(err)
	}
	state.Phase = "reboot-system"
	state.Camera.RebootFrom = d.attachment.fingerprint
	d.attachment = &usbAttachment{fingerprint: fmt.Sprintf("%064x", 2)}
	id.Version = a.Manifest.Version
	backend := &fakeCameraBackend{controls: []*fakeCameraControl{{id: id, started: true, events: []cameraUpdateEvent{{State: 16, Progress: -1}}}}}
	if err := runCameraInstall(context.Background(), backend, d, "unused", a, state, func() error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if backend.reboots != 0 || backend.stages != 0 {
		t.Fatal("already observed restart was repeated")
	}
}

func TestCameraCannotFinishOnNewVersionAlone(t *testing.T) {
	d, a, state, id := cameraInstallFixture()
	if err := bindCameraRecovery(state, d, id, a); err != nil {
		t.Fatal(err)
	}
	state.Phase = "merging"
	id.Version = a.Manifest.Version
	backend := &fakeCameraBackend{controls: []*fakeCameraControl{{id: id, started: true}}}
	if err := runCameraInstall(context.Background(), backend, d, "unused", a, state, func() error { return nil }, nil); err == nil {
		t.Fatal("accepted a version without update completion")
	}
}

func TestCameraFailurePreservesRecovery(t *testing.T) {
	for _, mode := range []string{"save", "stage", "device-failed", "wrong-final-version", "replacement-serial"} {
		t.Run(mode, func(t *testing.T) {
			d, a, state, id := cameraInstallFixture()
			updated := id
			updated.Version = a.Manifest.Version
			first := &fakeCameraControl{id: id, events: []cameraUpdateEvent{{State: 26, Progress: -1}}}
			last := &fakeCameraControl{id: updated, started: true, events: []cameraUpdateEvent{{State: 16, Progress: -1}}}
			backend := &fakeCameraBackend{controls: []*fakeCameraControl{first, last}}
			save := func() error { return nil }
			switch mode {
			case "save":
				save = func() error { return errors.New("disk full") }
			case "stage":
				backend.stageError = errors.New("USB disconnected")
			case "device-failed":
				first.events = []cameraUpdateEvent{{State: 20, Progress: -1}}
			case "wrong-final-version":
				last.id.Version = "4.1.0.1"
			case "replacement-serial":
				last.id.Serial = "different"
			}
			if err := runCameraInstall(context.Background(), backend, d, "unused", a, state, save, nil); err == nil {
				t.Fatal("failed update reported success")
			}
			if (mode == "save" || mode == "stage") && first.starts != 0 {
				t.Fatal("activated unstaged firmware")
			}
			if mode == "device-failed" && state.Phase != "failed" {
				t.Fatal("explicit failure not persisted")
			}
		})
	}
}

func TestCameraEventsBoundToAddress(t *testing.T) {
	packet := []byte{0, 8, 0, 7, 0x12, 0x12, 16}
	if event, ok, err := decodeCameraEvent(packet, 8); err != nil || !ok || event.State != 16 {
		t.Fatal(event, ok, err)
	}
	for _, change := range []int{0, 1, 4} {
		p := append([]byte(nil), packet...)
		p[change]++
		if _, ok, _ := decodeCameraEvent(p, 8); ok {
			t.Fatal("unrelated event accepted")
		}
	}
	if _, _, err := decodeCameraEvent(packet[:6], 8); err == nil {
		t.Fatal("truncated state accepted")
	}
}
