package firmware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type uvcCameraPeer struct {
	page         int
	image        uvcCameraImage
	header       bool
	written      int
	digest       hash.Hash
	headerStatus byte
	failPage     int
	commands     int
}

func (p *uvcCameraPeer) Command(ctx context.Context, op byte, address uint32, data []byte, response int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.commands++
	switch op {
	case 0xc0:
		version := byte(1)
		if p.page == 1024 {
			version = 2
		}
		return []byte{0, version, 0}, nil
	case 0xc1:
		if p.header || len(data) != 256 || response != 1 || address != 0 {
			return nil, errors.New("invalid header transaction")
		}
		base := uint32(0x4000000)
		kind := "main"
		if p.image.Boot {
			base = 0
			kind = "boot"
		}
		body := p.image.Data
		if string(body[:4]) != "MA2x" {
			body = body[1024:]
		}
		if binary.LittleEndian.Uint32(data) != base || binary.LittleEndian.Uint32(data[4:]) != uint32(len(p.image.Data)) || binary.LittleEndian.Uint32(data[8:]) != uvcImageLimit || binary.LittleEndian.Uint32(data[12:]) != crc32.ChecksumIEEE(body) || int(binary.LittleEndian.Uint16(data[16:])) != p.page || !bytes.Equal(data[18:22], []byte("app\x00")) || !bytes.Equal(data[24:29], append([]byte(kind), 0)) {
			return nil, errors.New("image header fields mismatch")
		}
		p.header = p.headerStatus == 0
		return []byte{p.headerStatus}, nil
	case 0xc2:
		if !p.header || len(data) != p.page || response != 0 {
			return nil, errors.New("page without accepted header")
		}
		base := uint32(0x4000000)
		if p.image.Boot {
			base = 0
		}
		if address != base+uint32(p.written) {
			return nil, errors.New("wrong camera page address")
		}
		if p.failPage > 0 && p.written/p.page+1 == p.failPage {
			return nil, errors.New("injected USB page failure")
		}
		count := min(len(p.image.Data)-p.written, p.page)
		if count <= 0 {
			return nil, errors.New("extra image data")
		}
		if !bytes.Equal(data[:count], p.image.Data[p.written:p.written+count]) || !bytes.Equal(data[count:], bytes.Repeat([]byte{0xff}, p.page-count)) {
			return nil, errors.New("image bytes or final page padding mismatch")
		}
		_, _ = p.digest.Write(data[:count])
		p.written += p.page
		return nil, nil
	default:
		return nil, errors.New("unknown camera request")
	}
}

func testUVCCameraTransfer(t *testing.T, image uvcCameraImage, page int) {
	t.Helper()
	peer := &uvcCameraPeer{page: page, image: image, digest: sha256.New()}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := uvcCameraPageSize(ctx, peer)
	if err != nil || got != page {
		t.Fatal(got, err)
	}
	var waits []time.Duration
	last := 0
	err = transferUVCCameraImage(ctx, peer, image, page, func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }, func(done, total int) {
		if done < last || total != len(image.Data) || done > total {
			t.Error("invalid progress")
		}
		last = done
	})
	if err != nil {
		t.Fatal(err)
	}
	wanted := sha256.Sum256(image.Data)
	if !bytes.Equal(peer.digest.Sum(nil), wanted[:]) || last != len(image.Data) || len(waits) != 2 || waits[0] != 100*time.Millisecond || waits[1] != 5*time.Second {
		t.Fatal("camera transfer was incomplete")
	}
	if peer.commands != 2+(len(image.Data)+page-1)/page {
		t.Fatal("unexpected camera command count")
	}
}

func TestUVCCameraImagesAndTransfers(t *testing.T) {
	for _, size := range []int{1029, 65535, 65536, 65537, 131101} {
		for _, boot := range []bool{false, true} {
			for _, page := range []int{256, 1024} {
				t.Run(fmt.Sprintf("%d-%t-%d", size, boot, page), func(t *testing.T) {
					data := make([]byte, size)
					for i := range data {
						data[i] = byte(i*13 + 7)
					}
					copy(data[1024:], "MA2x")
					image, err := parseUVCCameraImage(GnVFile{}, data)
					if err != nil {
						t.Fatal(err)
					}
					image.Boot = boot
					testUVCCameraTransfer(t, image, page)
				})
			}
		}
	}
}

func TestUVCCameraTransferStopsOnFailure(t *testing.T) {
	data := make([]byte, 2500)
	copy(data, "MA2x")
	image, err := parseUVCCameraImage(GnVFile{}, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"header-rejected", "header-delay-cancel", "page-write", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			peer := &uvcCameraPeer{image: image, page: 256, digest: sha256.New()}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sleep := func(context.Context, time.Duration) error { return nil }
			switch mode {
			case "header-rejected":
				peer.headerStatus = 1
			case "header-delay-cancel":
				sleep = func(context.Context, time.Duration) error { return context.Canceled }
			case "page-write":
				peer.failPage = 3
			case "cancelled":
				cancel()
			}
			if err := transferUVCCameraImage(ctx, peer, image, 256, sleep, nil); err == nil {
				t.Fatal("failure accepted")
			}
			if mode != "page-write" && peer.written != 0 {
				t.Fatal("page written after failure")
			}
			if mode == "page-write" && peer.written != 512 {
				t.Fatal("write failure did not stop on the failed page")
			}
		})
	}
}

