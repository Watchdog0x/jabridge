package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type link390Sim struct {
	mu                      sync.Mutex
	port, hid, serial, fw   string
	variant                 []byte
	value                   byte
	apply                   bool
	permission              []byte
	writeMode, endMode      string // ack nak lost
	frames                  []string
	reads, handles, unowned int
	peers                   []*os.File
	onFrame                 func(string)
	onRead                  func(int)
	dropInfo                int
}

func newLink390Sim(t *testing.T, name string) *link390Sim {
	port := t.TempDir()
	for n, v := range map[string]string{"idVendor": "0b0e", "idProduct": "2e57", "busnum": "1", "devnum": "2", "serial": "SERIAL-A"} {
		if err := os.WriteFile(filepath.Join(port, n), []byte(v), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return &link390Sim{port: port, hid: "/dev/simhidraw-" + name, serial: "SERIAL-A", fw: "3.2.0", variant: []byte{2, 4, 0x10}, value: 1, apply: true, permission: []byte{1}, writeMode: "ack", endMode: "ack"}
}

func (s *link390Sim) frame(kind string) {
	s.mu.Lock()
	s.frames = append(s.frames, kind)
	hook := s.onFrame
	s.mu.Unlock()
	if hook != nil {
		hook(kind)
	}
}
func (s *link390Sim) list() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.frames, ",")
}

// USB re-enumeration: old descriptors die, attachment and node number changeLink390Sim.
func (s *link390Sim) restart(path string) {
	s.mu.Lock()
	peers := s.peers
	s.peers = nil
	s.hid = path
	s.mu.Unlock()
	raw, _ := os.ReadFile(filepath.Join(s.port, "devnum"))
	var n int
	if _, err := fmt.Sscanf(string(raw), "%d", &n); err != nil {
		panic(err)
	}
	tmp, err := os.CreateTemp(s.port, ".devnum-")
	if err != nil {
		panic(err)
	}
	if _, err = fmt.Fprint(tmp, n+1); err != nil {
		_ = tmp.Close()
		panic(err)
	}
	if err = tmp.Close(); err != nil {
		panic(err)
	}
	if err = os.Rename(tmp.Name(), filepath.Join(s.port, "devnum")); err != nil {
		panic(err)
	}
	for _, p := range peers {
		_ = p.Close()
	}
}

