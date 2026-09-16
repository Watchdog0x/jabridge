package firmware

import "fmt"

const (
	xapInternalCodeBase = uint32(0xc00000)
	xapInternalBuffer   = uint16(0x9000)
	xapInternalCommand  = uint16(0xb000)
	xapInternalSector   = uint16(0xb001)
	xapInternalStatus   = uint16(0xb002)
	xapInternalReady    = uint16(0xb003)
	xapInternalCount    = uint16(0xb004)
	xapInternalOffset   = uint16(0xb005)
	xapInternalMagic    = uint16(0x4a42)
	xapInternalSectors  = 251
	xapInternalWords    = 4096
)

// This is an independently authored RAM program for the internal flash
// controller in CSR8670/CSR8675. The timed FB86 pulses run on the device, so
// USB scheduling cannot stretch an erase/program pulse. It contains no code
// extracted from JabraCLI. The host still checks the actual chip, image and
// readback, and must not reset the processor during an active flash operation.
func buildSitelInternalFlashProgram() ([]uint16, error) {
	var p xapProgramBuilder
	p.set(0x3b, 0)
	p.set(0x17, 0)
	p.emit(0x1c, 0xb300) // initial Y stack pointer
	p.emit(0x14, 0x10)
	p.emit(0x27, 0xffff)
	p.emit(0x05, 0xffff) // dedicated LD FLAGS, (-1,Y)
	for _, item := range [][2]uint16{{0xf80c, 0x4204}, {0xf80d, 0}, {0xf84f, 1}, {0xf815, 1}, {2, 1}, {xapInternalCommand, 0}, {xapInternalSector, 0}, {xapInternalStatus, 0}, {xapInternalReady, xapInternalMagic}} {
		p.set(item[0], item[1])
	}
	p.label("wait")
	for _, value := range []uint16{0x6734, 0xd6bf, 0xc31e} {
		p.set(0x55, value)
	}
	p.loadAL(xapInternalCommand)
	p.compareAL(0)
	p.branch(xapEqual, "wait")
	p.storeAL(0xb010) // snapshot command
	for _, command := range []uint16{1, 2, 3} {
		p.compareAL(command)
		p.branch(xapEqual, "sector")
	}
	p.branch(xapAlways, "bad")
	p.label("sector")
	p.loadAL(xapInternalSector)
	p.storeAL(0xb020)
	p.andAL(0xff00)
	p.branch(xapNotEqual, "bad")
	p.loadAL(0xb020)
	p.compareAL(xapInternalSectors - 1)
	p.branch(xapGreater, "bad")
	p.loadAL(0xb010)
	p.compareAL(2)
	p.branch(xapEqual, "erase_args")
	// Snapshot offset/count. All checks finish before a controller write.
	p.loadAL(xapInternalOffset)
	p.storeAL(0xb021)
	p.andAL(0xf000)
	p.branch(xapNotEqual, "bad")
	p.loadAL(xapInternalCount)
	p.storeAL(0xb022)
	p.andAL(0xe000)
	p.branch(xapNotEqual, "bad")
	p.loadAL(0xb022)
	p.compareAL(xapInternalWords)
	p.branch(xapGreater, "bad")
	p.compareAL(0)
	p.branch(xapEqual, "bad")
	p.emit(0x35, 0xb021) // ADD AL, offset
	p.compareAL(xapInternalWords)
	p.branch(xapGreater, "bad")
	p.loadAL(0xb010)
	p.compareAL(1)
	p.branch(xapEqual, "map")
	p.loadAL(0xb021)
	p.andAL(3)
	p.branch(xapNotEqual, "bad")
	p.loadAL(0xb022)
	p.andAL(3)
	p.branch(xapNotEqual, "bad")
	p.branch(xapAlways, "map")
	p.label("erase_args")
	p.loadAL(xapInternalOffset)
	p.compareAL(0)
	p.branch(xapNotEqual, "bad")
	p.loadAL(xapInternalCount)
	p.compareAL(0)
	p.branch(xapNotEqual, "bad")
	p.label("map")
	p.loadAL(0xb020)
	p.emit(0x35, 0xb020) // sector * 2
	p.emit(0xb4, 0x2000) // flash window selector
	p.storeAL(0xf8de)
	p.loadAL(0xb010)
	p.compareAL(1)
	p.branch(xapEqual, "read")
	p.compareAL(2)
	p.branch(xapEqual, "erase")
	p.branch(xapAlways, "program")
	p.label("read")
	p.emit(0x18, 0x2000) // X = flash window + offset
	p.emit(0x39, 0xb021)
	p.emit(0x1c, xapInternalBuffer) // Y = destination buffer
	p.loadAL(0xb022)
	p.label("readword")
	p.emit(0x12, 0) // LD AH, (0,X)
	p.emit(0x23, 0) // ST AH, (0,Y)
	p.emit(0x38, 1)
	p.emit(0x3c, 1)
	p.emit(0x54, 1)
	p.branch(xapNotEqual, "readword")
	p.branch(xapAlways, "done")
	p.label("erase")
	p.set(0xfb86, 0x400)
	p.set(0x2000, 0)
	p.set(0xfb86, 0x482)
	p.delay(5, "erase_setup")
	p.set(0xfb86, 0x492)
	p.delay(10000, "erase_pulse")
	p.set(0xfb86, 0x412)
	p.delay(5, "erase_hold")
	p.set(0xfb86, 0x400)
	p.set(0xfb86, 0)
	p.delay(10, "erase_settle")
	p.branch(xapAlways, "done")
	p.label("program")
	p.emit(0x18, xapInternalBuffer) // X = source
	p.emit(0x1c, 0x2000)            // Y = flash window + offset
	p.emit(0x3d, 0xb021)
	p.loadAL(0xb022)
	p.storeAL(0xb02a)
	p.label("group")
	p.set(0xfb86, 0x400)
	p.set(0xfb86, 0x40a)
	p.emit(0x12, 0)
	p.emit(0x23, 0)
	p.delay(5, "program_setup")
	p.set(0xfb86, 0x41a)
	p.delay(10, "program_charge")
	for i := range 4 {
		if i != 0 {
			p.emit(0x12, 0)
			p.emit(0x23, 0)
		}
		p.set(0xfb86, 0x41b)
		p.delay(10, fmt.Sprintf("program_bit%d", i))
		p.set(0xfb86, 0x41a)
		p.emit(0x38, 1)
		p.emit(0x3c, 1)
	}
	p.set(0xfb86, 0x412)
	p.delay(5, "program_hold")
	p.set(0xfb86, 0x400)
	p.delay(10, "program_settle")
	p.set(0xfb86, 0)
	p.loadAL(0xb02a)
	p.emit(0x54, 4)
	p.storeAL(0xb02a)
	p.compareAL(0)
	p.branch(xapNotEqual, "group")
	p.label("done")
	p.complete(0)
	p.label("bad")
	p.complete(1)
	return p.finish()
}

