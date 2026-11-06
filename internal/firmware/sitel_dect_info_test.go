package firmware

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Synthetic device-info responses, not a claim that firmware 4.4.1 supports
// this command on the normal management transport.
type dectInfoPeer struct {
	packets [][]byte
	queue   [][]byte
	length  int
	fault   string
	address byte
	readErr error
}

func (p *dectInfoPeer) Write(_ context.Context, request []byte) error {
	p.packets = append(p.packets, bytes.Clone(request))
	address := request[1]
	if p.fault == "timeout" && address == p.address {
		p.readErr = context.DeadlineExceeded
		return nil
	}
	if p.fault == "rejected" && address == p.address {
		p.queue = append(p.queue, []byte{5, 0, address, request[3], 0xc5, 0xfe})
		return nil
	}
	length := p.length
	if length == 0 {
		length = 19
	}
	data := make([]byte, length)
	if length >= 17 {
		image := map[byte]uint32{1: 0x0b0e1111, 10: 0x0b0e1116, 3: 0x0b0e1117}[address]
		if p.fault == "wrong-id" && address == p.address {
			image++
		}
		binary.LittleEndian.PutUint32(data, image)
		binary.LittleEndian.PutUint32(data[4:], 0x100)
		binary.LittleEndian.PutUint32(data[8:], 256)
		binary.LittleEndian.PutUint32(data[12:], 128)
		data[16] = 1
		if p.fault == "geometry" && address == p.address {
			clear(data[8:12])
		}
		if p.fault == "mode" && address == p.address {
			data[16] = 4
		}
	}
	reply := append([]byte{5, 0, address, request[3], 0xc0 | byte(6+length), 15, 0}, data...)
	if p.fault == "correlation" && address == p.address {
		wrongAddress, wrongSequence := bytes.Clone(reply), bytes.Clone(reply)
		wrongAddress[2] = 8
		wrongSequence[3]++
		p.queue = append(p.queue, wrongAddress, wrongSequence)
	}
	p.queue = append(p.queue, reply)
	return nil
}
func (p *dectInfoPeer) Read(context.Context) ([]byte, error) {
	if p.readErr != nil {
		err := p.readErr
		p.readErr = nil
		return nil, err
	}
	if len(p.queue) == 0 {
		return nil, errors.New("fixture has no reply")
	}
	reply := p.queue[0]
	p.queue = p.queue[1:]
	return reply, nil
}

func TestDECTImageInfoStrictObservationsAndSingleAttempts(t *testing.T) {
	for _, tc := range []struct {
		name, fault, want string
		length            int
	}{
		{"17-byte", "", "measured runtime GetInfo image ID 0b0e:1116", 17},
		{"19-byte", "", "measured runtime GetInfo image ID 0b0e:1116", 19},
		{"correlation", "correlation", "measured runtime GetInfo image ID 0b0e:1116", 19},
		{"NAK", "rejected", "rejected", 19},
		{"timeout", "timeout", "timeout", 19},
		{"truncated", "", "invalid device-info", 16},
		{"extra", "", "invalid device-info", 20},
		{"geometry", "geometry", "invalid device-info", 19},
		{"mode", "mode", "invalid device-info", 19},
		{"wrong-image", "wrong-id", "does not match the expected image", 19},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, device := newReporterDECT(false)
			peer := &dectInfoPeer{fault: tc.fault, address: 10, length: tc.length}
			checks, err := inspectDECTImageInfo(context.Background(), device, peer, func() error { return nil }, 100*time.Millisecond, time.Second)
			if err != nil || len(checks) != 3 || !strings.Contains(checks[1].Detail, tc.want) {
				t.Fatalf("unexpected observations: %+v %v", checks, err)
			}
			var addresses []byte
			for i, packet := range peer.packets {
				want := make([]byte, 64)
				copy(want, []byte{5, []byte{1, 10, 3}[i], 0, byte(i + 1), 0x46, 15, 0})
				if !bytes.Equal(packet, want) {
					t.Fatalf("unexpected request: %x", packet)
				}
				addresses = append(addresses, packet[1])
			}
			if !reflect.DeepEqual(addresses, []byte{1, 10, 3}) {
				t.Fatal("query repeated or omitted", addresses)
			}
			if strings.Contains(checks[1].Detail, device.Serial) {
				t.Fatal("serial leaked")
			}
		})
	}
}

