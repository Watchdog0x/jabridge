package firmware

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type extendedUpdatePeer struct {
	csrStagePeer
	permission          byte
	failCommit, badEcho bool
	commands            []string
}

func (p *extendedUpdatePeer) Write(ctx context.Context, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(raw) != 64 {
		return errors.New("expected 64-byte HID report")
	}
	p.commands = append(p.commands, fmt.Sprintf("%02x/%02x", raw[5], raw[6]))
	if raw[5] == 15 && raw[6] != 0x1d {
		err := p.csrStagePeer.Write(ctx, raw)
		if p.badEcho && len(p.queue) > 0 {
			ack := p.queue[len(p.queue)-1]
			if len(ack) == 11 && ack[5] == 0xff {
				ack[10] ^= 1
			}
		}
		return err
	}
	if !p.finished {
		return errors.New("commit before verified image")
	}
	if raw[5] == 13 && raw[6] == 0x11 {
		if raw[4] != 0x46 {
			return errors.New("permission must be a query")
		}
		p.queue = append(p.queue, []byte{5, 0, 8, raw[3], 0xc7, 13, 0x11, p.permission})
		return nil
	}
	if raw[4] != 0x86 || (raw[5] != 15 || raw[6] != 0x1d) && (raw[5] != 13 || raw[6] != 0x12) {
		return errors.New("unknown update command")
	}
	if p.failCommit {
		p.queue = append(p.queue, []byte{5, 0, 8, raw[3], 0xc6, 0xfe, 1})
	} else {
		p.queue = append(p.queue, append([]byte{5, 0, 8, raw[3], 0xca, 0xff}, raw[1:6]...))
	}
	return nil
}

type extendedBackendPeer struct {
	peers          []*extendedUpdatePeer
	current        int
	ids            []csrExtendedIdentity
	closed, opened int
	connectError   bool
}

func (p *extendedBackendPeer) connection() csrExtendedConnection {
	return csrExtendedConnection{IO: p.peers[p.current], Identity: p.ids[p.current], Address: 8, ReportSize: 64, Close: func() error { p.closed++; return nil }}
}
func (p *extendedBackendPeer) Open(ctx context.Context) (csrExtendedConnection, error) {
	p.opened++
	return p.connection(), ctx.Err()
}
func (p *extendedBackendPeer) Reconnect(ctx context.Context, _ csrExtendedIdentity) (csrExtendedConnection, error) {
	if p.connectError {
		return csrExtendedConnection{}, errors.New("device not returned")
	}
	p.current++
	return p.connection(), ctx.Err()
}

func extendedFixture(split bool) (*extendedBackendPeer, csrExtendedPlan) {
	plan := csrExtendedPlan{Protocol: 16, Version: [3]byte{1, 1, 10}, Language: 0x409, Preload: 10, Stages: []csrExtendedInstallStage{{Kind: "firmware", Images: []csrExtendedImage{{Data: bytes.Repeat([]byte{73}, 105)}}}}}
	if split {
		plan.Protocol = 17
		plan.Stages = append([]csrExtendedInstallStage{{Kind: "language", ConfigExit: true, Images: plan.Stages[0].Images}}, plan.Stages...)
		plan.Stages[1].ConfigExit = true
	}
	b := &extendedBackendPeer{}
	for i := 0; i <= len(plan.Stages); i++ {
		version := "1.0.0"
		if i == len(plan.Stages) {
			version = "1.1.10"
		}
		b.ids = append(b.ids, csrExtendedIdentity{PID: 0x253d, Port: "test-port", Attachment: fmt.Sprint(i), Serial: "synthetic-only", Variant: "01-98", Version: version, Language: 0x409, FirmwareProtocols: []byte{16, 17}})
		b.peers = append(b.peers, &extendedUpdatePeer{csrStagePeer: csrStagePeer{image: plan.Stages[0].Images[0].Data, reportSize: 64, chunkBytes: 52}, permission: 1})
	}
	return b, plan
}

