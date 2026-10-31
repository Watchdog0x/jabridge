package firmware

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type languageUpdatePeer struct {
	afterCommitWrite func()
	afterCommitACK   func()
	commitACKRead    bool
	languageStagePeer
	failCommit, failExit                             bool
	permission                                       byte
	committed, serialRead, permissionRead, restarted bool
	commands                                         [][2]byte
}

func (peer *languageUpdatePeer) Read(ctx context.Context) ([]byte, error) {
	result, err := peer.languageStagePeer.Read(ctx)
	if err == nil && peer.committed && !peer.commitACKRead && len(result) > 5 && result[5] == 0xff {
		peer.commitACKRead = true
		if peer.afterCommitACK != nil {
			peer.afterCommitACK()
		}
	}
	return result, err
}

func TestCSRLanguageCancelAfterCommitStillRestartsOnce(t *testing.T) {
	backend, plan := languageDriverFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend.peer.afterCommitACK = cancel
	err := runCSRLanguageUpdate(ctx, backend, plan, "", "", "", func(string, csrLanguageConnection) error { return nil }, nil)
	if !errors.Is(err, context.Canceled) || !backend.peer.serialRead || !backend.peer.permissionRead || !backend.peer.restarted {
		t.Fatal("cancellation skipped the critical restart", err, backend.peer.commands)
	}
	counts := map[[2]byte]int{}
	for _, command := range backend.peer.commands {
		counts[command]++
	}
	for _, command := range [][2]byte{{15, 0x1d}, {13, 0x11}, {13, 0x12}} {
		if counts[command] != 1 {
			t.Fatal("critical command was skipped or repeated", command, counts[command])
		}
	}
}

func TestCSRLanguageUnchangedRecoveryCanStartFresh(t *testing.T) {
	backend, plan := languageDriverFixture()
	before := backend.before
	backend.before.Attachment = "after-replug"
	recovery := &csrLanguageRecovery{SourceAttachment: before.Attachment, SourceLanguage: before.Language, SourceTunesVersion: "0.2.1", TuneVersion: "0.2.1"}
	err := runCSRLanguageUpdate(context.Background(), backend, plan, csrLanguageCommitting, before.Attachment, extendedRecoveryIdentity(before), func(string, csrLanguageConnection) error {
		t.Fatal("read-only recovery attempted a write checkpoint")
		return nil
	}, nil, recovery)
	if !errors.Is(err, errCSRLanguageNotApplied) || len(backend.peer.commands) != 0 {
		t.Fatal("unchanged rebooted headset could not leave recovery safely", err)
	}
	// The typed command clears the record for this specific outcome. A later
	// deliberately new update can transfer normally rather than staying stuck.
	err = runCSRLanguageUpdate(context.Background(), backend, plan, "", "", extendedRecoveryIdentity(before), func(string, csrLanguageConnection) error { return nil }, nil)
	if err != nil || !backend.peer.restarted {
		t.Fatal("fresh retry after unchanged recovery failed", err)
	}
}

type languageBatteryBackend struct {
	*languageBackendPeer
	level    int
	charging bool
	readErr  error
}

func (backend languageBatteryBackend) Open(ctx context.Context) (csrLanguageConnection, error) {
	connection, err := backend.languageBackendPeer.Open(ctx)
	connection.Battery = func(context.Context) (int, bool, error) { return backend.level, backend.charging, backend.readErr }
	return connection, err
}

func TestCSRLanguageUsesVendorUSBPowerPrecondition(t *testing.T) {
	for _, test := range []struct {
		level    int
		charging bool
		pass     bool
	}{{9, true, false}, {10, true, true}, {0, false, true}, {101, false, false}, {-1, false, false}} {
		backend, plan := languageDriverFixture()
		checked := languageBatteryBackend{languageBackendPeer: backend, level: test.level, charging: test.charging}
		checkpoints := 0
		err := runCSRLanguageUpdate(context.Background(), checked, plan, "", "", "", func(string, csrLanguageConnection) error { checkpoints++; return nil }, nil)
		if (err == nil) != test.pass {
			t.Fatal(test, err)
		}
		if !test.pass && (checkpoints != 0 || len(backend.peer.commands) != 0) {
			t.Fatal("failed power precondition sent state-changing commands")
		}
	}
}