func TestDECTImageInfoGuardRejectsForbiddenRequests(t *testing.T) {
	t.Setenv(HardwareWriteEnv, HardwareWriteAck)
	oldRisk := commandLineRiskAccepted.Load()
	commandLineRiskAccepted.Store(true)
	t.Cleanup(func() { commandLineRiskAccepted.Store(oldRisk) })
	for _, mutate := range []func([]byte){
		func(p []byte) { p[1] = 8 }, func(p []byte) { p[1] = 2 },
		func(p []byte) { p[4] = 0x86 }, func(p []byte) { p[4] = 0x47 },
		func(p []byte) { p[5] = 7 }, func(p []byte) { p[5] = 2 },
		func(p []byte) { p[6] = 1 }, func(p []byte) { p[6] = 2 },
		func(p []byte) { p[6] = 3 }, func(p []byte) { p[6] = 5 },
		func(p []byte) { p[7] = 3 }, func(p []byte) { p[63] = 1 },
	} {
		peer := &dectInfoPeer{}
		guard := &dectInfoReadOnlyIO{next: peer, validate: func() error { return nil }}
		packet := make([]byte, 64)
		copy(packet, []byte{5, 1, 0, 1, 0x46, 15, 0})
		mutate(packet)
		if err := guard.Write(context.Background(), packet); !errors.Is(err, errManagementNotSent) || len(peer.packets) != 0 {
			t.Fatal("forbidden request reached transport", packet, err)
		}
	}
	for _, field := range []int{1, 4, 5, 6} {
		for value := 0; value < 256; value++ {
			if field == 1 && (value == 1 || value == 10 || value == 3) || field == 4 && value == 0x46 || field == 5 && value == 15 || field == 6 && value == 0 {
				continue
			}
			peer := &dectInfoPeer{}
			guard := &dectInfoReadOnlyIO{next: peer, validate: func() error { return nil }}
			packet := make([]byte, 64)
			copy(packet, []byte{5, 1, 0, 1, 0x46, 15, 0})
			packet[field] = byte(value)
			if err := guard.Write(context.Background(), packet); !errors.Is(err, errManagementNotSent) || len(peer.packets) != 0 {
				t.Fatalf("forbidden field %d value %02x reached transport", field, value)
			}
		}
	}
	for _, length := range []int{0, 6, 7, 33, 63, 65} {
		peer := &dectInfoPeer{}
		guard := &dectInfoReadOnlyIO{next: peer, validate: func() error { return nil }}
		packet := make([]byte, length)
		copy(packet, []byte{5, 1, 0, 1, 0x46, 15, 0})
		if err := guard.Write(context.Background(), packet); !errors.Is(err, errManagementNotSent) || len(peer.packets) != 0 {
			t.Fatal("malformed canonical length accepted", length, err)
		}
	}
}

type blockedDECTInfoPeer struct{ dectInfoPeer }

func (p *blockedDECTInfoPeer) Read(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDECTImageInfoBudgetCancellationAndState(t *testing.T) {
	t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", t.TempDir())
	risk := commandLineRiskAccepted.Load()
	_, _, device := newReporterDECT(false)
	peer := &blockedDECTInfoPeer{}
	start := time.Now()
	checks, err := inspectDECTImageInfo(context.Background(), device, peer, func() error { return nil }, time.Second, 5*time.Millisecond)
	if err != nil || len(checks) != 3 || len(peer.packets) != 1 || time.Since(start) > 250*time.Millisecond || checks[1].State != "NOT TESTED" || checks[2].State != "NOT TESTED" {
		t.Fatalf("budget or single attempt violated: %+v %v packets=%d", checks, err, len(peer.packets))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fresh := &dectInfoPeer{}
	checks, err = inspectDECTImageInfo(ctx, device, fresh, func() error { return nil }, time.Second, time.Second)
	if err != nil || len(checks) != 3 || len(fresh.packets) != 0 {
		t.Fatal("cancelled check sent traffic", checks, err)
	}
	entries, err := os.ReadDir(os.Getenv("JABRIDGE_FIRMWARE_STATE_DIR"))
	if err != nil || len(entries) != 0 || commandLineRiskAccepted.Load() != risk {
		t.Fatal("read experiment changed persistent state or permissions", entries, err)
	}
}

func TestDECTImageInfoRejectsUnsupportedOrChanged(t *testing.T) {
	for _, pid := range []uint16{0x1111, 0x1110, 0x24c9, 0x4051} {
		_, _, device := newReporterDECT(false)
		device.ProductID = pid
		peer := &dectInfoPeer{}
		if _, err := inspectDECTImageInfo(context.Background(), device, peer, func() error { return nil }, time.Second, time.Second); err == nil || len(peer.packets) != 0 {
			t.Fatal("unsupported PID reached transport", pid, err)
		}
	}
	for _, kind := range []string{"vendor", "dongle", "unbound"} {
		_, _, device := newReporterDECT(false)
		switch kind {
		case "vendor":
			device.VendorID = 0xffff
		case "dongle":
			device.ViaDongle = true
		case "unbound":
			device.attachment = nil
		}
		peer := &dectInfoPeer{}
		if _, err := inspectDECTImageInfo(context.Background(), device, peer, func() error { return nil }, time.Second, time.Second); err == nil || len(peer.packets) != 0 {
			t.Fatal("unsupported binding reached transport", kind, err)
		}
	}
	for _, at := range []int{1, 2, 3} {
		_, _, device := newReporterDECT(false)
		peer := &dectInfoPeer{}
		calls := 0
		validate := func() error {
			calls++
			if calls == at {
				return errors.New("replacement")
			}
			return nil // A later restored fingerprint must not salvage stale data.
		}
		checks, err := inspectDECTImageInfo(context.Background(), device, peer, validate, time.Second, time.Second)
		if !errors.Is(err, ErrDECTInspectionChanged) || checks != nil || len(peer.packets) > 1 {
			t.Fatal("changed device result or further queries escaped", at, checks, err)
		}
	}
}

func TestDECTImageInfoPublicFixtureMatchesRealInspection(t *testing.T) {
	_, _, device := newReporterDECT(false)
	checks, err := inspectDECTImageInfo(context.Background(), device, &dectInfoPeer{}, func() error { return nil }, time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for i := range checks {
		checks[i].ElapsedMS = 0
	}
	data, err := json.MarshalIndent(checks, "", "  ")
	if err != nil || bytes.Contains(data, []byte(device.Serial)) {
		t.Fatal("public observation serialization failed or leaked", err)
	}
	expected, err := os.ReadFile("testdata/dect-image-info-public-result.json")
	if err != nil || strings.TrimSpace(string(expected)) != string(data) {
		t.Fatalf("public fixture does not match real GetInfo observations: %v\n%s", err, data)
	}
}
