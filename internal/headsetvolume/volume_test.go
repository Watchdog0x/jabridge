package headsetvolume

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type request struct {
	kind, op     byte
	value, index uint16
	data         []byte
}
type peer struct {
	requests []request
	code     int16
	fail     error
	short    bool
}

func (p *peer) Control(ctx context.Context, k, r byte, v, i uint16, d []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	p.requests = append(p.requests, request{k, r, v, i, append([]byte(nil), d...)})
	if p.fail != nil {
		return 0, p.fail
	}
	if k == 0xa2 {
		binary.LittleEndian.PutUint16(d, uint16(p.code))
	}
	if p.short {
		return 1, nil
	}
	return len(d), nil
}

func TestVolumeRequestsAndBounds(t *testing.T) {
	for percent := 0; percent <= 100; percent++ {
		p := &peer{code: -492}
		value, err := Set(context.Background(), p, percent)
		if err != nil || value.Percent != percent || len(p.requests) != 2 {
			t.Fatal(percent, value, err)
		}
		read, write := p.requests[0], p.requests[1]
		if read.kind != 0xa2 || read.op != 0x81 || write.kind != 0x22 || write.op != 1 || write.value != 0x200 || write.index != 0x200 || len(write.data) != 2 {
			t.Fatal("wrong endpoint/master feature request", p.requests)
		}
		code := int16(binary.LittleEndian.Uint16(write.data))
		if code < minimum || code > maximum || code != value.Code {
			t.Fatal("volume escaped profile limits")
		}
		if percent == 0 && code != -12288 || percent == 100 && code != 3072 {
			t.Fatal("endpoints differ from original firmware")
		}
	}
	for _, percent := range []int{-1, 101} {
		p := &peer{}
		if _, err := Set(context.Background(), p, percent); err == nil || len(p.requests) != 0 {
			t.Fatal("invalid volume reached transport")
		}
	}
}

func TestVolumeProbeFailureNeverWrites(t *testing.T) {
	for _, p := range []*peer{{fail: errors.New("disconnected")}, {short: true}, {code: -32768}, {code: 32767}} {
		if _, err := Set(context.Background(), p, 50); err == nil || len(p.requests) != 1 {
			t.Fatal("invalid response reached a write", p.requests, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &peer{}
	if _, err := Set(ctx, p, 50); !errors.Is(err, context.Canceled) || len(p.requests) != 0 {
		t.Fatal("cancelled request reached transport")
	}
}

func descriptorFixture() []byte {
	data := []byte{18, 1, 0, 2, 0, 0, 0, 8, 0x0e, 0x0b, 0x36, 0x0e, 0x11, 1, 1, 2, 3, 1, 9, 2, 0, 0, 2, 1, 0, 0x80, 50}
	parts := [][]byte{{9, 4, 0, 0, 0, 1, 1, 0, 0}, {9, 0x24, 1, 0, 1, 40, 0, 1, 1}, {12, 0x24, 2, 1, 1, 1, 0, 2, 3, 0, 0, 0}, {10, 0x24, 6, 2, 1, 1, 1, 0, 0, 0}, {9, 0x24, 3, 3, 1, 3, 0, 2, 0}, {9, 4, 1, 0, 0, 1, 2, 0, 0}}
	for _, p := range parts {
		data = append(data, p...)
	}
	binary.LittleEndian.PutUint16(data[20:], uint16(len(data)-18))
	return data
}

func TestDescriptorsBindExactAudioPath(t *testing.T) {
	d := descriptorFixture()
	info, err := Inspect(d)
	if err != nil || info.Channels != 2 || info.Interface != 0 || info.AdvertisedVolume {
		t.Fatal(info, err)
	}
	// Audio feature unit2 may omit the Volume bit. The exact firmware proof,
	// not that bit, supplies this model's endpoint compatibility route.
	for _, mutate := range []func([]byte){
		func(b []byte) { b[10]++ }, func(b []byte) { b[12]++ }, func(b []byte) { b[17] = 2 },
		func(b []byte) { b[20]++ }, func(b []byte) { b[34] = 0x20 }, func(b []byte) { b[40] = 2 },
		func(b []byte) { b[57+4] = 4 }, func(b []byte) { b[67+7] = 7 }, func(b []byte) { b[57] = 255 },
	} {
		copyData := append([]byte(nil), d...)
		mutate(copyData)
		if _, err := Inspect(copyData); err == nil {
			t.Fatal("unverified audio path accepted", copyData)
		}
	}
	if Supported(0x24c7, Firmware) || Supported(ProductID, "1.12.0") || !Supported(ProductID, Firmware) {
		t.Fatal("unsupported models/firmware enabled")
	}
}

func TestCapturedAttachmentRejectsReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "headset")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	write := func() {
		for name, value := range map[string]string{"idVendor": "0b0e", "idProduct": "0e36", "bcdDevice": "0111", "busnum": "1", "devnum": "2"} {
			if err := os.WriteFile(filepath.Join(path, name), []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	write()
	a, err := Capture(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	write()
	if err := a.validate(); err == nil {
		t.Fatal("same port and PID replacement accepted")
	}
}
