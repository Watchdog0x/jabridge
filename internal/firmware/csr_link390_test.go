package firmware

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// This peer implements the independently decompiled protocol-7 rules, rather
// than deriving its expectations from the production stage/packet helpers.
type csr7Peer struct {
	delayedErase                        bool
	erasePending                        bool
	eventBudgets                        map[byte]time.Duration
	image                               []byte
	version                             [3]byte
	queue                               [][]byte
	writes                              [][]byte
	next, count                         uint32
	preload                             uint16
	state                               byte
	awaitingAck, verified               bool
	missingFinal, badCRC, lostCommitAck bool
	nak                                 byte
	onCommit                            func()
}

func (p *csr7Peer) event(op byte, body ...byte) []byte {
	return append([]byte{5, 0, 1, 0, byte(6 + len(body)), 15, op}, body...)
}
func (p *csr7Peer) Write(ctx context.Context, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.writes = append(p.writes, append([]byte(nil), raw...))
	if len(raw) != 64 || raw[0] != 5 || raw[1] != 1 || raw[2] != 0 || raw[5] != 15 {
		return errors.New("wrong protocol-7 frame or config-end")
	}
	op := raw[6]
	length := int(raw[4] & 63)
	if length < 6 || length+1 > len(raw) {
		return errors.New("wrong frame bounds")
	}
	if op != 0x1a && raw[4]&0xc0 != 0x80 {
		return errors.New("wrong command flag")
	}
	ack := append([]byte{5, 0, 1, raw[3], 0xca, 0xff}, raw[1:6]...)
	if p.nak == op {
		p.queue = append(p.queue, []byte{5, 0, 1, raw[3], 0xc6, 0xfe, 1})
		return nil
	}
	switch op {
	case 0x2d:
		if p.state != 0 || length != 7 || raw[7] != 0 {
			return errors.New("wrong partition select")
		}
		p.state = 1
	case 0x17:
		if p.state != 1 {
			return errors.New("wrong erase order")
		}
		p.state = 2
		if p.delayedErase {
			p.erasePending = true
		} else {
			p.queue = append(p.queue, p.event(0x18))
		}
	case 0x19:
		if p.state != 2 || length != 14 {
			return errors.New("protocol 7 requires the 8-byte count/CRC header")
		}
		crc := uint32(binary.LittleEndian.Uint16(raw[7:9]))<<16 | uint32(binary.LittleEndian.Uint16(raw[9:11]))
		if crc != referenceStageCRC(p.image) {
			return errors.New("incorrect host CRC")
		}
		p.count = uint32(binary.LittleEndian.Uint16(raw[11:13]))
		p.preload = binary.LittleEndian.Uint16(raw[13:15])
		if p.count != uint32((len(p.image)+51)/52) || p.preload != 10 {
			return errors.New("wrong count/preload")
		}
		p.state = 3
	case 0x1a:
		if p.state != 3 || p.awaitingAck || raw[4]&0xc0 != 0 || p.next >= p.count {
			return errors.New("block sent before prior batch ACK")
		}
		start := int(p.next) * 52
		end := min(start+52, len(p.image))
		size := int(binary.LittleEndian.Uint16(raw[9:11]))
		if binary.LittleEndian.Uint16(raw[7:9]) != uint16(p.next) || size != end-start || size > 52 || !bytes.Equal(raw[11:11+size], p.image[start:end]) {
			return errors.New("wrong block size/index/data")
		}
		last := p.next
		if last == 0 || last%uint32(p.preload) == 0 || last+1 == p.count {
			p.awaitingAck = true
			if !p.missingFinal || last+1 != p.count {
				var body [2]byte
				binary.LittleEndian.PutUint16(body[:], uint16(last))
				p.queue = append(p.queue, p.event(0x1b, body[:]...))
			}
		}
		p.next++
		if p.next == p.count {
			status := byte(0)
			if p.badCRC {
				status = 1
			}
			p.queue = append(p.queue, p.event(0x1c, status))
		}
		return nil
	case 0x1e:
		if p.next != p.count || !p.verified || p.awaitingAck || length != 11 || !bytes.Equal(raw[7:12], append(p.version[:], 9, 4)) {
			return errors.New("version before verified final ACK or missing language")
		}
		p.state = 4
	case 0x1d:
		if p.state != 4 || length != 6 {
			return errors.New("commit before complete image")
		}
		p.state = 5
		if p.onCommit != nil {
			p.onCommit()
		}
		if p.lostCommitAck {
			return errors.New("simulated lost commit acknowledgement")
		}
	default:
		return fmt.Errorf("unexpected protocol-7 opcode %02x", op)
	}
	p.queue = append(p.queue, ack)
	return nil
}
func (p *csr7Peer) Read(ctx context.Context) ([]byte, error) {
	if len(p.queue) == 0 && p.erasePending {
		p.queue = append(p.queue, p.event(0x18))
		p.erasePending = false
	}
	if len(p.queue) == 0 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	packet := p.queue[0]
	p.queue = p.queue[1:]
	if p.eventBudgets != nil && packet[5] == 15 {
		if end, ok := ctx.Deadline(); ok {
			p.eventBudgets[packet[6]] = time.Until(end)
		}
	}
	if packet[5] == 15 && packet[6] == 0x1b {
		p.awaitingAck = false
	}
	if packet[5] == 15 && packet[6] == 0x1c && packet[7] == 0 {
		p.verified = true
	}
	return packet, nil
}

