package firmware

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type otaNoIOTransport struct{ writes, reads int }

func (tr *otaNoIOTransport) Write([]byte) error {
	tr.writes++
	return errors.New("unexpected test transport write")
}
func (tr *otaNoIOTransport) Read(time.Duration) ([]byte, error) {
	tr.reads++
	return nil, errors.New("unexpected test transport read")
}
func (tr *otaNoIOTransport) Close() error { return nil }

func TestOTALegacyOversizeRejectedBeforeAnyDeviceIO(t *testing.T) {
	for _, count := range []int{65536, 71156} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			parts := []OtaPartition{{ID: 0, Data: []byte{1}}, {ID: 1, Data: make([]byte, count*52)}}
			for _, all := range []bool{true, false} {
				tr := &otaNoIOTransport{}
				u := NewCsrOtaUpdater(tr, DefaultCsrOtaOptions(), 63, 8, 0)
				var err error
				if all {
					err = u.FlashAll(parts, [3]byte{1, 0, 0})
				} else {
					err = u.flashPartition(parts[1], [3]byte{1, 0, 0})
				}
				if tr.writes != 0 || tr.reads != 0 || err == nil || !strings.Contains(err.Error(), "legacy chunk limit") {
					t.Errorf("all=%t writes=%d reads=%d error=%v; oversized legacy image must fail before any I/O", all, tr.writes, tr.reads, err)
				}
			}
		})
	}
}

func TestOTAExtendedProtocolsUseSeparateNativeEngine(t *testing.T) {
	for _, protocol := range []int{16, 17} {
		for _, pid := range []uint16{0x253d, 0x253e, 0x253f} {
			if !NativeFirmwareProtocolSupported(pid, protocol) {
				t.Fatalf("missing extended native implementation %d for %04x", protocol, pid)
			}
		}
	}
	if !NativeFirmwareProtocolSupported(0x4052, 4) || NativeFirmwareProtocolSupported(0x24c7, 4) {
		t.Fatal("Sitel implementation must remain model-specific")
	}
}