const (
	xapEqual    = byte(0xf4)
	xapNotEqual = byte(0xf0)
	xapGreater  = byte(0x20)
	xapLess     = byte(0xe4)
	xapAlways   = byte(0xe0)
)

type xapProgramBranch struct {
	at     int
	opcode byte
	label  string
}

type xapProgramBuilder struct {
	code     []uint16
	labels   map[string]int
	branches []xapProgramBranch
	err      error
}

// Fixed two-word instructions keep labels stable. Operand bytes are signed;
// prefix zero is a NOP when the operand already fits one signed byte.
func xapInstruction(opcode byte, operand uint16) [2]uint16 {
	value := int32(int16(operand))
	low := int32(int8(byte(operand)))
	return [2]uint16{uint16(byte((value-low)/256)) << 8, uint16(byte(operand))<<8 | uint16(opcode)}
}

func (p *xapProgramBuilder) emit(opcode byte, operand uint16) {
	words := xapInstruction(opcode, operand)
	p.code = append(p.code, words[:]...)
}

func (p *xapProgramBuilder) loadAL(address uint16)  { p.emit(0x15, address) }
func (p *xapProgramBuilder) storeAL(address uint16) { p.emit(0x25, address) }
func (p *xapProgramBuilder) compareAL(value uint16) { p.emit(0x84, value) }
func (p *xapProgramBuilder) andAL(value uint16)     { p.emit(0xc4, value) }
func (p *xapProgramBuilder) set(address, value uint16) {
	p.emit(0x14, value)
	p.storeAL(address)
}

func (p *xapProgramBuilder) label(name string) {
	if p.labels == nil {
		p.labels = make(map[string]int)
	}
	if _, duplicate := p.labels[name]; duplicate {
		p.err = fmt.Errorf("duplicate XAP label %q", name)
	}
	p.labels[name] = len(p.code)
}

func (p *xapProgramBuilder) branch(opcode byte, label string) {
	p.branches = append(p.branches, xapProgramBranch{len(p.code), opcode, label})
	p.code = append(p.code, 0, uint16(opcode))
}

// All delays are at most 10000 hardware-counter ticks. Subtracting the low
// counter handles wrap. An elapsed value with bit 15 set has already expired;
// otherwise a signed comparison is safe. No USB transaction controls a pulse.
func (p *xapProgramBuilder) delay(ticks uint16, name string) {
	p.loadAL(0x10)
	p.storeAL(0xb028)
	p.label(name)
	p.loadAL(0x10)
	p.emit(0x55, 0xb028)
	p.storeAL(0xb029)
	p.andAL(0x8000)
	p.branch(xapNotEqual, name+"done")
	p.loadAL(0xb029)
	p.compareAL(ticks)
	p.branch(xapLess, name)
	p.label(name + "done")
}

func (p *xapProgramBuilder) complete(status uint16) {
	p.set(xapInternalStatus, status)
	p.set(xapInternalCommand, 0)
	p.branch(xapAlways, "wait")
}

func (p *xapProgramBuilder) finish() ([]uint16, error) {
	if p.err != nil {
		return nil, p.err
	}
	for _, branch := range p.branches {
		target, ok := p.labels[branch.label]
		offset := target - (branch.at + 1) // relative to opcode, after its prefix
		if !ok || offset < -32768 || offset > 32767 {
			return nil, fmt.Errorf("invalid XAP branch %q", branch.label)
		}
		words := xapInstruction(branch.opcode, uint16(int16(offset)))
		copy(p.code[branch.at:branch.at+2], words[:])
	}
	if len(p.code) > 8192 {
		return nil, fmt.Errorf("XAP program exceeds its RAM code window")
	}
	return p.code, nil
}