type csr7BackendPeer struct {
	peer                *csr7Peer
	before, after       csrExtendedIdentity
	opened, reconnected int
}

func (b *csr7BackendPeer) connection(id csrExtendedIdentity) csrExtendedConnection {
	return csrExtendedConnection{IO: b.peer, Identity: id, Address: 1, ReportSize: 64, Close: func() error { return nil }}
}
func (b *csr7BackendPeer) Open(ctx context.Context) (csrExtendedConnection, error) {
	b.opened++
	return b.connection(b.before), ctx.Err()
}
func (b *csr7BackendPeer) Reconnect(ctx context.Context, _ csrExtendedIdentity) (csrExtendedConnection, error) {
	b.reconnected++
	return b.connection(b.after), ctx.Err()
}

func csr7Fixture() (*csr7BackendPeer, link390CSR7Image) {
	image := link390CSR7Image{Data: bytes.Repeat([]byte{0x45}, 573), Version: [3]byte{3, 2, 0}, Language: 0x409, Preload: 10}
	before := csrExtendedIdentity{PID: 0x2e56, Port: "isolated-port", Attachment: "old-generation", Serial: "fixture-only", Variant: "0410", Version: "3.0.13", FirmwareProtocols: []byte{7}}
	after := before
	after.Version = "3.2.0"
	after.Attachment = "new-generation"
	return &csr7BackendPeer{peer: &csr7Peer{image: image.Data, version: image.Version}, before: before, after: after}, image
}

func TestLink390CSR7CompleteTransferAndFreshVersion(t *testing.T) {
	for _, sameGeneration := range []bool{false, true} {
		backend, image := csr7Fixture()
		if sameGeneration {
			backend.after.Attachment = backend.before.Attachment
		}
		var phases []string
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := runLink390CSR7Update(ctx, backend, image, "", "", func(_ csrExtendedIdentity, phase string) error { phases = append(phases, phase); return nil }, nil)
		cancel()
		if err != nil || backend.peer.state != 5 || backend.reconnected != 1 || fmt.Sprint(phases) != "[csr7-transferring csr7-committing]" {
			t.Fatal(err, backend.peer.state, phases)
		}
		if backend.peer.writes[2][4]&63 != 14 {
			t.Fatal("not a protocol-7 CRC header")
		}
	}
}