func (s *link390Sim) open(path string, permit *controlPermit) (*hidrawConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if path != s.hid {
		return nil, unix.ENOENT
	}
	if permit == nil && link390ControlGate.active() {
		s.unowned++
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	s.handles++
	id := s.handles
	peer := os.NewFile(uintptr(fds[1]), "link390Sim-peer")
	s.peers = append(s.peers, peer)
	go s.serve(peer, id)
	return &hidrawConn{f: os.NewFile(uintptr(fds[0]), "link390Sim-host"), path: path}, nil
}

func (s *link390Sim) serve(peer *os.File, id int) {
	defer func() { _ = peer.Close() }()
	reply := func(seq, class, op byte, data []byte) {
		r, err := buildGNPReport(0, seq, 0xc0, class, op, data)
		if err == nil {
			r[2] = gnpSrcDongle
			_, _ = peer.Write(r)
		}
	}
	for {
		b := make([]byte, 64)
		n, err := peer.Read(b)
		if err != nil || n == 0 {
			return
		}
		seq, flags, class, op := b[3], b[4]&0xc0, b[5], b[6]
		payload := append([]byte(nil), b[7:1+int(b[4]&0x3f)]...)
		switch {
		case b[1] != gnpSrcDongle:
			s.frame(fmt.Sprintf("WRONG-ADDRESS:%d", b[1]))
		case flags == gnpFlagQuery && class == gnpClassDevInfo && op == gnpOpSerialNumber:
			s.mu.Lock()
			v := s.serial
			s.mu.Unlock()
			reply(seq, class, op, append([]byte{byte(len(v))}, v...))
		case flags == gnpFlagQuery && class == gnpClassDevInfo && op == gnpOpFirmwareVer:
			s.mu.Lock()
			v := s.fw
			s.mu.Unlock()
			reply(seq, class, op, append([]byte{byte(len(v))}, v...))
		case flags == gnpFlagQuery && class == gnpClassDevInfo && op == gnpOpDeviceInfo:
			s.mu.Lock()
			drop := s.dropInfo > 0
			if drop {
				s.dropInfo--
			}
			v := s.variant
			s.mu.Unlock()
			if !drop {
				reply(seq, class, op, v)
			}
		case flags == gnpFlagQuery && class == gnpClassConfig:
			s.mu.Lock()
			s.reads++
			c, v, hook := s.reads, s.value, s.onRead
			s.mu.Unlock()
			if hook != nil {
				hook(c)
			}
			reply(seq, class, op, []byte{v})
		case flags == gnpFlagQuery && class == gnpClassPairingDevice && op == 0x11:
			s.frame(fmt.Sprintf("permission@%d", id))
			s.mu.Lock()
			v := s.permission
			s.mu.Unlock()
			if v == nil {
				reply(seq, 0xfe, 9, nil)
			} else {
				reply(seq, class, op, v)
			}
		case flags == gnpFlagCmd && class == gnpClassConfig:
			s.frame(fmt.Sprintf("write:%d/%x@%d", payload[0], op, id))
			s.mu.Lock()
			if s.apply {
				s.value = payload[0]
			}
			m := s.writeMode
			s.mu.Unlock()
			switch m {
			case "ack":
				reply(seq, 0xff, 0, nil)
			case "nak":
				reply(seq, 0xfe, 9, nil)
			}
		case flags == gnpFlagCmd && class == gnpClassPairingDevice && op == 0x12:
			s.frame(fmt.Sprintf("end@%d", id))
			s.mu.Lock()
			m := s.endMode
			s.mu.Unlock()
			switch m {
			case "ack":
				reply(seq, 0xff, 0, nil)
			case "nak":
				reply(seq, 0xfe, 9, nil)
			}
		default:
			if flags == gnpFlagCmd {
				s.frame(fmt.Sprintf("UNEXPECTED-CMD:%x/%x", class, op))
			}
			reply(seq, 0xfe, 9, nil)
		}
	}
}

func installLink390Sim(t *testing.T, sims ...*link390Sim) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("STATE_DIRECTORY", "")
	byPath := func(path string) *link390Sim {
		for _, s := range sims {
			s.mu.Lock()
			hit := s.hid == path
			s.mu.Unlock()
			if hit {
				return s
			}
		}
		return nil
	}
	oldParent, oldFind, oldEnum, oldOpen := controlUSBParent, findHidrawPathsForDevice, enumerateJabraUSB, openHidrawRaw
	t.Cleanup(func() {
		controlUSBParent, findHidrawPathsForDevice, enumerateJabraUSB, openHidrawRaw = oldParent, oldFind, oldEnum, oldOpen
	})
	controlUSBParent = func(path string) (string, error) {
		s := byPath(path)
		if s == nil {
			return "", errors.New("no node")
		}
		if _, err := os.Stat(s.port); err != nil {
			return "", err
		}
		return s.port, nil
	}
	findHidrawPathsForDevice = func(d *jabra_DeviceInfo) []string {
		var out []string
		for i := len(sims) - 1; i >= 0; i-- { // foreign node first on purpose
			if _, err := os.Stat(sims[i].port); err == nil {
				sims[i].mu.Lock()
				out = append(out, sims[i].hid)
				sims[i].mu.Unlock()
			}
		}
		return out
	}
	enumerateJabraUSB = func() ([]usbDev, error) {
		var out []usbDev
		for _, s := range sims {
			if _, err := os.Stat(s.port); err == nil {
				out = append(out, usbDev{sysPath: s.port, vendorID: jabraVendorID, productID: 0x2e57, serial: readTextFile(filepath.Join(s.port, "serial"))})
			}
		}
		return out, nil
	}
	openHidrawRaw = func(path string, permit *controlPermit) (*hidrawConn, error) {
		s := byPath(path)
		if s == nil {
			return nil, unix.ENOENT
		}
		return s.open(path, permit)
	}
}

func link390SimRegistry(t *testing.T, s *link390Sim) *jabra_DeviceInfo {
	live := &jabra_DeviceInfo{deviceID: 7, instance: "inst-1", productID: 0x2e57, vendorID: jabraVendorID, isDongle: true, deviceConnection: deviceConnectionType_USB, usbDevicePath: s.port, hidrawPath: s.hid, serialNumber: "SERIAL-A", firmwareVersion: "3.2.0", variantType: "04-10", featureFlags: &featureFlags{}}
	child := &jabra_DeviceInfo{deviceID: 8, instance: "child", productID: 0x4100, vendorID: jabraVendorID, deviceConnection: deviceConnectionType_BT, parentDeviceID: 7, hidrawPath: s.hid, batteryStatus: &batteryStatus{levelInPercent: 61, component: batteryHeadband}, featureFlags: &featureFlags{}}
	withDeviceState(t, devices{7: live, 8: child}, 8, 7)
	return cloneDeviceInfo(live)
}

