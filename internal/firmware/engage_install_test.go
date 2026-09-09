package firmware

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
	"testing"
	"time"
)

// Independent wire peer. No production encoder, CRC or reply parser creates
// its answers. These fixtures model the documented update path, not the CPU.
type engageWorld struct {
	controllerHasNoSerial                                          bool
	pid                                                            uint16
	version, controllerVersion                                     string
	images                                                         []sitelPlannedImage
	areas                                                          map[byte]sitelArea
	memory                                                         map[uint32]byte
	bootMode                                                       bool
	unsubscribes                                                   int
	generation, erases, writes, activations                        int
	failWrite                                                      int
	badVerify, badController, wrongPort, badGeometry, wrongImageID bool
	zeroSequence, bufferNotice                                     bool
	badControllerType                                              bool
	controllerReenumerates                                         bool
	checkpoint                                                     func() bool
}

func makeEngageWorld(controller bool) *engageWorld {
	w := &engageWorld{pid: 0x4056, version: "4.0.0", controllerVersion: "4.0.0", memory: map[uint32]byte{}, areas: map[byte]sitelArea{0: {Address: 0x60000, Size: 4096}, 4: {Address: 0xe2000, Size: 4096}, 3: {Address: 0x102000, Size: 4096}}}
	if controller {
		w.pid = 0x4052
	}
	for i, target := range []byte{3, 29, 27} {
		kind := []byte{0, 4, 3}[i]
		base := w.areas[kind].Address
		data := make([]byte, 4096)
		for j := range data {
			data[j] = byte(j*17 + i)
		}
		if target == 27 {
			copy(data[11:21], append([]byte("4.1.3"), make([]byte, 5)...))
			binary.LittleEndian.PutUint32(data[30:], 0x0b0e4050)
		} else {
			header := 0x100
			pointerBase := base
			identity := uint32(0x0b0e4050)
			if target == 29 {
				header = 0x400
				pointerBase = 0x08005000
				identity = 0x0b0e4032
			}
			for j, offset := range []uint32{0x80, 0x90, 0xa0, 0xc0} {
				binary.LittleEndian.PutUint32(data[header+j*4:], pointerBase+offset)
			}
			binary.LittleEndian.PutUint32(data[0x80:], identity)
			copy(data[0xc0:], []byte("4.1.3\x00"))
		}
		w.images = append(w.images, sitelPlannedImage{File: GnVFile{Name: fmt.Sprint(target), Version: "4.1.3", SitelHidTargetID: fmt.Sprintf("%02d", target), GNPAddress: "1", UpdateOrder: i}, Segments: []hexImageSegment{{Address: base, Data: data}}})
		for j := range data {
			w.memory[base+uint32(j)] = 255
		}
	}
	return w
}
func (w *engageWorld) device() USBDevice {
	pid := w.pid
	if w.bootMode {
		pid = 0x4050
	}
	path := "/isolated/usb/1-2"
	if w.wrongPort && w.generation > 0 {
		path = "/isolated/usb/1-3"
	}
	return USBDevice{VendorID: JabraVendorID, ProductID: pid, SysPath: path, Serial: "fixture-usb", attachment: &usbAttachment{fingerprint: fmt.Sprint(w.generation)}}
}
func (w *engageWorld) runtime(ctx context.Context, device USBDevice) (*engageRuntime, engageIdentity, func() error, error) {
	r := &engageRuntime{io: &engageRuntimePeer{world: w}}
	id, err := r.identify(ctx, device)
	return r, id, func() error { return nil }, err
}
func (w *engageWorld) wait(ctx context.Context, _ USBDevice, pid uint16) (USBDevice, error) {
	if err := ctx.Err(); err != nil {
		return USBDevice{}, err
	}
	w.generation++
	device := w.device()
	if device.ProductID != pid {
		return device, errors.New("wrong fixture mode")
	}
	return device, nil
}
func (w *engageWorld) boot(ctx context.Context, _ USBDevice) (*sitelRequester, func() error, error) {
	peer := &engageHPPeer{world: w}
	layout := sitelHIDLayout{ReportID: 10, ReportBytes: 64, MaxMessage: 1024}
	link := &sitelLink{io: peer, in: layout, out: layout, timeout: 10 * time.Millisecond}
	if err := link.start(ctx); err != nil {
		return nil, nil, err
	}
	return &sitelRequester{link: link, address: 1, timeout: time.Second}, func() error { return nil }, nil
}

