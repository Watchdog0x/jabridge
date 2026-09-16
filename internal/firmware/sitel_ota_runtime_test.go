package firmware

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

type otaRuntimePeer struct {
	parentAddress   byte
	canonicalParent bool
	queue           [][]byte
	badSerial       bool
	mutations       int
}

func (p *otaRuntimePeer) Write(ctx context.Context, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(raw) != 64 || raw[0] != 5 || raw[4]&0xc0 != 0x40 || raw[5] != 2 || raw[4]&63 != 6 {
		p.mutations++
		return errors.New("identity probing attempted a mode change")
	}
	address, op := raw[1], raw[6]
	if address != 4 && address != p.parentAddress {
		p.queue = append(p.queue, []byte{5, 0, address, raw[3], 0xc6, 0xfe, 1})
		return nil
	}
	var data []byte
	switch op {
	case 0x11:
		pid := uint16(0x1131)
		if p.canonicalParent {
			pid = 0x1130
		}
		if address == 4 {
			pid = 0x1116
		}
		data = []byte{byte(pid), byte(pid >> 8)}
	case 1:
		text := "fixture-parent"
		if address == 4 {
			text = "fixture-wireless"
		}
		if p.badSerial && address == 4 {
			text = ""
		}
		data = append([]byte{byte(len(text))}, text...)
	case 2:
		data = []byte{2, 1, 0x72}
	case 3:
		data = append([]byte{6}, "5.17.0"...)
	case 0x13:
		data = []byte{0x16, 0x11}
	case 0x14:
		data = []byte{12}
	case 0x21:
		data = append([]byte{6}, "5.17.1"...)
	case 0x22:
		data = []byte{1}
	default:
		return errors.New("unexpected identity query")
	}
	p.queue = append(p.queue, append([]byte{5, 0, address, raw[3], 0xc0 | byte(6+len(data)), 2, op}, data...))
	return nil
}
func (p *otaRuntimePeer) Read(ctx context.Context) ([]byte, error) {
	if len(p.queue) == 0 {
		return nil, context.DeadlineExceeded
	}
	data := p.queue[0]
	p.queue = p.queue[1:]
	return data, nil
}

func TestWirelessEngageIdentityUsesParentAndChildAddresses(t *testing.T) {
	for _, address := range []byte{1, 8} {
		for _, canonical := range []bool{false, true} {
			peer := &otaRuntimePeer{parentAddress: address, canonicalParent: canonical}
			r := &sitelRuntime{io: peer}
			parent := USBDevice{VendorID: JabraVendorID, ProductID: 0x1131, SysPath: "/isolated/1-2", attachment: &usbAttachment{fingerprint: "attachment"}}
			target, err := r.identifyOTA(context.Background(), parent)
			if err != nil || target.ParentAddress != address || target.Child.PID != 0x1116 || target.Child.Serial != "fixture-wireless" || target.Region != 1 || target.TunesVersion != "5.17.1" || target.childIdentity() == "" || peer.mutations != 0 {
				t.Fatal("wrong wireless target identity", err)
			}
			peer.badSerial = true
			if _, err := r.identifyOTA(context.Background(), parent); err == nil {
				t.Fatal("serial-less child was accepted for wireless recovery")
			}
		}
	}
}

func TestWirelessEngagePermissionRequiresAuthorization(t *testing.T) {
	previous := commandLineRiskAccepted.Swap(false)
	defer commandLineRiskAccepted.Store(previous)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	backend := nativeSitelOTABackend{}
	for _, action := range []func(context.Context, sitelOTATarget) error{backend.enter, backend.exit} {
		if err := action(ctx, sitelOTATarget{}); err == nil {
			t.Fatal("mode-changing permission command was not gated")
		}
	}
}

func TestWirelessEngageImageSetRejectsCrossedRoutes(t *testing.T) {
	for _, change := range []func(*sitelOTAWorld){
		func(s *sitelOTAWorld) { s.archive.Images[0].File.GNPAddress = "1" },
		func(s *sitelOTAWorld) { s.archive.Images[1].File.SitelHidTargetID = "27" },
		func(s *sitelOTAWorld) { s.archive.Images[1].File.UpdateOrder = 1 },
		func(s *sitelOTAWorld) { s.archive.Images[1].File.RegionID = "" },
		func(s *sitelOTAWorld) {
			binary.LittleEndian.PutUint32(s.archive.Images[1].Segments[0].Data[30:], 0x0b0e1130)
		},
	} {
		s := makeSitelOTAWorld()
		change(s)
		state := otaTestState()
		if err := s.run(context.Background(), &state); err == nil || s.w.erases != 0 {
			t.Fatal("invalid wireless image reached erase", err)
		}
	}
}
