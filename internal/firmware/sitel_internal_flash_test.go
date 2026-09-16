package firmware

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func internalTestImage(chip string) *sitelBluecoreImage {
	im := &sitelBluecoreImage{Chip: chip, Sectors: make(map[uint16][]uint16)}
	for _, sector := range []uint16{0, 250} {
		words := make([]uint16, 4096)
		for i := range words {
			words[i] = uint16(i*13) ^ sector ^ 0x2c39
		}
		im.Sectors[sector] = words
	}
	return im
}
func TestInternalFlashStandaloneTransferAndProtectedSectors(t *testing.T) {
	for _, revision := range []uint16{0x28, 0x35} {
		s := newInternalFlashSim(revision)
		s.cpu.ticks = 65520
		protected := append([]uint16(nil), s.cpu.flash[251*4096:]...)
		untouched := append([]uint16(nil), s.cpu.flash[4096:8192]...)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		f, err := prepareSitelInternalFlash(ctx, s, s, s.sleep)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		image := internalTestImage(f.chip)
		checkpoints := 0
		last := 0
		err = f.transfer(ctx, image, func(uint16) error { checkpoints++; return nil }, func(done, total int) {
			if done <= last || done > total {
				t.Error("invalid flash progress")
			}
			last = done
		})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		for sector, wanted := range image.Sectors {
			if !slices.Equal(s.cpu.flash[int(sector)*4096:int(sector+1)*4096], wanted) {
				t.Fatal("flash bytes differ from input", sector)
			}
		}
		if checkpoints != 2 || last != 2 || s.boots != 0 || !slices.Equal(s.cpu.flash[251*4096:], protected) || !slices.Equal(s.cpu.flash[4096:8192], untouched) {
			t.Fatal("protected or untouched flash changed")
		}
		// A second pass must compare actual flash, not erase already verified data.
		if err := f.transfer(ctx, image, func(uint16) error { t.Fatal("complete sector erased again"); return nil }, nil); err != nil {
			t.Fatal(err)
		}
		if err := f.bootApplication(ctx); err != nil {
			t.Fatal(err)
		}
		if s.boots != 1 || s.cpu.control != 0 {
			t.Fatal("application did not start from an idle controller")
		}
		cancel()
	}
}
func TestInternalFlashRejectsWrongIdentityCodeAndImage(t *testing.T) {
	for _, mode := range []string{"unknown-chip", "wrong-generation", "corrupt-code", "wrong-image-chip", "protected-sector", "bad-sector-size", "checkpoint"} {
		t.Run(mode, func(t *testing.T) {
			s := newInternalFlashSim(0x35)
			if mode == "unknown-chip" {
				s.debug[0xfe81] = 0x34
			}
			if mode == "wrong-generation" {
				s.debug[0xff9a] = 0x50e1
			}
			if mode == "corrupt-code" {
				s.corruptCode = true
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			f, err := prepareSitelInternalFlash(ctx, s, s, s.sleep)
			if mode == "unknown-chip" || mode == "wrong-generation" || mode == "corrupt-code" {
				if err == nil || len(s.cpu.erases) != 0 || s.cpu.running {
					t.Fatal("unverified processor/program was started", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			im := internalTestImage(f.chip)
			switch mode {
			case "wrong-image-chip":
				im.Chip = "gordon"
			case "protected-sector":
				im.Sectors[251] = make([]uint16, 4096)
			case "bad-sector-size":
				im.Sectors[0] = []uint16{1, 2, 3}
			}
			saveError := errors.New("checkpoint failed")
			err = f.transfer(ctx, im, func(uint16) error {
				if mode == "checkpoint" {
					return saveError
				}
				return nil
			}, nil)
			if err == nil || len(s.cpu.erases) != 0 || s.cpu.programs != 0 {
				t.Fatal("invalid plan changed flash", err)
			}
			if mode == "checkpoint" && !errors.Is(err, saveError) {
				t.Fatal("checkpoint error lost", err)
			}
		})
	}
}
func TestInternalFlashLostAcknowledgementAndRecovery(t *testing.T) {
	for _, command := range []uint16{2, 3} {
		s := newInternalFlashSim(0x28)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		f, err := prepareSitelInternalFlash(ctx, s, s, s.sleep)
		if err != nil {
			t.Fatal(err)
		}
		im := internalTestImage(f.chip)
		s.failCommand, s.failOnce = command, true
		if err := f.transfer(ctx, im, func(uint16) error { return nil }, nil); err == nil {
			t.Fatal("lost command ACK was hidden")
		}
		if s.cpu.control != 0 || !s.cpu.idle || s.boots != 0 {
			t.Fatal("failed transfer halted or booted an active controller")
		}
		// There is deliberately no application/BCCMD symbol table in this model.
		// Recovery must work through raw identified-chip SPI and its own RAM code.
		s.cpu.ram[0x80], s.cpu.ram[0x81] = 0, 0
		retry, err := prepareSitelInternalFlash(ctx, s, s, s.sleep)
		if err != nil {
			t.Fatal(err)
		}
		if err := retry.transfer(ctx, im, func(uint16) error { return nil }, nil); err != nil {
			t.Fatal(err)
		}
		expectedErases := 1
		if command == 2 {
			expectedErases = 2
		}
		if s.cpu.erases[0] != expectedErases || s.cpu.erases[250] != 1 {
			t.Fatal("completed flash command was replayed", command, s.cpu.erases)
		}
	}
}
func TestInternalFlashCancellationSettlesCurrentPulse(t *testing.T) {
	s := newInternalFlashSim(0x35)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f, err := prepareSitelInternalFlash(ctx, s, s, s.sleep)
	if err != nil {
		t.Fatal(err)
	}
	s.cancelOnCommand = cancel
	err = f.transfer(ctx, internalTestImage(f.chip), func(uint16) error { return nil }, nil)
	if !errors.Is(err, context.Canceled) || s.cpu.control != 0 || !s.cpu.idle || s.boots != 0 {
		t.Fatal("cancellation interrupted a flash pulse", err)
	}
	if s.cpu.programs != 4096 {
		t.Fatal("current programming operation was not allowed to settle", s.cpu.programs)
	}
}
func TestInternalFlashReadbackFailureDoesNotBoot(t *testing.T) {
	s := newInternalFlashSim(0x28)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	f, err := prepareSitelInternalFlash(ctx, s, s, s.sleep)
	if err != nil {
		t.Fatal(err)
	}
	s.badReadback = true
	if err := f.transfer(ctx, internalTestImage(f.chip), func(uint16) error { return nil }, nil); err == nil || s.boots != 0 {
		t.Fatal("failed readback was accepted", err)
	}
}

func internalFlashWireTransport(t *testing.T, sim *internalFlashSim) (*sitelSPI, *sitelSPI) {
	t.Helper()
	peer := &sitelBootPeer{world: makeEngageWorld(false), dropAck: 1}
	peer.onPacket = func(packet []byte) ([]byte, error) { return simulateSitelSPIPacket(packet, sim) }
	layout := sitelHIDLayout{ReportID: 10, ReportBytes: 64, MaxMessage: 1024}
	link := &sitelLink{io: peer, in: layout, out: layout, timeout: time.Millisecond}
	if err := link.start(context.Background()); err != nil {
		t.Fatal(err)
	}
	slow, err := newSitelSPI(&sitelRequester{link: link, timeout: time.Second}, 23, 0)
	if err != nil {
		t.Fatal(err)
	}
	return slow, slow.fastAccess()
}

func TestInternalFlashThroughHIDFraming(t *testing.T) {
	s := newInternalFlashSim(0x28)
	slow, fast := internalFlashWireTransport(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f, err := prepareSitelInternalFlash(ctx, slow, fast, s.sleep)
	if err != nil {
		t.Fatal(err)
	}
	im := internalTestImage(f.chip)
	if err := f.transfer(ctx, im, func(uint16) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	for sector, want := range im.Sectors {
		if !slices.Equal(s.cpu.flash[int(sector)*4096:int(sector+1)*4096], want) {
			t.Fatal("HID/SPI/CPU/controller path changed the firmware bytes")
		}
	}
}

func TestInternalFlashSerializesTransferBootAndCancellation(t *testing.T) {
	for _, failCheckpoint := range []bool{false, true} {
		s := newInternalFlashSim(0x35)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		f, err := prepareSitelInternalFlash(ctx, s, s, s.sleep)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		image := internalTestImage(f.chip)
		entered, proceed := make(chan struct{}), make(chan struct{})
		transferDone, bootDone := make(chan error, 1), make(chan error, 1)
		first := true
		go func() {
			transferDone <- f.transfer(ctx, image, func(uint16) error {
				if first {
					first = false
					close(entered)
					<-proceed
				}
				if failCheckpoint {
					return errors.New("injected checkpoint failure")
				}
				return nil
			}, nil)
		}()
		select {
		case <-entered:
		case err := <-transferDone:
			cancel()
			t.Fatal("transfer failed before its checkpoint", err)
		case <-ctx.Done():
			cancel()
			t.Fatal("transfer did not reach its checkpoint")
		}
		go func() { bootDone <- f.bootApplication(ctx) }()
		waiting, stopWaiting := context.WithCancel(ctx)
		stopped := make(chan error, 1)
		go func() { stopped <- f.transfer(waiting, image, func(uint16) error { return nil }, nil) }()
		stopWaiting()
		if err := <-stopped; !errors.Is(err, context.Canceled) {
			t.Fatal("waiting operation ignored cancellation", err)
		}
		select {
		case err := <-bootDone:
			t.Fatal("boot overlapped an active transfer", err)
		default:
		}
		if s.boots != 0 || len(s.cpu.erases) != 0 {
			t.Fatal("another operation reached the shared transport")
		}
		close(proceed)
		transferErr, bootErr := <-transferDone, <-bootDone
		if failCheckpoint {
			if transferErr == nil || bootErr == nil || s.boots != 0 {
				t.Fatal("failed transfer was followed by boot", transferErr, bootErr)
			}
		} else {
			if transferErr != nil || bootErr != nil || s.boots != 1 {
				t.Fatal("serialized transfer/boot failed", transferErr, bootErr)
			}
			if err := f.transfer(ctx, image, func(uint16) error { return nil }, nil); err == nil {
				t.Fatal("RAM worker object reused after application boot")
			}
			if err := f.bootApplication(ctx); err == nil {
				t.Fatal("closed flash session booted a second time")
			}
		}
		cancel()
	}
}

func simulateSitelSPIPacket(packet []byte, wordsIO sitelSPIWords) ([]byte, error) {
	if len(packet) < 10 || packet[0] != 1 || packet[1] != 0 || packet[4] != 15 || packet[3] != 0x40 && packet[3] != 0x80 {
		return nil, errors.New("invalid internal-flash SPI wire header")
	}
	address := uint16(packet[6]) | uint16(packet[7])<<8
	count := int(packet[8]) | int(packet[9])<<8
	if count <= 0 || count%2 != 0 || count > 1014 {
		return nil, errors.New("invalid SPI byte count")
	}
	op := packet[5]
	if packet[3] == 0x40 {
		if len(packet) != 10 || op != 0x0b && op != 0x0d && op != 0x12 && op != 0x14 {
			return nil, errors.New("invalid SPI read request")
		}
		words, err := wordsIO.read(context.Background(), address, count/2, op == 0x0d || op == 0x14)
		if err != nil {
			return nil, err
		}
		reply := []byte{0, 1, packet[2], 0xc0, 15, op, byte(count), byte(count >> 8)}
		for _, word := range words {
			reply = append(reply, byte(word), byte(word>>8))
		}
		return reply, nil
	}
	if len(packet) != 10+count || op != 0x0c && op != 0x0e && op != 0x13 && op != 0x15 {
		return nil, errors.New("invalid SPI write request")
	}
	words := make([]uint16, count/2)
	for i := range words {
		words[i] = uint16(packet[10+2*i]) | uint16(packet[11+2*i])<<8
	}
	if err := wordsIO.write(context.Background(), address, words, op == 0x0e || op == 0x15); err != nil {
		return nil, err
	}
	return []byte{0, 1, packet[2], 0xc0, 0xff, 0}, nil
}