func TestUVCVendorPacketLayouts(t *testing.T) {
	selector, packet, err := uvcVendorPacket(0xc0, 0, nil, 3)
	if err != nil || selector != 9 || !bytes.Equal(packet, []byte{0xc0, 0, 0, 0, 0, 3, 0}) {
		t.Fatal(selector, packet, err)
	}
	for _, size := range []int{256, 1024} {
		data := bytes.Repeat([]byte{0xab}, size)
		address := uint32(0x0401ff00)
		if size == 1024 {
			address = 0x0401fc00
		}
		selector, packet, err := uvcVendorPacket(0xc2, address, data, 0)
		wantSelector := byte(11)
		if size == 1024 {
			wantSelector = 23
		}
		if err != nil || selector != wantSelector || len(packet) != size+7 || !bytes.Equal(packet[:5], []byte{0xc2, 1, 4, 0, byte(address >> 8)}) || binary.LittleEndian.Uint16(packet[5:]) != uint16(size) || !bytes.Equal(packet[7:], data) {
			t.Fatalf("invalid wire layout %d %x %v", selector, packet[:7], err)
		}
	}
	for _, size := range []int{0, 1, 255, 257, 1023, 1025} {
		if _, _, err := uvcVendorPacket(0xc2, 0, make([]byte, size), 0); err == nil {
			t.Fatal("accepted partial or oversize page", size)
		}
	}
	for _, data := range [][]byte{nil, []byte("bad!"), append(make([]byte, 1024), []byte("nope")...)} {
		if _, err := parseUVCCameraImage(GnVFile{}, data); err == nil {
			t.Fatal("accepted malformed image")
		}
	}
	for _, version := range []string{"2.7.17", "2.7.18", "2.7.19"} {
		if !uvcBootRequired(0x3020, version) || uvcBootRequired(0x3029, version) {
			t.Fatal("wrong bootloader migration profile")
		}
	}
	if uvcBootRequired(0x3020, "4.2.9") {
		t.Fatal("unnecessary bootloader write")
	}
}

func TestUVCCameraOriginalArchive(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_UVC_CAMERA_AUDIT")
	if path == "" {
		t.Skip("original camera archive is a private opt-in fixture")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Protocol      int
		Path, Version string
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range rows {
		if row.Protocol != 11 || row.Path == "" {
			continue
		}
		count++
		t.Run(filepath.Base(row.Path), func(t *testing.T) {
			archive, err := loadUVCCameraArchive(row.Path)
			if err != nil {
				t.Fatal(err)
			}
			if archive.Manifest.Version != row.Version || archive.Boot.File.Version != "2.7.5" {
				t.Fatal("wrong original image versions")
			}
			for _, image := range []uvcCameraImage{archive.Boot, archive.Main} {
				for _, page := range []int{256, 1024} {
					testUVCCameraTransfer(t, image, page)
					t.Logf("exact %s transfer: %d bytes with %d-byte pages", image.File.Name, len(image.Data), page)
				}
			}
			if os.Getenv("JABRIDGE_TEST_LIVE_RELEASES") == "1" {
				for _, pid := range []uint16{0x3020, 0x3021, 0x3029, 0x302a} {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					err := verifyUVCCameraRelease(ctx, row.Path, archive, pid)
					cancel()
					if err != nil {
						t.Fatalf("official PanaCast 20 release %04x: %v", pid, err)
					}
				}
			}
			if oracle := os.Getenv("JABRIDGE_TEST_UVC_ORACLE"); oracle != "" {
				data, err := os.ReadFile(oracle)
				if err != nil {
					t.Fatal(err)
				}
				var reference struct {
					Headers []struct {
						File, Header string
						Page         int
						CRC          uint32
						Bytes        int
					}
				}
				if err := json.Unmarshal(data, &reference); err != nil {
					t.Fatal(err)
				}
				if len(reference.Headers) != 4 {
					t.Fatal("incomplete native header oracle")
				}
				for _, record := range reference.Headers {
					image := archive.Main
					if record.File == archive.Boot.File.Name {
						image = archive.Boot
					}
					if record.File != image.File.Name || record.CRC != image.CRC || record.Bytes != len(image.Data) {
						t.Fatal("image or CRC differs from original native header")
					}
					wanted, err := hex.DecodeString(record.Header)
					if err != nil {
						t.Fatal(err)
					}
					got, err := uvcUpdateHeader(image, record.Page)
					if err != nil {
						t.Fatal(err)
					}
					if len(wanted) != 256 || !bytes.Equal(got[:22], wanted[:22]) || !bytes.Equal(got[24:29], wanted[24:29]) {
						t.Fatal("initialized header fields differ from original updater")
					}
				}
			}
		})
	}
	if count != 1 {
		t.Fatalf("expected original PanaCast 20 archive, got %d", count)
	}
}
