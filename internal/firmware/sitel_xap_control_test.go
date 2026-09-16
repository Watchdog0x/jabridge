package firmware

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

type sitelXAPCall struct {
	Op       string   `json:"op"`
	Opcode   byte     `json:"opcode,omitempty"`
	Address  uint16   `json:"address,omitempty"`
	Count    int      `json:"count,omitempty"`
	Words    []uint16 `json:"words,omitempty"`
	Verified bool     `json:"verified,omitempty"`
}

type sitelXAPPeer struct {
	memory  [65536]uint16
	calls   []sitelXAPCall
	failAt  int
	noStop  bool
	noReset bool
}

func newSitelXAPPeer() *sitelXAPPeer {
	p := &sitelXAPPeer{}
	p.memory[0xf831], p.memory[0xf39f], p.memory[0xf3bd] = 1, 0x1230, 0xa400
	return p
}

func (p *sitelXAPPeer) record(ctx context.Context, call sitelXAPCall) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.calls = append(p.calls, call)
	if p.failAt > 0 && len(p.calls) == p.failAt {
		return errors.New("injected processor transport failure")
	}
	return nil
}

func (p *sitelXAPPeer) read(ctx context.Context, address uint16, count int, verified bool) ([]uint16, error) {
	if err := p.record(ctx, sitelXAPCall{Op: "read", Address: address, Count: count, Verified: verified}); err != nil {
		return nil, err
	}
	if address == 0xf82f && !p.noReset {
		p.memory[address] = 0
	}
	if address == 0xf831 && p.noStop {
		p.memory[address] = 0
	}
	return append([]uint16(nil), p.memory[int(address):int(address)+count]...), nil
}

func (p *sitelXAPPeer) write(ctx context.Context, address uint16, words []uint16, verified bool) error {
	if err := p.record(ctx, sitelXAPCall{Op: "write", Address: address, Words: append([]uint16(nil), words...), Verified: verified}); err != nil {
		return err
	}
	copy(p.memory[int(address):], words)
	return nil
}

func (p *sitelXAPPeer) control(ctx context.Context, opcode byte) error {
	return p.record(ctx, sitelXAPCall{Op: "control", Opcode: opcode})
}

func TestSitelXAPStopsOnEveryTransportFailure(t *testing.T) {
	for _, chip := range []string{"elvis", "gordon", "rick"} {
		for _, operation := range []string{"reset", "resume"} {
			peer := newSitelXAPPeer()
			run := func(p *sitelXAPPeer) error {
				x, err := newSitelXAP(&sitelBCCMD{spi: p, chip: chip}, p.control)
				if err != nil {
					return err
				}
				if operation == "reset" {
					return x.resetAndStop(context.Background())
				}
				return x.resume(context.Background())
			}
			if err := run(peer); err != nil {
				t.Fatal(err)
			}
			for fail := 1; fail <= len(peer.calls); fail++ {
				broken := newSitelXAPPeer()
				broken.failAt = fail
				if err := run(broken); err == nil || len(broken.calls) != fail {
					t.Fatal("processor control continued after an uncertain reply", chip, operation, fail, err)
				}
			}
		}
	}
}

func TestSitelXAPPollsAreBounded(t *testing.T) {
	for _, mode := range []string{"stop", "reset", "cancelled"} {
		peer := newSitelXAPPeer()
		peer.noStop, peer.noReset = mode == "stop", mode == "reset"
		x, err := newSitelXAP(&sitelBCCMD{spi: peer, chip: "rick"}, peer.control)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		if mode == "cancelled" {
			cancel()
		}
		err = x.resetAndStop(ctx)
		cancel()
		if err == nil || len(peer.calls) > 40 {
			t.Fatal("unbounded processor-state polling", mode, len(peer.calls), err)
		}
		if mode == "cancelled" && (!errors.Is(err, context.Canceled) || len(peer.calls) != 0) {
			t.Fatal("cancelled reset still touched the processor", err)
		}
	}
	if _, err := newSitelXAP(&sitelBCCMD{spi: newSitelXAPPeer(), chip: "unknown"}, nil); err == nil {
		t.Fatal("unknown processor controls were guessed")
	}
}

func TestLocalSitelXAPOriginalControl(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_BLUECORE_XAP_ORACLE")
	if path == "" {
		t.Skip("set JABRIDGE_TEST_BLUECORE_XAP_ORACLE for original host control-flow checks")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Vectors []struct {
			Chip, Operation string
			Calls           []sitelXAPCall
		}
	}
	if err := json.Unmarshal(data, &oracle); err != nil || len(oracle.Vectors) != 6 {
		t.Fatal("incomplete processor-control oracle", err)
	}
	for _, vector := range oracle.Vectors {
		peer := newSitelXAPPeer()
		x, err := newSitelXAP(&sitelBCCMD{spi: peer, chip: vector.Chip}, peer.control)
		if err != nil {
			t.Fatal(err)
		}
		switch vector.Operation {
		case "reset-and-stop":
			err = x.resetAndStop(context.Background())
		case "resume":
			err = x.resume(context.Background())
		default:
			t.Fatal("unknown original control-flow operation")
		}
		if err != nil || !reflect.DeepEqual(peer.calls, vector.Calls) {
			t.Fatalf("processor-control mismatch %s/%s: %v\nGo: %+v\nNative: %+v", vector.Chip, vector.Operation, err, peer.calls, vector.Calls)
		}
	}
}
