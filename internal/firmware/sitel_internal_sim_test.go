package firmware

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Independent instruction/controller simulator. It executes the uploaded RAM
// words and checks controller pulse order/dwell. It does not emulate radio
// firmware, analog flash behavior or physical USB timing.
type internalFlashCPU struct {
	code                []uint16
	ram                 [65536]uint16
	flash               [256 * 4096]uint16
	regs                [4]uint16
	pc, prefix          int
	ticks, since        uint64
	zero, less, greater bool
	running, idle       bool
	window              uint16
	control             uint16
	operation           byte
	latchAddress        uint32
	latchValue          uint16
	latched             bool
	pulses              int
	erases              map[uint16]int
	programs            int
}

func newInternalFlashCPU() *internalFlashCPU {
	c := &internalFlashCPU{erases: make(map[uint16]int)}
	for i := range c.flash {
		c.flash[i] = uint16(i*37) ^ 0x5aa5
	}
	return c
}
func (c *internalFlashCPU) memory(address uint16) uint16 {
	if address == 0x10 {
		return uint16(c.ticks)
	}
	if address >= 0x2000 && address < 0x3000 {
		return c.flash[uint32(c.window)*4096+uint32(address-0x2000)]
	}
	return c.ram[address]
}
func (c *internalFlashCPU) store(address, value uint16) error {
	switch {
	case address == 0xf8de:
		if value&0xfe01 != 0x2000 || (value&511)/2 > 250 {
			return fmt.Errorf("invalid flash window %04x", value)
		}
		c.window = (value & 511) / 2
	case address == 0xfb86:
		return c.controller(value)
	case address >= 0x2000 && address < 0x3000:
		if c.control != 0x400 && c.control != 0x40a && c.control != 0x41a {
			return errors.New("flash latch written outside a controller transaction")
		}
		c.latchAddress = uint32(c.window)*4096 + uint32(address-0x2000)
		c.latchValue = value
		c.latched = true
	case address == 0x3b || address == 0x17 || address == 0xb2ff || address == 0x55 || address == 0xf80c || address == 0xf80d || address == 0xf84f || address == 0xf815 || address == 2 || address >= 0x9000 && address < 0xa000 || address >= 0xb000 && address < 0xb030:
		c.ram[address] = value
	default:
		return fmt.Errorf("unrelated processor write %04x", address)
	}
	if address == 0xb003 && value == 0x4a42 || address == 0xb000 && value == 0 && c.ram[0xb003] == 0x4a42 {
		c.idle = true
	}
	return nil
}
func (c *internalFlashCPU) controller(value uint16) error {
	previous, elapsed := c.control, c.ticks-c.since
	valid := false
	switch value {
	case 0x400:
		valid = previous == 0 || previous == 0x412 && elapsed >= 5
	case 0x40a:
		valid = previous == 0x400
		c.operation = 3
		c.pulses = 0
		c.latched = false
	case 0x41a:
		if previous == 0x40a {
			valid = elapsed >= 5
		} else {
			valid = previous == 0x41b && elapsed >= 10 && c.latched && c.latchAddress < 251*4096
			if valid {
				c.flash[c.latchAddress] &= c.latchValue
				c.latched = false
				c.pulses++
				c.programs++
			}
		}
	case 0x41b:
		valid = previous == 0x41a && c.latched && (c.pulses != 0 || elapsed >= 10)
	case 0x482:
		valid = previous == 0x400 && c.latched
		c.operation = 2
	case 0x492:
		valid = previous == 0x482 && elapsed >= 5
	case 0x412:
		if c.operation == 2 {
			valid = previous == 0x492 && elapsed >= 10000
			if valid {
				for i := uint32(c.window) * 4096; i < uint32(c.window+1)*4096; i++ {
					c.flash[i] = 0xffff
				}
				c.erases[c.window]++
			}
		} else {
			valid = previous == 0x41a && c.pulses == 4
		}
	case 0:
		valid = previous == 0x400 && (c.operation != 3 || elapsed >= 10)
	}
	if !valid {
		return fmt.Errorf("bad flash controller transition %04x -> %04x after %d ticks", previous, value, elapsed)
	}
	c.control = value
	c.since = c.ticks
	return nil
}
func (c *internalFlashCPU) step() error {
	c.ticks++
	if c.pc < 0 || c.pc >= len(c.code) {
		return fmt.Errorf("processor escaped uploaded code at %d", c.pc)
	}
	word, at := c.code[c.pc], c.pc
	c.pc++
	if word == 0 {
		c.prefix = 0
		return nil
	}
	operandByte := int(int8(byte(word >> 8)))
	if word&255 == 0 {
		c.prefix = c.prefix*256 + operandByte
		return nil
	}
	operand := uint16(c.prefix*256 + operandByte)
	c.prefix = 0
	group, register, mode := word>>4&15, word>>2&3, word&3
	address := func() uint16 {
		switch mode {
		case 1:
			return operand
		case 2:
			return c.regs[2] + operand
		default:
			return c.regs[3] + operand
		}
	}
	data := func() uint16 {
		if mode == 0 {
			return operand
		}
		return c.memory(address())
	}
	switch {
	case group == 0 && word&15 == 5:
		if c.memory(c.regs[3]+operand) != 0x10 {
			return errors.New("wrong processor flags initialization")
		}
	case group == 1:
		c.regs[register] = data()
	case group == 2 && mode != 0:
		return c.store(address(), c.regs[register])
	case group == 3:
		c.regs[register] += data()
		c.zero = c.regs[register] == 0
	case group == 5:
		c.regs[register] -= data()
		c.zero = c.regs[register] == 0
	case group == 8:
		v := data()
		c.zero = c.regs[register] == v
		c.less = int16(c.regs[register]) < int16(v)
		c.greater = int16(c.regs[register]) > int16(v)
	case group == 11:
		c.regs[register] |= data()
		c.zero = c.regs[register] == 0
	case group == 12:
		c.regs[register] &= data()
		c.zero = c.regs[register] == 0
	case group == 14 || group == 15 || group == 2 && mode == 0:
		if mode != 0 {
			return errors.New("unexpected indirect branch")
		}
		take := group == 14 && (register == 0 || register == 1 && c.less) || group == 2 && register == 0 && c.greater || group == 15 && (register == 0 && !c.zero || register == 1 && c.zero)
		if take {
			c.pc = at + int(int16(operand))
		}
	default:
		return fmt.Errorf("unknown uploaded instruction %04x", word)
	}
	return nil
}
func (c *internalFlashCPU) advance() error {
	if !c.running || c.idle {
		return nil
	}
	for range 32768 {
		if err := c.step(); err != nil {
			return err
		}
		if c.idle {
			return nil
		}
	}
	return nil
}

