package firmware

import (
	"context"
	"testing"
	"time"
)

func TestLink390CSR7NativeIdentityAllowsMissingLanguageOnlyForThisPolicy(t *testing.T) {
	for _, protocol := range []byte{7, 16} {
		peer := &extendedIdentityPeer{pid: 0x2e56, both: true, noLanguage: true, protocol: protocol}
		device := USBDevice{VendorID: JabraVendorID, ProductID: 0x2e56, SysPath: "isolated-port", attachment: &usbAttachment{fingerprint: "isolated-generation"}}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		connection, err := identifyCSRConnection(ctx, peer, device, 64, func() error { return nil }, true)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if err := validateCSR7Connection(connection); (err == nil) != (protocol == 7) {
			t.Fatal("wrong protocol was accepted", protocol, err)
		}
		if connection.Identity.Language != 0 || peer.destructive != 0 {
			t.Fatal(connection.Identity, peer.destructive)
		}
	}
}
