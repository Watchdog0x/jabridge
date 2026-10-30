package firmware

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"testing"
	"time"
)

func recordCSRFrame(h hash.Hash, direction byte, packet []byte) {
	_, _ = h.Write([]byte{direction})
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(packet)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(packet)
}

type transcriptStageIO struct {
	csrStageIO
	digest hash.Hash
}

func (r transcriptStageIO) Write(ctx context.Context, p []byte) error {
	recordCSRFrame(r.digest, 'W', p)
	return r.csrStageIO.Write(ctx, p)
}
func (r transcriptStageIO) Read(ctx context.Context) ([]byte, error) {
	p, e := r.csrStageIO.Read(ctx)
	if e == nil {
		recordCSRFrame(r.digest, 'R', p)
	}
	return p, e
}

type transcriptBackend struct {
	inner  csrExtendedBackend
	digest hash.Hash
}

func (r transcriptBackend) Open(ctx context.Context) (csrExtendedConnection, error) {
	c, e := r.inner.Open(ctx)
	if e == nil {
		c.IO = transcriptStageIO{c.IO, r.digest}
	}
	return c, e
}
func (r transcriptBackend) Reconnect(ctx context.Context, id csrExtendedIdentity) (csrExtendedConnection, error) {
	c, e := r.inner.Reconnect(ctx, id)
	if e == nil {
		c.IO = transcriptStageIO{c.IO, r.digest}
	}
	return c, e
}

func TestExtendedCSRWireTranscript(t *testing.T) {
	for _, split := range []bool{false, true} {
		inner, plan := extendedFixture(split)
		digest := sha256.New()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := runExtendedCSRUpdate(ctx, transcriptBackend{inner, digest}, plan, func(int) error { return nil }, nil)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		got := fmt.Sprintf("%x", digest.Sum(nil))
		want := map[bool]string{false: "ae6e1bcffeda313f85fe5ea6aa76e4043aa76c9324a7db885e9d7c37f1336cbe", true: "c9f3030bd01e5a97474feea28ffc8c9041b1ca709d8fa9bdc333dfd9904a986b"}[split]
		if got != want {
			t.Fatalf("protocol=%d transcript=%s want=%s", plan.Protocol, got, want)
		}
	}
}

func assertLegacyCSRTranscript(t *testing.T, writes, reads [][]byte) {
	t.Helper()
	digest := sha256.New()
	for _, p := range writes {
		recordCSRFrame(digest, 'W', p)
	}
	for _, p := range reads {
		recordCSRFrame(digest, 'R', p)
	}
	got := fmt.Sprintf("%x", digest.Sum(nil))
	const want = "40b9481f7aac748cdced1cddc6ccf2eb003af9b4c0e3aefceb412a85add5113b"
	if got != want {
		t.Fatalf("legacy gnV transcript=%s want=%s", got, want)
	}
}