func assertLink390Idle(t *testing.T) {
	t.Helper()
	link390ControlGate.mu.Lock()
	ports, unbound := len(link390ControlGate.ports), link390ControlGate.unbound
	link390ControlGate.mu.Unlock()
	if ports != 0 || unbound != 0 {
		t.Fatalf("gate leaked ports=%d unbound=%d", ports, unbound)
	}
	release, err := beginSettingTransaction()
	if err != nil {
		t.Fatalf("lease leaked: %v", err)
	}
	release()
}

func changeLink390Sim(t *testing.T, s *link390Sim, key string, value byte, expected *byte) (verifiedLink390Change, error) {
	t.Helper()
	snapshot := link390SimRegistry(t, s)
	d, _ := link390ConfigDefinitionForKey(key)
	v, err := changeLink390Config(snapshot, d, value, expected)
	assertLink390Idle(t)
	return v, err
}

func TestLink390NativeHappyNoRestart(t *testing.T) {
	s := newLink390Sim(t, "a")
	installLink390Sim(t, s)
	started := time.Now()
	v, err := changeLink390Sim(t, s, "wireless-range", 3, nil)
	if err != nil || v.Result.Status != link390Applied || v.Device == nil || *v.Result.ReadBack != 3 {
		t.Fatal(v, err)
	}
	if time.Since(started) < 4*time.Second {
		t.Fatal("settle bypassed", time.Since(started))
	}
	if got := s.list(); got != "permission@1,write:3/e@1,end@1" {
		t.Fatal(got)
	}
	if d := deviceForID(7); d.instance != "inst-1" || d.hidrawPath != s.hid {
		t.Fatal("same attachment should keep the instance", d.instance)
	}
}

func TestLink390NativeRestartOnEndRenumbers(t *testing.T) {
	s := newLink390Sim(t, "b")
	installLink390Sim(t, s)
	s.endMode = "lost"
	s.onFrame = func(k string) {
		if strings.HasPrefix(k, "end") {
			s.restart("/dev/simhidraw-b2")
		}
	}
	v, err := changeLink390Sim(t, s, "softphone-integration", 0, nil)
	if err != nil || v.Result.Status != link390Applied {
		t.Fatal(v.Result, err)
	}
	d := deviceForID(7)
	if d.hidrawPath != "/dev/simhidraw-b2" || d.instance == "inst-1" || v.Device.instance != d.instance {
		t.Fatal("path/instance not published", d.hidrawPath, d.instance)
	}
	if got := s.list(); !strings.HasPrefix(got, "permission@1,write:0/4c@1,end@1") || strings.Count(got, "write") != 1 || strings.Count(got, "end@") != 1 {
		t.Fatal(got)
	}
}

func TestLink390NativeAbsenceWindowThenReturn(t *testing.T) {
	s := newLink390Sim(t, "c")
	installLink390Sim(t, s)
	s.endMode = "lost"
	port := s.port
	s.onFrame = func(k string) {
		if !strings.HasPrefix(k, "end") {
			return
		}
		s.restart("/dev/simhidraw-gone")
		_ = os.Rename(port, port+".gone")
		time.AfterFunc(4500*time.Millisecond, func() { _ = os.Rename(port+".gone", port); s.mu.Lock(); s.hid = "/dev/simhidraw-c2"; s.mu.Unlock() })
	}
	v, err := changeLink390Sim(t, s, "wireless-range", 2, nil)
	if err != nil || v.Result.Status != link390Applied || deviceForID(7).hidrawPath != "/dev/simhidraw-c2" {
		t.Fatal(v.Result, err)
	}
	if deviceForID(8) == nil {
		t.Fatal("child removed across a verified restart")
	}
}

func TestLink390NativeEndNakNeverApplied(t *testing.T) {
	s := newLink390Sim(t, "d")
	installLink390Sim(t, s)
	s.endMode = "nak"
	v, err := changeLink390Sim(t, s, "wireless-range", 3, nil)
	if err == nil || v.Result.Status != link390Unknown {
		t.Fatal(v.Result, err)
	}
	if d := deviceForID(7); d == nil || d.instance != "inst-1" {
		t.Fatal("verified attachment should stay registered", d)
	}
}