type internalFlashSim struct {
	cpu             *internalFlashCPU
	debug           [65536]uint16
	codeWindow      bool
	codePC          uint32
	stopped         bool
	boots           int
	failCommand     uint16
	failOnce        bool
	corruptCode     bool
	badReadback     bool
	cancelOnCommand context.CancelFunc
}

func newInternalFlashSim(revision uint16) *internalFlashSim {
	s := &internalFlashSim{cpu: newInternalFlashCPU()}
	s.debug[0xfe81] = revision
	s.debug[0xf831] = 1
	return s
}
func (s *internalFlashSim) read(ctx context.Context, address uint16, count int, _ bool) ([]uint16, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.cpu.advance(); err != nil {
		return nil, err
	}
	if count <= 0 || int(address)+count > 65536 {
		return nil, errors.New("bad simulator read range")
	}
	result := make([]uint16, count)
	for i := range result {
		a := address + uint16(i)
		switch {
		case a >= 0x2000 && a < 0x4000 && s.codeWindow:
			index := int(a - 0x2000)
			if index < len(s.cpu.code) {
				result[i] = s.cpu.code[index]
			}
			if s.corruptCode && index == 0 {
				result[i] ^= 1
			}
		case a == 0xfb86:
			result[i] = s.cpu.control
		case a >= 0xf000:
			result[i] = s.debug[a]
		default:
			result[i] = s.cpu.ram[a]
		}
	}
	if s.badReadback && s.cpu.programs > 0 && address == 0x9000 {
		result[0] ^= 1
	}
	return result, nil
}
func (s *internalFlashSim) write(ctx context.Context, address uint16, words []uint16, _ bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.cpu.advance(); err != nil {
		return err
	}
	if len(words) == 0 || int(address)+len(words) > 65536 {
		return errors.New("bad simulator write range")
	}
	for i, value := range words {
		a := address + uint16(i)
		switch {
		case a == 0xf8fa:
			s.debug[a] = value
			s.codeWindow = value == 0x3800
		case a >= 0x2000 && a < 0x4000 && s.codeWindow:
			if !s.stopped {
				return errors.New("code was uploaded to a running processor")
			}
			index := int(a - 0x2000)
			for len(s.cpu.code) <= index {
				s.cpu.code = append(s.cpu.code, 0)
			}
			s.cpu.code[index] = value
		case a == 0xf82f:
			if s.cpu.control != 0 {
				return errors.New("processor reset during active flash pulse")
			}
			s.debug[a] = 0
			s.codePC = 0
			s.debug[0xffe9], s.debug[0xffea] = 0, 0
			s.cpu.running = false
			s.cpu.idle = false
			s.cpu.ram[0xb003] = 0
		case a == 0xffe9:
			s.debug[a] = value
			s.codePC = (s.codePC & 0xffff) | uint32(value)<<16
		case a == 0xffea:
			s.debug[a] = value
			s.codePC = (s.codePC & 0xff0000) | uint32(value)
		case a == 0xf81d:
			if value != 1 && s.cpu.control != 0 {
				return errors.New("processor halted during active flash pulse")
			}
			s.debug[a] = value
			s.stopped = value != 1
			if s.stopped {
				s.debug[0xf831] = 1
				s.cpu.running = false
			} else {
				s.debug[0xf831] = 0
				if s.codePC == 0xc00000 {
					s.cpu.pc = 0
					s.cpu.prefix = 0
					s.cpu.running = true
					s.cpu.idle = false
				} else {
					s.boots++
				}
			}
		case a >= 0xf000:
			s.debug[a] = value
		default:
			s.cpu.ram[a] = value
			if a == 0xb000 && value != 0 {
				s.cpu.idle = false
				if s.cancelOnCommand != nil && value == 3 {
					s.cancelOnCommand()
					s.cancelOnCommand = nil
					return context.Canceled
				}
				if s.failOnce && value == s.failCommand {
					s.failOnce = false
					return errors.New("injected lost command acknowledgement")
				}
			}
		}
	}
	return nil
}
func (s *internalFlashSim) sleep(ctx context.Context, _ time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.cpu.advance()
}
