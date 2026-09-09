package firmware

import (
	"bytes"
	"strconv"
	"testing"
)

func TestControlAssemblerInvalidLayoutNeverPanics(t *testing.T) {
	for _, size := range []int{-1, 0, 1, 6, 66} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			defer func() {
				if value := recover(); value != nil {
					t.Errorf("malformed layout panicked: %v", value)
				}
			}()
			a := NewControlPacketAssembler(ControlLayout{InputID: 5, InputBytes: size})
			if _, err := a.Push([]byte{5, 0, 8, 1, 0xc5, 0xff}); err == nil {
				t.Fatal("invalid layout accepted")
			}
		})
	}
}

func FuzzControlAssemblerRecovery(f *testing.F) {
	f.Add(33, []byte{2, 0, 8, 0x31, 0xc5, 0xff}, []byte{2})
	f.Add(0, []byte{2, 0, 8, 0x31, 0xc5, 0xff}, []byte{})
	f.Fuzz(func(t *testing.T, size int, first, second []byte) {
		if len(first) > 8193 || len(second) > 8193 {
			return
		}
		a := NewControlPacketAssembler(ControlLayout{InputID: 2, InputBytes: size})
		for _, frame := range [][]byte{first, second} {
			packet, err := a.Push(frame)
			if err != nil && len(a.state.packet) != 0 {
				t.Fatal("error retained partial packet")
			}
			if len(a.state.packet) > 63 || len(packet) > 64 {
				t.Fatal("unbounded management packet")
			}
		}
	})
}

func TestControlAssemblerErrorDropsPartialPacket(t *testing.T) {
	for _, bad := range [][]byte{{2}, make([]byte, 34)} {
		bad[0] = 2
		a := NewControlPacketAssembler(ControlLayout{InputID: 2, InputBytes: 33})
		first := make([]byte, 33)
		copy(first, []byte{2, 0, 8, 0x31, 0xe8, 0x13, 0x2a})
		if got, err := a.Push(first); got != nil || err != nil {
			t.Fatal(got, err)
		}
		if _, err := a.Push(bad); err == nil {
			t.Fatal("malformed continuation accepted")
		}
		ack := make([]byte, 33)
		copy(ack, []byte{2, 0, 8, 0x32, 0xc5, 0xff})
		got, err := a.Push(ack)
		if err != nil || !bytes.Equal(got, []byte{5, 0, 8, 0x32, 0xc5, 0xff}) {
			t.Fatalf("partial packet contaminated later ACK: %x %v", got, err)
		}
	}
}