func TestLink390NativeLostWriteAckValueChangedStaysUnknown(t *testing.T) {
	s := newLink390Sim(t, "e")
	installLink390Sim(t, s)
	s.writeMode = "lost"
	v, err := changeLink390Sim(t, s, "wireless-range", 3, nil)
	if err == nil || v.Result.Status != link390Unknown || strings.Count(s.list(), "write") != 1 || strings.Count(s.list(), "end@") != 1 {
		t.Fatal(v.Result, err, s.list())
	}
}

func TestLink390NativeLostWriteAckContraryReads(t *testing.T) {
	s := newLink390Sim(t, "f")
	installLink390Sim(t, s)
	s.writeMode = "lost"
	s.apply = false
	v, err := changeLink390Sim(t, s, "wireless-range", 3, nil)
	t.Logf("OBSERVED lost write ACK + stable contrary reads: status=%q attempted=%v err=%v", v.Result.Status, v.Result.Attempted, err)
	if err == nil || v.Result.Status != link390Unknown {
		t.Fatal(v.Result)
	}
}

func TestLink390NativeWireMismatchesBeforeWrite(t *testing.T) {
	for name, mutate := range map[string]func(*link390Sim){
		"variant":  func(s *link390Sim) { s.variant = []byte{2, 4, 0x11} },
		"firmware": func(s *link390Sim) { s.fw = "3.3.0" },
		"serial":   func(s *link390Sim) { s.serial = "SERIAL-B" },
	} {
		t.Run(name, func(t *testing.T) {
			s := newLink390Sim(t, "g")
			installLink390Sim(t, s)
			mutate(s)
			v, err := changeLink390Sim(t, s, "wireless-range", 3, nil)
			if !errors.Is(err, errLink390Identity) || v.Result.Status != link390NotApplied || s.list() != "" {
				t.Fatal(v.Result, err, s.list())
			}
			if deviceForID(7) != nil || deviceForID(8) != nil {
				t.Fatal("unverified slot kept")
			}
		})
	}
}

func TestLink390NativeWireMismatchesAfterWrite(t *testing.T) {
	for name, mutate := range map[string]func(*link390Sim){
		"variant":  func(s *link390Sim) { s.variant = []byte{2, 4, 0x11} },
		"firmware": func(s *link390Sim) { s.fw = "3.3.0" },
		"usb-serial": func(s *link390Sim) {
			_ = os.WriteFile(filepath.Join(s.port, "serial"), []byte("SERIAL-B"), 0600)
		},
		"gnp-serial": func(s *link390Sim) { s.serial = "SERIAL-B" },
	} {
		t.Run(name, func(t *testing.T) {
			s := newLink390Sim(t, "h")
			installLink390Sim(t, s)
			s.onFrame = func(k string) {
				if strings.HasPrefix(k, "end") {
					mutate(s)
					s.restart("/dev/simhidraw-h2")
				}
			}
			s.endMode = "lost"
			v, err := changeLink390Sim(t, s, "wireless-range", 3, nil)
			if err == nil || v.Result.Status != link390Unknown || strings.Count(s.list(), "write") != 1 {
				t.Fatal(v.Result, err, s.list())
			}
			if deviceForID(7) != nil {
				t.Fatal("wrong device kept")
			}
		})
	}
}

func TestLink390NativeNoOpAndStaleEdit(t *testing.T) {
	s := newLink390Sim(t, "i")
	installLink390Sim(t, s)
	v, err := changeLink390Sim(t, s, "wireless-range", 1, nil)
	if err != nil || v.Result.Status != link390Applied || s.list() != "" {
		t.Fatal(v.Result, err, s.list())
	}
	old := byte(2)
	v, err = changeLink390Sim(t, s, "wireless-range", 3, &old)
	if err == nil || v.Result.Status != link390NotApplied || s.list() != "" || deviceForID(7) == nil {
		t.Fatal(v.Result, err, s.list())
	}
}

