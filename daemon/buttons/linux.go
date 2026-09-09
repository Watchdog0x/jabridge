package buttons

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Watchdog0x/jabridge/internal/firmware"
	"github.com/Watchdog0x/jabridge/internal/gnpevents"
	"golang.org/x/sys/unix"
)

type Source struct {
	ID          string    `json:"id"`
	PID         uint16    `json:"pid"`
	Connection  string    `json:"connection"`
	Descriptor  string    `json:"descriptorSha256,omitempty"`
	Controls    []Control `json:"controls"`
	Ready       bool      `json:"ready"`
	ObservesGNP bool      `json:"observesGnp"`
	Error       string    `json:"error,omitempty"`
}

type Observation struct {
	Source Source
	Edge   Edge
	Signal *gnpevents.Signal
	At     time.Time
}
type Monitor func(context.Context, func([]Source), func(Observation))

func token() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func hidIdentity(text string) (bus, pid uint16, ok bool) {
	for _, line := range strings.Split(text, "\n") {
		value, exists := strings.CutPrefix(line, "HID_ID=")
		if !exists {
			continue
		}
		var b, v, p uint32
		if _, err := fmt.Sscanf(value, "%x:%x:%x", &b, &v, &p); err == nil && v == 0x0b0e && b <= 65535 && p <= 65535 {
			return uint16(b), uint16(p), true
		}
	}
	return 0, 0, false
}

type watchedNode struct {
	source   Source
	fd       int
	decoder  *InputDecoder
	identity string
}

// NativeMonitor uses separate read-only handles. Linux duplicates hidraw
// input reports per open handle; management replies remain with their owner.
// It does not request feature reports or enable vendor event subscriptions.
func NativeMonitor(ctx context.Context, publish func([]Source), emit func(Observation)) {
	nodes := map[string]*watchedNode{}
	defer func() {
		for _, node := range nodes {
			if node.fd >= 0 {
				_ = unix.Close(node.fd)
			}
		}
	}()
	rescan := time.Time{}
	for ctx.Err() == nil {
		if time.Now().After(rescan) {
			rescan = time.Now().Add(2 * time.Second)
			entries, err := os.ReadDir("/sys/class/hidraw")
			seen := map[string]bool{}
			if err == nil {
				for _, entry := range entries {
					if len(seen) >= 32 {
						break
					}
					name := entry.Name()
					if !strings.HasPrefix(name, "hidraw") {
						continue
					}
					if _, err := strconv.ParseUint(strings.TrimPrefix(name, "hidraw"), 10, 32); err != nil {
						continue
					}
					data, err := os.ReadFile(filepath.Join("/sys/class/hidraw", name, "device/uevent"))
					if err != nil {
						continue
					}
					bus, pid, ok := hidIdentity(string(data))
					if !ok {
						continue
					}
					seen[name] = true
					identity, err := filepath.EvalSymlinks(filepath.Join("/sys/class/hidraw", name, "device"))
					if err != nil {
						continue
					}
					if old := nodes[name]; old != nil && old.identity == identity && old.source.PID == pid && old.fd >= 0 {
						continue
					}
					if old := nodes[name]; old != nil && old.fd >= 0 {
						_ = unix.Close(old.fd)
					}
					source := Source{ID: token(), PID: pid, Connection: "usb", Controls: []Control{}}
					if bus == 5 {
						source.Connection = "system-bluetooth"
					} else if bus != 3 {
						source.Connection = fmt.Sprintf("hid-bus-%04x", bus)
					}
					node := &watchedNode{source: source, fd: -1, identity: identity}
					nodes[name] = node
					path := filepath.Join("/dev", name)
					layouts, err := firmware.InspectHIDReports(path)
					if err != nil {
						node.source.Error = "Cannot read HID descriptor; check device access"
						continue
					}
					node.source.Descriptor, _ = firmware.HIDDescriptorFingerprint(path)
					node.decoder = NewInputDecoder(pid, bus, layouts)
					node.source.Controls = node.decoder.Controls
					node.source.ObservesGNP = node.decoder.ObservesGNP()
					if len(node.decoder.Controls) == 0 && !node.source.ObservesGNP {
						node.source.Error = "No supported control input fields; other controls may still exist"
						continue
					}
					node.fd, err = unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
					if err != nil {
						node.source.Error = "Cannot listen to HID input; run jabridge setup"
						continue
					}
					node.source.Ready = true
				}
			}
			for name, node := range nodes {
				if !seen[name] {
					if node.fd >= 0 {
						_ = unix.Close(node.fd)
					}
					delete(nodes, name)
				}
			}
			var sources []Source
			for _, node := range nodes {
				sources = append(sources, node.source)
			}
			publish(sources)
		}
		var poll []unix.PollFd
		var active []*watchedNode
		for _, node := range nodes {
			if node.fd >= 0 {
				poll = append(poll, unix.PollFd{Fd: int32(node.fd), Events: unix.POLLIN})
				active = append(active, node)
			}
		}
		if _, err := unix.Poll(poll, 100); err != nil {
			continue
		}
		for i, event := range poll {
			node := active[i]
			if event.Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
				_ = unix.Close(node.fd)
				node.fd = -1
				node.source.Ready = false
				rescan = time.Time{}
				continue
			}
			if event.Revents&unix.POLLIN == 0 {
				continue
			}
			var packet [8193]byte
			// One read per ready source per poll keeps noisy devices bounded.
			n, err := unix.Read(node.fd, packet[:])
			if err == unix.EAGAIN || err == unix.EINTR {
				continue
			}
			if err != nil || n <= 0 {
				_ = unix.Close(node.fd)
				node.fd = -1
				node.source.Ready = false
				rescan = time.Time{}
				continue
			}
			edges, signal := node.decoder.Decode(packet[:n])
			for _, edge := range edges {
				emit(Observation{Source: node.source, Edge: edge, At: time.Now()})
			}
			if signal != nil {
				emit(Observation{Source: node.source, Signal: signal, At: time.Now()})
			}
		}
	}
}
