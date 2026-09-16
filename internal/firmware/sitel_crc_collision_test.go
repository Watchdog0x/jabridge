package firmware

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
)

type sitelCRCMemoryPeer struct {
	memory  []byte
	queries int
	legacy  bool
	mixed   bool
}

func (p *sitelCRCMemoryPeer) request(_ context.Context, op byte, data []byte) ([]byte, error) {
	if op != 4 || len(data) != 8 {
		return nil, errors.New("unexpected CRC request")
	}
	address, count := binary.LittleEndian.Uint32(data), binary.LittleEndian.Uint32(data[4:])
	if uint64(address)+uint64(count) > uint64(len(p.memory)) {
		return nil, errors.New("CRC outside memory")
	}
	p.queries++
	result := make([]byte, 2)
	checksum := independentSitelCRC(p.memory[address : address+count])
	if p.legacy && (!p.mixed || p.queries == 1) {
		checksum = sitelCRC(p.memory[address:address+count], true)
	}
	binary.LittleEndian.PutUint16(result, checksum)
	return result, nil
}

func TestSitelCRCRequiresAConsistentVariant(t *testing.T) {
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}
	for _, legacy := range []bool{false, true} {
		peer := &sitelCRCMemoryPeer{memory: data, legacy: legacy}
		match, err := sitelCRCMatches(context.Background(), peer, hexImageSegment{Data: data})
		if err != nil || !match || peer.queries != 5 {
			t.Fatal("valid CRC variant was rejected", legacy, match, err)
		}
	}
	peer := &sitelCRCMemoryPeer{memory: data, legacy: true, mixed: true}
	if match, err := sitelCRCMatches(context.Background(), peer, hexImageSegment{Data: data}); err != nil || match {
		t.Fatal("mixed CRC variants were accepted", err)
	}
}

func TestSitelCRCDoesNotMistakeErasedFlashForFirmware(t *testing.T) {
	// Synthetic bytes, not extracted firmware. The last two bytes make this
	// sector's 16-bit CRC equal to an erased (all FF) sector's CRC.
	wanted := make([]byte, 256)
	wanted[254], wanted[255] = 0xa4, 0xd0
	erased := make([]byte, 256)
	for i := range erased {
		erased[i] = 255
	}
	if independentSitelCRC(wanted) != independentSitelCRC(erased) {
		t.Fatal("collision fixture is invalid")
	}
	peer := &sitelCRCMemoryPeer{memory: erased}
	match, err := sitelCRCMatches(context.Background(), peer, hexImageSegment{Data: wanted})
	if err != nil {
		t.Fatal(err)
	}
	if match {
		t.Fatal("an unwritten sector was accepted as complete because its CRC collided")
	}
	peer.memory = wanted
	match, err = sitelCRCMatches(context.Background(), peer, hexImageSegment{Data: wanted})
	if err != nil || !match {
		t.Fatal("matching sector rejected", err)
	}
}