func (peer *languageUpdatePeer) Write(ctx context.Context, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(raw) != 64 {
		return errors.New("unexpected language update frame size")
	}
	peer.commands = append(peer.commands, [2]byte{raw[5], raw[6]})
	if raw[5] == 0x0f && raw[6] != OtaOpDfuFromSquif {
		return peer.languageStagePeer.Write(ctx, raw)
	}
	ack := func() { peer.queue = append(peer.queue, append([]byte{5, 0, 8, raw[3], 0xca, 0xff}, raw[1:6]...)) }
	nack := func() { peer.queue = append(peer.queue, []byte{5, 0, 8, raw[3], 0xc6, 0xfe, 1}) }
	if raw[5] == 0x0f && raw[6] == OtaOpDfuFromSquif {
		if !peer.finished || peer.committed {
			return errors.New("unverified or repeated language commit")
		}
		if peer.failCommit {
			nack()
			return nil
		}
		peer.committed = true
		ack()
		if peer.afterCommitWrite != nil {
			peer.afterCommitWrite()
		}
		return nil
	}
	if !peer.committed {
		return errors.New("restart operation before commit acknowledgement")
	}
	if raw[5] == 2 && raw[6] == 1 {
		if raw[4]&0xc0 != 0x40 {
			return errors.New("serial must be queried")
		}
		data := append([]byte{9}, []byte("test-only")...)
		peer.queue = append(peer.queue, append([]byte{5, 0, 8, raw[3], 0xc0 | byte(6+len(data)), 2, 1}, data...))
		peer.serialRead = true
		return nil
	}
	if raw[5] == 0x0d && raw[6] == 0x11 {
		if !peer.serialRead || raw[4]&0xc0 != 0x40 {
			return errors.New("wrong restart permission order")
		}
		peer.queue = append(peer.queue, []byte{5, 0, 8, raw[3], 0xc7, 0x0d, 0x11, peer.permission})
		peer.permissionRead = true
		return nil
	}
	if raw[5] == 0x0d && raw[6] == 0x12 {
		if !peer.permissionRead || peer.permission != 1 {
			return errors.New("configuration exit without permission")
		}
		if peer.failExit {
			nack()
			return nil
		}
		peer.restarted = true
		ack()
		return nil
	}
	return errors.New("unexpected voice update command")
}

type languageBackendPeer struct {
	peer                       *languageUpdatePeer
	before, after              csrExtendedIdentity
	beforeTune, afterTune      string
	closed, opened, reconnects int
	reconnectError             bool
}

func (backend *languageBackendPeer) connection(identity csrExtendedIdentity, tunes string) csrLanguageConnection {
	return csrLanguageConnection{csrExtendedConnection: csrExtendedConnection{IO: backend.peer, Identity: identity, Address: 8, ReportSize: 64, Close: func() error { backend.closed++; return nil }}, TunesVersion: tunes, Battery: func(context.Context) (int, bool, error) { return 94, false, nil }}
}
func (backend *languageBackendPeer) Open(ctx context.Context) (csrLanguageConnection, error) {
	backend.opened++
	return backend.connection(backend.before, backend.beforeTune), ctx.Err()
}
func (backend *languageBackendPeer) Reconnect(ctx context.Context, _ csrExtendedIdentity) (csrLanguageConnection, error) {
	backend.reconnects++
	if backend.reconnectError || !backend.peer.restarted {
		return csrLanguageConnection{}, errors.New("headset did not restart")
	}
	return backend.connection(backend.after, backend.afterTune), ctx.Err()
}