func TestExtendedCSRCompleteSequence(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			backend, plan := extendedFixture(split)
			var checkpoints []int
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := runExtendedCSRUpdate(ctx, backend, plan, func(stage int) error { checkpoints = append(checkpoints, stage); return nil }, nil)
			if err != nil || backend.current != len(plan.Stages) || backend.closed != len(plan.Stages)+1 || len(checkpoints) != len(plan.Stages) {
				t.Fatal(err, backend.current, backend.closed, checkpoints)
			}
			for _, p := range backend.peers[:len(plan.Stages)] {
				expect := []string{"0f/1d"}
				if split {
					expect = append(expect, "0d/11", "0d/12")
				}
				got := p.commands[len(p.commands)-len(expect):]
				if fmt.Sprint(got) != fmt.Sprint(expect) {
					t.Fatal(got, expect)
				}
			}
		})
	}
}

func TestExtendedCSRStopsOnFailure(t *testing.T) {
	for _, name := range []string{"protocol", "commit", "permission", "echo", "reconnect", "version", "language", "serial", "port", "attachment", "pid", "variant", "checkpoint", "cancel"} {
		t.Run(name, func(t *testing.T) {
			backend, plan := extendedFixture(true)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			checkpoint := func(int) error { return nil }
			switch name {
			case "protocol":
				backend.ids[0].FirmwareProtocols = []byte{7}
			case "commit":
				backend.peers[0].failCommit = true
			case "permission":
				backend.peers[0].permission = 0
			case "echo":
				backend.peers[0].badEcho = true
			case "reconnect":
				backend.connectError = true
			case "version":
				backend.ids[1].Version = "9.9.9"
			case "language":
				backend.ids[1].Language = 0x407
			case "serial":
				backend.ids[1].Serial = "replacement"
			case "port":
				backend.ids[1].Port = "another-port"
			case "attachment":
				backend.ids[1].Attachment = backend.ids[0].Attachment
			case "pid":
				backend.ids[1].PID++
			case "variant":
				backend.ids[1].Variant = "other"
			case "checkpoint":
				checkpoint = func(int) error { return errors.New("disk full") }
			case "cancel":
				cancel()
			}
			if err := runExtendedCSRUpdate(ctx, backend, plan, checkpoint, nil); err == nil {
				t.Fatal("failure accepted")
			}
			if len(backend.peers[1].commands) != 0 {
				t.Fatal("next stage started after failure")
			}
			if name == "permission" {
				for _, op := range backend.peers[0].commands {
					if op == "0d/12" {
						t.Fatal("exit sent after denial")
					}
				}
			}
			if (name == "checkpoint" || name == "cancel") && len(backend.peers[0].commands) != 0 {
				t.Fatal("write before checkpoint or after cancellation")
			}
		})
	}
}

func TestExtendedCSRPlanSelection(t *testing.T) {
	manifest := &BuildVector{Version: "1.1.10", MaxPreloadCount: 10}
	english := GnVFile{Name: "en.bin", Version: "1.1.10", Target: "headset", Content: "firmware"}
	english.Language.ID = "0x0409"
	german := english
	german.Name = "de.bin"
	german.Language.ID = "0x0407"
	manifest.Files = []GnVFile{german, english}
	files := map[string][]byte{"en.bin": {1, 2, 3}, "de.bin": {4, 5, 6}}
	plan, err := prepareExtendedCSRPlan(manifest, files, 16, 0x409, true, false)
	if err != nil || len(plan.Stages) != 1 || !bytes.Equal(plan.Stages[0].Images[0].Data, files["en.bin"]) {
		t.Fatal(plan, err)
	}
	files["en.bin"][0] = 99
	if plan.Stages[0].Images[0].Data[0] != 1 {
		t.Fatal("plan aliases mutable input")
	}
	for _, language := range []uint16{0, 0x411} {
		if _, err := prepareExtendedCSRPlan(manifest, files, 16, language, true, false); err == nil {
			t.Fatal("unknown language silently selected")
		}
	}
	manifest.Files = append(manifest.Files, english)
	if _, err := prepareExtendedCSRPlan(manifest, files, 16, 0x409, true, false); err == nil {
		t.Fatal("duplicate language selected")
	}
}
