package firmware

import (
	"context"
	"errors"
	"testing"
	"time"
)

type languageIdentityPeer struct {
	extendedIdentityPeer
	legacyReads, batteryReads int
	wrongTune                 bool
}

func (peer *languageIdentityPeer) Write(ctx context.Context, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(raw) < 7 || raw[4]&0xc0 != 0x40 {
		return errors.New("identity sent a destructive command")
	}
	var data []byte
	switch {
	case raw[5] == 0x13 && raw[6] == 8:
		peer.legacyReads++
		return errors.New("legacy language query is not supported by this split-image headset")
	case raw[5] == 0x13 && raw[6] == 0x74:
		data = []byte{9, 4}
	case raw[5] == 2 && raw[6] == 2:
		data = []byte{2, 1, 0xa3}
	case raw[5] == 2 && raw[6] == 3:
		data = []byte{5, '1', '.', '0', '.', '1'}
	case raw[5] == 2 && raw[6] == 0x21:
		data = []byte{5, '0', '.', '2', '.', '1'}
		if peer.wrongTune {
			data[0] = 9
		}
	case raw[5] == 0x12 && raw[6] == 2:
		peer.batteryReads++
		data = []byte{1, 94}
	default:
		return peer.extendedIdentityPeer.Write(ctx, raw)
	}
	peer.replies = append(peer.replies, append([]byte{5, 0, raw[1], raw[3], 0xc0 | byte(6+len(data)), raw[5], raw[6]}, data...))
	return nil
}

func TestCSRLanguageNativeReadsTunesLanguageVersionAndBattery(t *testing.T) {
	peer := &languageIdentityPeer{extendedIdentityPeer: extendedIdentityPeer{pid: 0x2549, protocol: 17}}
	device := USBDevice{VendorID: JabraVendorID, ProductID: 0x2549, SysPath: "isolated-usb", attachment: &usbAttachment{fingerprint: "before"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := identifyCSRLanguageConnection(ctx, peer, device, 64, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if connection.Identity.Language != 0x409 || connection.Identity.Version != "1.0.1" || connection.Identity.Variant != "01a3" || connection.TunesVersion != "0.2.1" || peer.legacyReads != 0 {
		t.Fatal("incorrect live split-image identity", connection.Identity, connection.TunesVersion, peer.legacyReads)
	}
	level, charging, err := connection.Battery(ctx)
	if err != nil || level != 94 || !charging || peer.batteryReads != 1 {
		t.Fatal("battery was not read from STATUS12/02", level, charging, err)
	}
	if peer.destructive != 0 {
		t.Fatal("discovery wrote device state")
	}
}

func TestCSRLanguageNativeRefusesMalformedTunesVersion(t *testing.T) {
	peer := &languageIdentityPeer{extendedIdentityPeer: extendedIdentityPeer{pid: 0x2549, protocol: 17}, wrongTune: true}
	device := USBDevice{VendorID: JabraVendorID, ProductID: 0x2549, SysPath: "isolated-usb", attachment: &usbAttachment{fingerprint: "before"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := identifyCSRLanguageConnection(ctx, peer, device, 64, func() error { return nil }); err == nil {
		t.Fatal("accepted malformed live voice-pack version")
	}
	if peer.destructive != 0 {
		t.Fatal("malformed identity caused a write")
	}
}