func languageDriverFixture() (*languageBackendPeer, csrLanguagePlan) {
	image := bytes.Repeat([]byte{0x51}, 105)
	peer := &languageUpdatePeer{languageStagePeer: languageStagePeer{csrStagePeer: csrStagePeer{image: image, reportSize: 64, chunkBytes: 52}, language: 0x407}, permission: 1}
	identity := csrExtendedIdentity{PID: 0x2549, Port: "isolated-usb", Attachment: "before", Serial: "test-only", Variant: "01a3", Version: "1.0.1", Language: 0x409, FirmwareProtocols: []byte{17}}
	after := identity
	after.Attachment = "after"
	after.Language = 0x407
	return &languageBackendPeer{peer: peer, before: identity, after: after, beforeTune: "0.2.1", afterTune: "0.2.1"}, csrLanguagePlan{Firmware: "1.0.1", Language: 0x407, TuneVersion: [3]byte{0, 2, 1}, Preload: 10, Image: image}
}

func TestCSRLanguageDriverCommitRestartAndFreshReadback(t *testing.T) {
	backend, plan := languageDriverFixture()
	var checkpoints []string
	err := runCSRLanguageUpdate(context.Background(), backend, plan, "", "", "", func(phase string, _ csrLanguageConnection) error {
		checkpoints = append(checkpoints, phase)
		return nil
	}, nil)
	if err != nil || !backend.peer.restarted || backend.reconnects != 1 || backend.closed != 2 {
		t.Fatal(err, backend.reconnects, backend.closed)
	}
	if len(checkpoints) != 2 || checkpoints[0] != csrLanguageTransferring || checkpoints[1] != csrLanguageCommitting {
		t.Fatal("checkpoint ordering", checkpoints)
	}
	last := backend.peer.commands[len(backend.peer.commands)-4:]
	want := [][2]byte{{15, 0x1d}, {2, 1}, {13, 0x11}, {13, 0x12}}
	for index := range want {
		if last[index] != want[index] {
			t.Fatal("wrong commit/restart sequence", last)
		}
	}
}

func TestCSRLanguageDriverFaultsRetainUnconfirmedState(t *testing.T) {
	for _, fault := range []string{"checkpoint", "commit", "permission", "exit", "reconnect", "language", "tunes", "firmware", "identity", "generation"} {
		t.Run(fault, func(t *testing.T) {
			backend, plan := languageDriverFixture()
			switch fault {
			case "commit":
				backend.peer.failCommit = true
			case "permission":
				backend.peer.permission = 0
			case "exit":
				backend.peer.failExit = true
			case "reconnect":
				backend.reconnectError = true
			case "language":
				backend.after.Language = 0x409
			case "tunes":
				backend.afterTune = "0.1.0"
			case "firmware":
				backend.after.Version = "1.0.2"
			case "identity":
				backend.after.Serial = "another-headset"
			case "generation":
				backend.after.Attachment = "before"
			}
			phase := ""
			err := runCSRLanguageUpdate(context.Background(), backend, plan, "", "", "", func(next string, _ csrLanguageConnection) error {
				if fault == "checkpoint" && next == csrLanguageCommitting {
					return errors.New("save failed")
				}
				phase = next
				return nil
			}, nil)
			if err == nil {
				t.Fatal("fault reported success")
			}
			if fault == "checkpoint" {
				if phase != csrLanguageTransferring || backend.peer.committed {
					t.Fatal("commit occurred before durable checkpoint")
				}
			} else if phase != csrLanguageCommitting {
				t.Fatal("unconfirmed commit phase was lost", phase)
			}
		})
	}
}

func TestCSRLanguageCommittingRecoveryNeverWrites(t *testing.T) {
	for _, complete := range []bool{false, true} {
		backend, plan := languageDriverFixture()
		original := backend.before
		if complete {
			backend.before = backend.after
		}
		err := runCSRLanguageUpdate(context.Background(), backend, plan, csrLanguageCommitting, original.Attachment, extendedRecoveryIdentity(original), func(string, csrLanguageConnection) error {
			t.Fatal("recovery attempted another checkpoint")
			return nil
		}, nil)
		if (err == nil) != complete || len(backend.peer.commands) != 0 || backend.reconnects != 0 {
			t.Fatal("recovery was not strictly read-only", complete, err, backend.peer.commands)
		}
	}
}