func TestLink390NativePermissionOutcomes(t *testing.T) {
	s := newLink390Sim(t, "j")
	installLink390Sim(t, s)
	s.permission = nil // NAK
	v, err := changeLink390Sim(t, s, "wireless-range", 3, nil)
	if err == nil || v.Result.Status != link390NotApplied || s.list() != "permission@1" {
		t.Fatal(v.Result, err, s.list())
	}
	s = newLink390Sim(t, "j2")
	installLink390Sim(t, s)
	s.permission = []byte{0} // denied
	v, err = changeLink390Sim(t, s, "wireless-range", 3, nil)
	if err == nil || v.Result.Status != link390NotApplied || s.list() != "permission@1" {
		t.Fatal(v.Result, err, s.list())
	}
	s = newLink390Sim(t, "j3")
	installLink390Sim(t, s)
	s.permission = []byte{7} // malformed: cleanup yes, write no
	v, err = changeLink390Sim(t, s, "wireless-range", 3, nil)
	if err == nil || v.Result.Status != link390NotApplied || s.list() != "permission@1,end@1" {
		t.Fatal(v.Result, err, s.list())
	}
}

func TestLink390NativeOwnershipRefusals(t *testing.T) {
	s := newLink390Sim(t, "k")
	installLink390Sim(t, s)
	snapshot := link390SimRegistry(t, s)
	d, _ := link390ConfigDefinitionForKey("wireless-range")
	search, err := link390ControlGate.shared(s.port, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := changeLink390Config(snapshot, d, 3, nil); !errors.Is(err, errSearchActive) {
		t.Fatal(err)
	}
	search.release()
	reader, _ := link390ControlGate.shared(s.port, false)
	started := time.Now()
	if _, err := changeLink390Config(snapshot, d, 3, nil); !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 6*time.Second {
		t.Fatal(err, time.Since(started))
	}
	reader.release()
	release, _ := beginSettingTransaction()
	if _, err := changeLink390Config(snapshot, d, 3, nil); err == nil || link390ControlGate.active() {
		t.Fatal("lease bypassed", err)
	}
	release()
	if s.list() != "" || deviceForID(7) == nil {
		t.Fatal(s.list())
	}
	assertLink390Idle(t)
	stale := cloneDeviceInfo(snapshot)
	stale.instance = "old"
	if _, err := changeLink390Config(stale, d, 3, nil); err == nil || s.list() != "" {
		t.Fatal("stale snapshot passed", err)
	}
	forged := d
	forged.Op = 0x11
	for _, bad := range []struct {
		device *jabra_DeviceInfo
		def    link390ConfigDefinition
		value  byte
	}{{snapshot, forged, 1}, {snapshot, d, 4}, {snapshot, d, 0}} {
		if _, err := changeLink390Config(bad.device, bad.def, bad.value, nil); !errors.Is(err, ErrNotSupported) {
			t.Fatal(err)
		}
	}
	for _, mutate := range []func(*jabra_DeviceInfo){
		func(x *jabra_DeviceInfo) { x.firmwareVersion = "3.0.13" },
		func(x *jabra_DeviceInfo) { x.variantType = "04-11" },
		func(x *jabra_DeviceInfo) { x.productID = 0x2e50 },
		func(x *jabra_DeviceInfo) { x.isDongle = false },
		func(x *jabra_DeviceInfo) { x.serialNumber = "" },
	} {
		x := cloneDeviceInfo(snapshot)
		mutate(x)
		if _, err := changeLink390Config(x, d, 3, nil); !errors.Is(err, ErrNotSupported) {
			t.Fatal(err)
		}
	}
	assertLink390Idle(t)
}

func TestLink390NativeNewerInstanceSurvivesFailure(t *testing.T) {
	s := newLink390Sim(t, "l")
	installLink390Sim(t, s)
	s.endMode = "nak"
	s.onFrame = func(k string) {
		if strings.HasPrefix(k, "end") {
			updateDeviceByID(7, func(d *jabra_DeviceInfo) { d.instance = "newer"; d.hidrawPath = "/dev/simhidraw-newer" })
		}
	}
	v, err := changeLink390Sim(t, s, "wireless-range", 3, nil)
	if err == nil || v.Result.Status != link390Unknown {
		t.Fatal(v.Result, err)
	}
	if d := deviceForID(7); d == nil || d.instance != "newer" || d.hidrawPath != "/dev/simhidraw-newer" {
		t.Fatal("newer link390SimRegistry instance was touched", d)
	}
}

func TestLink390NativeForeignNodeAndDuplicateSerial(t *testing.T) {
	a, b := newLink390Sim(t, "m-a"), newLink390Sim(t, "m-b")
	_ = os.WriteFile(filepath.Join(b.port, "serial"), []byte("SERIAL-B"), 0600)
	b.serial = "SERIAL-B"
	installLink390Sim(t, a, b)
	v, err := changeLink390Sim(t, a, "wireless-range", 3, nil)
	if err != nil || v.Result.Status != link390Applied || b.list() != "" || b.handles != 0 {
		t.Fatal(v.Result, err, b.list(), b.handles)
	}
	c, e := newLink390Sim(t, "n-a"), newLink390Sim(t, "n-b")
	installLink390Sim(t, c, e)
	v, err = changeLink390Sim(t, c, "wireless-range", 3, nil)
	if !errors.Is(err, errLink390Identity) || c.list() != "" || e.list() != "" || v.Result.Status != link390NotApplied {
		t.Fatal(v.Result, err)
	}
}

func TestLink390NativeRestartBetweenCountedReads(t *testing.T) {
	s := newLink390Sim(t, "o")
	installLink390Sim(t, s)
	s.onRead = func(n int) {
		if n == 3 { // pre-read, counted read 1, then detach during read 2
			s.restart("/dev/simhidraw-o2")
		}
	}
	v, err := changeLink390Sim(t, s, "wireless-range", 3, nil)
	if err != nil || v.Result.Status != link390Applied || deviceForID(7).hidrawPath != "/dev/simhidraw-o2" {
		t.Fatal(v.Result, err)
	}
	if s.reads < 5 {
		t.Fatal("restart did not restart the count", s.reads)
	}
}

func TestLink390NativeInfoTimeoutAfterRestart(t *testing.T) {
	s := newLink390Sim(t, "p")
	installLink390Sim(t, s)
	s.endMode = "lost"
	s.onFrame = func(k string) {
		if strings.HasPrefix(k, "end") {
			s.restart("/dev/simhidraw-p2")
			s.mu.Lock()
			s.dropInfo = 1
			s.mu.Unlock()
		}
	}
	started := time.Now()
	v, err := changeLink390Sim(t, s, "wireless-range", 3, nil)
	t.Logf("OBSERVED one device-info timeout after restart: status=%q elapsed=%s err=%v", v.Result.Status, time.Since(started).Round(100*time.Millisecond), err)
	if err != nil || v.Result.Status != link390Applied {
		t.Fatal("transient info timeout prevented recovery", v.Result, err)
	}
}

func TestLink390NativePollersWhileOwned(t *testing.T) {
	s := newLink390Sim(t, "q")
	installLink390Sim(t, s)
	var stop atomic.Bool
	done := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	s.onFrame = func(k string) {
		if strings.HasPrefix(k, "permission") {
			s.mu.Lock()
			s.unowned = 0
			s.mu.Unlock()
			once.Do(func() { close(started) })
		}
	}
	go func() {
		defer close(done)
		<-started
		for !stop.Load() {
			refreshSelectedDeviceData()
			time.Sleep(20 * time.Millisecond)
		}
	}()
	snapshot := link390SimRegistry(t, s)
	d, _ := link390ConfigDefinitionForKey("wireless-range")
	v, err := changeLink390Config(snapshot, d, 3, nil)
	stop.Store(true)
	<-done
	assertLink390Idle(t)
	if err != nil || v.Result.Status != link390Applied {
		t.Fatal(v.Result, err)
	}
	s.mu.Lock()
	unowned := s.unowned
	s.mu.Unlock()
	if unowned != 0 {
		t.Fatalf("%d unowned handles reached the dongle during the transaction", unowned)
	}
	if c := deviceForID(8); c == nil || c.batteryStatus == nil || c.batteryStatus.levelInPercent != 61 {
		t.Fatal("cached state lost", c)
	}
}

func TestLink390NativeCachedPIDMustMatchPhysicalPort(t *testing.T) {
	s := newLink390Sim(t, "wrong-pid")
	installLink390Sim(t, s)
	if err := os.WriteFile(filepath.Join(s.port, "idProduct"), []byte("2e56"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := changeLink390Sim(t, s, "wireless-range", 2, nil)
	if !errors.Is(err, errLink390Identity) || result.Result.Attempted || s.handles != 0 || s.list() != "" {
		t.Fatal("physical PID mismatch reached transport", result.Result, err, s.handles, s.list())
	}
}
