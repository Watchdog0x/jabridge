package firmware

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func TestHidrawDeliversBufferedReportBeforeHangup(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	report := []byte{5, 0, 1, 0x31, 7, 0x0f, OtaEventVerifyStatus, 0}
	if _, err := writer.Write(report); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	transport := &HidrawTransport{f: reader}
	got, err := transport.Read(100 * time.Millisecond)
	if err != nil || !bytes.Equal(got, report) {
		t.Fatal("complete report lost at hangup", got, err)
	}
}

func TestCSRRejectsWrongAddressAndEchoAcknowledgments(t *testing.T) {
	seq := byte(0x31)
	for _, ack := range [][]byte{
		{0, 8, seq, 0xca, 0xff, 8, 0, seq, 0x87, 0x0f},
		{0, 1, seq, 0xca, 0xff, 1, 0, seq, 0x86, 0x0f},
		{0, 1, seq, 0xc6, 0xff, 0},
	} {
		tr := &fakeTransport{replies: [][]byte{padTo63(ack)}}
		u := NewCsrOtaUpdater(tr, CsrOtaOptions{AckTimeout: 5 * time.Millisecond}, 64, 1, 0x24c7)
		if err := u.sendCmdSeq(seq, buildCommandFull(1, seq, GnpClassCsrOta, OtaOpSelectPartition, []byte{0}, 64)); err == nil {
			t.Fatalf("invalid ACK accepted: %x", ack)
		}
	}
}

func TestCSRRejectsWrongAddressVerification(t *testing.T) {
	tr := &fakeTransport{replies: [][]byte{padTo63([]byte{0, 8, 0x31, 7, 0x0f, OtaEventVerifyStatus, 0})}}
	u := NewCsrOtaUpdater(tr, CsrOtaOptions{}, 64, 1, 0x24c7)
	u.eventPhase = 3
	if _, err := u.waitEvent(OtaEventVerifyStatus, 5*time.Millisecond); err == nil {
		t.Fatal("verification from another address accepted")
	}
}

func TestCSRRejectsTruncatedLengthAndZeroSource(t *testing.T) {
	for _, packet := range [][]byte{
		{0, 1, 0x31, 0x3f, 0x0f, OtaEventVerifyStatus, 0},
		{0, 0, 0x31, 7, 0x0f, OtaEventVerifyStatus, 0},
		{0, 1, 0x31, 6, 0x0f, OtaEventVerifyStatus},
	} {
		if _, err := parseResponse(packet); err == nil {
			t.Fatalf("invalid frame accepted: %x", packet)
		}
	}
}

func TestCSRPreservesEraseEventBeforeStartAcknowledgment(t *testing.T) {
	seq := byte(0x31)
	tr := &fakeTransport{replies: [][]byte{
		padTo63([]byte{0, 1, 0x42, 0x0a, 0x0f, OtaEventFlashEraseDone, 0, 0, 0, 0}),
		padTo63([]byte{0, 1, seq, 0xca, 0xff, 1, 0, seq, 0x86, 0x0f}),
	}}
	u := NewCsrOtaUpdater(tr, CsrOtaOptions{AckTimeout: 5 * time.Millisecond}, 64, 1, 0x24c7)
	if err := u.sendCmdSeq(seq, buildCommandFull(1, seq, GnpClassCsrOta, OtaOpSendStart, nil, 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := u.waitEvent(OtaEventFlashEraseDone, 5*time.Millisecond); err != nil {
		t.Fatal("erase event lost", err)
	}
}

func TestCSREventQueueIsBoundedAndRespectsTransferPhase(t *testing.T) {
	u := NewCsrOtaUpdater(&fakeTransport{}, CsrOtaOptions{}, 64, 1, 0x24c7)
	u.eventPhase = 2
	event := OtaResponse{Source: 1, IsEvent: true, EventOpcode: OtaEventVerifyStatus, EventPayload: []byte{0}}
	if err := u.queueEvent(event); err != nil || len(u.pendingEvents) != 0 {
		t.Fatal("early verification accepted", err)
	}
	u.eventPhase = 3
	if err := u.queueEvent(event); err != nil {
		t.Fatal(err)
	}
	if err := u.queueEvent(event); err != nil || len(u.pendingEvents) != 1 {
		t.Fatal("duplicate not coalesced", err)
	}
	for i := 1; i < 64; i++ {
		event.Seq = byte(i)
		if err := u.queueEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	event.Seq = 64
	if err := u.queueEvent(event); err == nil || len(u.pendingEvents) != 64 {
		t.Fatal("unbounded event queue")
	}
}

func FuzzCSRResponse(f *testing.F) {
	f.Add([]byte{0, 1, 1, 7, 0x0f, OtaEventVerifyStatus, 0})
	f.Add([]byte{0, 1, 1, 0x3f, 0x0f, OtaEventVerifyStatus, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := parseResponse(data)
		if err == nil && r.Source == 0 {
			t.Fatal("zero source accepted")
		}
	})
}