type engageRuntimePeer struct {
	world  *engageWorld
	queue  [][]byte
	closed bool
}

func (p *engageRuntimePeer) event(class byte, body ...byte) {
	p.queue = append(p.queue, append([]byte{5, 0, 1, 0, byte(5 + len(body)), class}, body...))
}
func (p *engageRuntimePeer) Write(ctx context.Context, raw []byte) error {
	if p.closed {
		return syscall.ENODEV
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w := p.world
	if len(raw) != 64 || raw[0] != 5 || (raw[1] != 1 && raw[1] != 3) {
		return errors.New("bad runtime route")
	}
	kind := raw[4] & 0xc0
	length := int(raw[4] & 63)
	class := raw[5]
	body := raw[6 : length+1]
	if kind == 0x40 {
		if class != 2 || len(body) != 1 {
			return errors.New("unexpected runtime query")
		}
		var data []byte
		switch body[0] {
		case 0x11:
			data = make([]byte, 2)
			binary.LittleEndian.PutUint16(data, w.pid)
		case 1:
			text := "fixture-headset"
			if raw[1] == 3 && w.controllerHasNoSerial {
				p.queue = append(p.queue, []byte{5, 0, 3, raw[3], 0xc6, 0xfe, 1})
				return nil
			}
			if raw[1] == 3 {
				text = "fixture-controller"
			}
			data = append([]byte{byte(len(text))}, text...)
		case 2:
			data = []byte{2, 1, 0x72}
			if raw[1] == 3 {
				data = []byte{2, 3, 5}
			}
		case 3:
			text := w.version
			if raw[1] == 3 {
				text = w.controllerVersion
			}
			data = append([]byte{byte(len(text))}, text...)
		case 0x13:
			data = []byte{0x50, 0x40}
		case 0x14:
			data = []byte{4}
		default:
			return errors.New("unexpected IDENT opcode")
		}
		p.queue = append(p.queue, append([]byte{5, 0, raw[1], raw[3], 0xc0 | byte(6+len(data)), 2, body[0]}, data...))
		return nil
	}
	if kind == 0 && class == 13 && bytes.Equal(body, []byte{1, 5, 0}) {
		if w.badControllerType {
			p.event(13, 1, 5, 3)
		} else {
			p.event(13, 1, 2, 3)
		}
		return nil
	}
	if kind == 0 && class == 13 && bytes.Equal(body, []byte{2, 5, 0}) {
		w.unsubscribes++
		return nil
	}
	if kind != 0x80 || class != 7 {
		return errors.New("unknown runtime mutation")
	}
	if w.checkpoint != nil && !w.checkpoint() {
		return errors.New("mutation without durable checkpoint")
	}
	if len(body) == 0 {
		w.bootMode = true
	} else if bytes.Equal(body, []byte{2}) {
		w.activations++
		p.event(7, 1, 1)
		p.event(13, 2, 2, 3)
		p.event(7, 1, 4)
		if !w.badController {
			w.controllerVersion = "4.1.3"
		}
		p.event(7, 1, 5)
		if w.controllerReenumerates {
			p.closed = true
		}
	} else {
		return errors.New("unexpected DFU command")
	}
	p.queue = append(p.queue, append([]byte{5, 0, raw[1], raw[3], 0xca, 0xff}, raw[1:6]...))
	return nil
}
func (p *engageRuntimePeer) Read(ctx context.Context) ([]byte, error) {
	if len(p.queue) == 0 {
		if p.closed {
			return nil, syscall.ENODEV
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	v := p.queue[0]
	p.queue = p.queue[1:]
	return v, nil
}

type engageHPPeer struct {
	world             *engageWorld
	queue             [][]byte
	fragment, message byte
	incoming          []byte
	total             int
	currentMessage    byte
	nextHost          byte
	lastMessage       byte
	haveLast          bool
	dropAck           int
}

func (p *engageHPPeer) control(kind, value byte) {
	raw := make([]byte, 64)
	raw[0] = 10
	raw[1] = kind<<4 | p.fragment
	raw[2] = value
	p.fragment = (p.fragment + 1) & 15
	p.queue = append(p.queue, raw)
}
func (p *engageHPPeer) reply(data []byte) {
	for offset := 0; offset < len(data); {
		raw := make([]byte, 64)
		raw[0] = 10
		head := 2
		if offset == 0 {
			raw[1] = 0x30 | p.fragment
			binary.LittleEndian.PutUint16(raw[2:4], uint16(len(data)))
			raw[4] = p.message
			head = 5
		} else {
			raw[1] = 0x40 | p.fragment
		}
		p.fragment = (p.fragment + 1) & 15
		count := copy(raw[head:], data[offset:])
		offset += count
		p.queue = append(p.queue, raw)
	}
	p.message++
}
func (p *engageHPPeer) Write(ctx context.Context, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(raw) != 64 || raw[0] != 10 {
		return errors.New("bad HP report")
	}
	if raw[1]>>4 == 1 {
		p.nextHost = (raw[1] + 1) & 15
		p.control(2, 0)
		return nil
	}
	if raw[1]&15 != p.nextHost {
		return errors.New("host fragment sequence mismatch")
	}
	p.nextHost = (p.nextHost + 1) & 15
	switch raw[1] >> 4 {
	case 5:
		return nil
	case 3:
		p.total = int(binary.LittleEndian.Uint16(raw[2:4]))
		p.currentMessage = raw[4]
		p.incoming = append([]byte(nil), raw[5:min(64, 5+p.total)]...)
	case 4:
		if len(p.incoming) >= p.total {
			return errors.New("orphan continuation")
		}
		n := min(62, p.total-len(p.incoming))
		p.incoming = append(p.incoming, raw[2:2+n]...)
	default:
		return errors.New("unknown HP message")
	}
	if len(p.incoming) == p.total {
		if !p.haveLast || p.currentMessage != p.lastMessage {
			response, err := p.handle(p.incoming)
			if err != nil {
				return err
			}
			if p.world.bufferNotice && p.incoming[5] == 3 && response[4] == 15 {
				notice := append([]byte(nil), response...)
				notice[6] = 1
				p.reply(notice)
			}
			p.reply(response)
			p.haveLast = true
			p.lastMessage = p.currentMessage
		}
		if p.dropAck > 0 {
			p.dropAck--
		} else {
			p.control(5, p.currentMessage)
		}
	}
	return nil
}
func (p *engageHPPeer) Read(ctx context.Context) ([]byte, error) {
	if len(p.queue) == 0 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	v := p.queue[0]
	p.queue = p.queue[1:]
	return v, nil
}
func independentSitelCRC(data []byte) uint16 {
	crc := uint16(65535)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
func (p *engageHPPeer) handle(packet []byte) ([]byte, error) {
	w := p.world
	if len(packet) < 6 || packet[0] != 1 || packet[1] != 0 || packet[4] != 15 || packet[3]&0xc0 != 0x40 {
		return nil, errors.New("bad FWU message")
	}
	op := packet[5]
	args := packet[6:]
	var body []byte
	switch op {
	case 0:
		body = make([]byte, 19)
		id := uint32(0x0b0e4050)
		if w.wrongImageID {
			id++
		}
		binary.LittleEndian.PutUint32(body, id)
		binary.LittleEndian.PutUint32(body[4:], 0x100)
		sector := uint32(256)
		if w.badGeometry {
			sector = 0
		}
		binary.LittleEndian.PutUint32(body[8:], sector)
		binary.LittleEndian.PutUint32(body[12:], 128)
		body[17] = 1
	case 1:
		if len(args) != 2 || args[0] != 3 {
			return nil, errors.New("bad area query")
		}
		area := w.areas[args[1]]
		body = make([]byte, 17+5)
		binary.LittleEndian.PutUint32(body, area.Address)
		binary.LittleEndian.PutUint32(body[4:], area.Size)
		body[16] = 5
		copy(body[17:], "4.0.0")
	case 2, 3, 4:
		if len(args) < 4 {
			return nil, errors.New("missing address")
		}
		address := binary.LittleEndian.Uint32(args)
		if _, ok := w.memory[address]; !ok {
			return nil, errors.New("write outside fixture flash")
		}
		if op == 2 {
			if address%256 != 0 || len(args) != 4 {
				return nil, errors.New("bad erase")
			}
			w.erases++
			for i := uint32(0); i < 256; i++ {
				w.memory[address+i] = 255
			}
			body = []byte{0}
		}
		if op == 3 {
			if packet[3] != 0x4b || len(args) <= 4 || len(args) > 132 {
				return nil, errors.New("bad variable write request")
			}
			w.writes++
			if w.failWrite > 0 && w.writes == w.failWrite {
				return []byte{0, 1, packet[2], 0xc6, 0xfe, 1}, nil
			}
			for i, b := range args[4:] {
				w.memory[address+uint32(i)] = b
			}
			body = []byte{0}
		}
		if op == 4 {
			if len(args) != 8 {
				return nil, errors.New("bad CRC request")
			}
			count := binary.LittleEndian.Uint32(args[4:])
			if count > 256 {
				return nil, errors.New("CRC exceeded sector")
			}
			data := make([]byte, count)
			for i := range data {
				data[i] = w.memory[address+uint32(i)]
			}
			crc := independentSitelCRC(data)
			if w.badVerify && w.writes > 0 {
				crc ^= 1
			}
			body = make([]byte, 2)
			binary.LittleEndian.PutUint16(body, crc)
		}
	case 5:
		if !bytes.Equal(args, []byte{3}) {
			return nil, errors.New("wrong boot mode")
		}
		for _, image := range w.images {
			for _, segment := range image.Segments {
				for i, b := range segment.Data {
					if w.memory[segment.Address+uint32(i)] != b {
						return nil, errors.New("boot before every image verified")
					}
				}
			}
		}
		w.bootMode = false
		w.version = "4.1.3"
	default:
		return nil, errors.New("unknown FWU opcode")
	}
	sequence := packet[2]
	if w.zeroSequence {
		sequence = 0
	}
	return append([]byte{0, 1, sequence, 0xc0 | byte(6+len(body)), 15, op}, body...), nil
}

func TestEngageFullUpdateAndControllerActivation(t *testing.T) {
	for _, controller := range []bool{false, true} {
		t.Run(fmt.Sprint(controller), func(t *testing.T) {
			w := makeEngageWorld(controller)
			state := firmwareRecoveryState{ArchiveSHA256: "synthetic"}
			saved := false
			w.checkpoint = func() bool { return saved }
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := runEngageInstall(ctx, w, w.device(), w.images, "4.1.3", &state, func() error { saved = true; return nil }, nil)
			if err != nil || w.version != "4.1.3" || w.writes == 0 || w.bootMode {
				t.Fatal(err, w.writes)
			}
			if controller && (w.controllerVersion != "4.1.3" || w.activations != 1) {
				t.Fatal("controller did not activate")
			}
			if controller && w.unsubscribes != 1 {
				t.Fatal("controller event subscription not cleaned up")
			}
			if !controller && w.activations != 0 {
				t.Fatal("absent controller activated")
			}
		})
	}
}

func TestEngageInterruptedBootloaderRecovery(t *testing.T) {
	w := makeEngageWorld(true)
	w.failWrite = 3
	state := firmwareRecoveryState{ArchiveSHA256: "synthetic"}
	save := func() error { return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runEngageInstall(ctx, w, w.device(), w.images, "4.1.3", &state, save, nil); err == nil || !w.bootMode || state.Phase != "flashing" {
		t.Fatal("expected interrupted transfer", err, state.Phase)
	}
	w.failWrite = 0
	if err := runEngageInstall(ctx, w, w.device(), w.images, "4.1.3", &state, save, nil); err != nil {
		t.Fatal("recovery failed", err)
	}
	if w.version != "4.1.3" || w.controllerVersion != "4.1.3" {
		t.Fatal("recovery not verified")
	}
}

func TestEngageUpdateFailuresStayRecoverable(t *testing.T) {
	for _, name := range []string{"checkpoint", "geometry", "image-id", "verify", "controller", "port", "cancel"} {
		t.Run(name, func(t *testing.T) {
			w := makeEngageWorld(true)
			state := firmwareRecoveryState{ArchiveSHA256: "synthetic"}
			save := func() error { return nil }
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			switch name {
			case "checkpoint":
				save = func() error { return errors.New("disk full") }
			case "geometry":
				w.badGeometry = true
			case "image-id":
				w.wrongImageID = true
			case "verify":
				w.badVerify = true
			case "controller":
				w.badController = true
			case "port":
				w.wrongPort = true
			case "cancel":
				cancel()
			}
			if err := runEngageInstall(ctx, w, w.device(), w.images, "4.1.3", &state, save, nil); err == nil {
				t.Fatal("failure accepted")
			}
			if (name == "checkpoint" || name == "geometry" || name == "image-id" || name == "port" || name == "cancel") && w.writes != 0 {
				t.Fatal("wrote after preflight failure")
			}
			if name == "controller" {
				before := w.writes
				w.badController = false
				if err := runEngageInstall(ctx, w, w.device(), w.images, "4.1.3", &state, save, nil); err != nil || w.writes != before {
					t.Fatal("controller recovery repeated headset flash", err)
				}
			}
		})
	}
}