func TestLink390CSR7RefusesMissingFinalAckAndCRCFailure(t *testing.T) {
	for _, fault := range []string{"final-ack", "crc", "select-nak", "start-nak", "header-nak", "version-nak", "identity", "protocol"} {
		backend, image := csr7Fixture()
		switch fault {
		case "final-ack":
			backend.peer.missingFinal = true
		case "crc":
			backend.peer.badCRC = true
		case "select-nak":
			backend.peer.nak = 0x2d
		case "start-nak":
			backend.peer.nak = 0x17
		case "header-nak":
			backend.peer.nak = 0x19
		case "version-nak":
			backend.peer.nak = 0x1e
		case "identity":
			backend.before.Serial = ""
		case "protocol":
			backend.before.FirmwareProtocols = []byte{16}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		err := runLink390CSR7Update(ctx, backend, image, "", "", func(csrExtendedIdentity, string) error { return nil }, nil)
		cancel()
		if err == nil || backend.peer.state == 5 {
			t.Fatal(fault, err)
		}
	}
}

func TestLink390CSR7CommitUncertaintyNeverReplays(t *testing.T) {
	for _, fault := range []string{"lost-ack", "commit-nak", "wrong-version", "wrong-serial"} {
		backend, image := csr7Fixture()
		switch fault {
		case "lost-ack":
			backend.peer.lostCommitAck = true
		case "commit-nak":
			backend.peer.nak = 0x1d
			backend.after.Version = "3.0.13"
		case "wrong-version":
			backend.after.Version = "3.0.13"
		case "wrong-serial":
			backend.after.Serial = "different-unit"
		}
		var phase string
		err := runLink390CSR7Update(context.Background(), backend, image, "", "", func(_ csrExtendedIdentity, p string) error { phase = p; return nil }, nil)
		if (err == nil) != (fault == "lost-ack") || phase != csr7Committing {
			t.Fatal(fault, phase, err)
		}
		commits := 0
		for _, raw := range backend.peer.writes {
			if raw[6] == 0x1d {
				commits++
			}
		}
		if commits != 1 {
			t.Fatal("commit replayed", commits)
		}
	}
}

func TestLink390CSR7PendingCommitRecoveryIsReadOnly(t *testing.T) {
	for _, knownUpdated := range []bool{false, true} {
		backend, image := csr7Fixture()
		if !knownUpdated {
			backend.after.Version = backend.before.Version
		}
		err := runLink390CSR7Update(context.Background(), backend, image, csr7Committing, extendedRecoveryIdentity(backend.before), func(csrExtendedIdentity, string) error {
			t.Fatal("recovery tried to checkpoint a new write")
			return nil
		}, nil)
		if (err == nil) != knownUpdated || len(backend.peer.writes) != 0 {
			t.Fatal(err, backend.peer.writes)
		}
	}
}

func TestLink390CSR7RecoveryOpenAlreadyReportsUpdatedGeneration(t *testing.T) {
	for _, wrongSerial := range []bool{false, true} {
		backend, image := csr7Fixture()
		expected := extendedRecoveryIdentity(backend.before)
		backend.before = backend.after
		if wrongSerial {
			backend.before.Serial = "different-unit"
		}
		err := runLink390CSR7Update(context.Background(), backend, image, csr7Committing, expected, func(csrExtendedIdentity, string) error { t.Fatal("recovery wrote a checkpoint"); return nil }, nil)
		if (err == nil) == wrongSerial || len(backend.peer.writes) != 0 || backend.reconnected != 0 {
			t.Fatal(wrongSerial, err, backend.peer.writes)
		}
	}
}

func TestLink390CSR7RuntimeObservationsHaveOneSecondQuietIntervals(t *testing.T) {
	clock := time.Unix(0, 0)
	var observations []time.Time
	connection, err := waitLink390Runtime(context.Background(), func(context.Context) (csrExtendedConnection, error) {
		observations = append(observations, clock)
		if len(observations) < 3 {
			return csrExtendedConnection{}, errors.New("device is still updating")
		}
		return csrExtendedConnection{Identity: csrExtendedIdentity{Version: "3.2.0"}}, nil
	}, func(_ context.Context, d time.Duration) error {
		if d < time.Second {
			t.Fatal("runtime polling too fast", d)
		}
		clock = clock.Add(d)
		return nil
	})
	if err != nil || connection.Identity.Version != "3.2.0" || len(observations) != 3 {
		t.Fatal(err, observations)
	}
	for i := 1; i < len(observations); i++ {
		if observations[i].Sub(observations[i-1]) < time.Second {
			t.Fatal("no quiet interval")
		}
	}
}

func TestLink390CSR7UsesVendorEventBudgets(t *testing.T) {
	backend, image := csr7Fixture()
	backend.peer.delayedErase = true
	backend.peer.eventBudgets = make(map[byte]time.Duration)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := runLink390CSR7Update(ctx, backend, image, "", "", func(csrExtendedIdentity, string) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	for op, want := range map[byte]time.Duration{0x18: 90 * time.Second, 0x1b: 15 * time.Second, 0x1c: 30 * time.Second} {
		got := backend.peer.eventBudgets[op]
		if got > want || got < want-time.Second {
			t.Fatalf("event %02x budget=%s want=%s", op, got, want)
		}
	}
}

func TestLink390CSR7LateRuntimeSuccessIsNotAccepted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	closed := false
	_, err := waitLink390Runtime(ctx, func(context.Context) (csrExtendedConnection, error) {
		cancel()
		return csrExtendedConnection{Close: func() error { closed = true; return nil }}, nil
	}, func(context.Context, time.Duration) error { t.Fatal("retried after cancellation"); return nil })
	if !errors.Is(err, context.Canceled) || !closed {
		t.Fatal(err, closed)
	}
}

func link390ArchiveFixture(t *testing.T, edit func(*BuildVector)) string {
	t.Helper()
	// XML is the official layout; payload is explicitly synthetic.
	xml := `<buildVector version="3.2.0" productName="Jabra Link 390"><maxPreloadCount>10</maxPreloadCount><maxBlePreloadCount>1</maxBlePreloadCount><targetUsbPids><usbPid>0x2e50</usbPid></targetUsbPids><minFirmwareUpdaterAppVersion>5.0.0.0</minFirmwareUpdaterAppVersion><files><file name="update_package_3_2_0.bin"><content>firmware</content><version>3.2.0</version><target>headset</target><language id="0x0409">English</language><partition>0</partition></file></files></buildVector>`
	path := writeFirmwareArchiveFixture(t, xml, map[string][]byte{"update_package_3_2_0.bin": bytes.Repeat([]byte{45}, 573)})
	if edit == nil {
		return path
	}
	manifest, err := parseFirmwareManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	edit(manifest)
	return rewriteCSR7Fixture(t, manifest)
}

func TestLink390CSR7ArchiveAndOptIn(t *testing.T) {
	path := link390ArchiveFixture(t, nil)
	image, err := loadLink390CSR7Archive(path)
	if err != nil || image.Preload != 10 || image.Language != 0x409 {
		t.Fatal(err, image.Preload)
	}
	if err := validateNativeCSRArchive(path); err == nil {
		t.Fatal("fixture did not reproduce old .gnv refusal")
	}
	if err := validateCSRInstallArchive(path, 7); err != nil {
		t.Fatal(err)
	}
	old := link390FirmwareEnabled
	link390FirmwareEnabled = false
	t.Cleanup(func() { link390FirmwareEnabled = old })
	manifest, err := parseFirmwareManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("JABRIDGE_ENABLE_EXPERIMENTAL_WRITES", "I_ACCEPT_THE_BRICK_RISK")
	t.Setenv("JABRIDGE_ENABLE_LINK390_CONFIG_WRITES", "I_ACCEPT_UNTESTED_SETTINGS")
	if err := checkLink390FirmwareOptIn(7, manifest, 0x2e56); err == nil {
		t.Fatal("other flags enabled firmware")
	}
	link390FirmwareEnabled = true
	if err := checkLink390FirmwareOptIn(7, manifest, 0x2e56); err != nil {
		t.Fatal(err)
	}
	if err := checkLink390FirmwareOptIn(7, manifest, 0x24c7); err == nil {
		t.Fatal("unrelated PID enabled")
	}
}

func TestLink390CSR7OfficialArtifactLayoutAndTransfer(t *testing.T) {
	path := os.Getenv("JABRIDGE_LINK390_TEST_PACKAGE")
	if path == "" {
		t.Skip("official package is an external read-only research artifact")
	}
	image, err := loadLink390CSR7Archive(path)
	if err != nil {
		t.Fatal(err)
	}
	backend, _ := csr7Fixture()
	backend.peer.image = image.Data
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runLink390CSR7Update(ctx, backend, image, "", "", func(csrExtendedIdentity, string) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	count := (len(image.Data) + 51) / 52
	if int(backend.peer.next) != count || backend.peer.state != 5 {
		t.Fatal("incomplete real-size transfer")
	}
	t.Logf("actual file: bytes=%d chunks=%d final-index=%d final-size=%d CRC-header=%x", len(image.Data), count, count-1, len(image.Data)-(count-1)*52, backend.peer.writes[2][7:15])
}

// Kept separate so invalid-metadata tests can exercise the exact shared loader.
func rewriteCSR7Fixture(t *testing.T, manifest *BuildVector) string {
	t.Helper()
	data, err := xml.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return writeFirmwareArchiveFixture(t, string(data), map[string][]byte{"update_package_3_2_0.bin": bytes.Repeat([]byte{45}, 573)})
}

func TestLink390CSR7RejectsAmbiguousAndForeignMetadata(t *testing.T) {
	for _, name := range []string{"multiple-languages", "foreign-target", "empty-image-language", "not-firmware", "wrong-version", "preload"} {
		path := link390ArchiveFixture(t, func(m *BuildVector) {
			switch name {
			case "multiple-languages":
				m.Files = append(m.Files, m.Files[0])
			case "foreign-target":
				m.TargetUSBPIDs = []string{"0x24c7"}
			case "empty-image-language":
				m.Files[0].Language.ID = ""
			case "not-firmware":
				m.Files[0].Content = "langpack"
			case "wrong-version":
				m.Files[0].Version = "3.1.0"
			case "preload":
				m.MaxPreloadCount = 0
			}
		})
		if _, err := loadLink390CSR7Archive(path); err == nil {
			t.Fatal(name, "accepted")
		}
	}
}

func TestLink390CSR7RecoveryStateValidation(t *testing.T) {
	backend, _ := csr7Fixture()
	s := firmwareRecoveryState{RuntimePID: 0x2e56, USBPort: "isolated-port", Language: 0x409, Phase: csr7Committing, TargetIdentitySHA256: extendedRecoveryIdentity(backend.before)}
	if err := validLink390CSR7Recovery(s); err != nil {
		t.Fatal(err)
	}
	s.TargetIdentitySHA256 = strings.Repeat("g", 64)
	if err := validLink390CSR7Recovery(s); err == nil {
		t.Fatal("invalid identity accepted")
	}
}
