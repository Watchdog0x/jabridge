package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/Watchdog0x/jabridge/internal/firmware"
	"golang.org/x/sys/unix"
)

type hidActivity struct {
	previous  map[byte][]byte
	counts    map[byte]int
	changes   map[byte]map[int]bool
	gnpEvents map[string]int
}

func newHIDActivity() *hidActivity {
	return &hidActivity{previous: map[byte][]byte{}, counts: map[byte]int{}, changes: map[byte]map[int]bool{}, gnpEvents: map[string]int{}}
}

func (a *hidActivity) observe(packet []byte, reports map[byte]int) {
	if len(packet) == 0 {
		return
	}
	id := packet[0]
	prefix := 1
	if _, ok := reports[0]; ok {
		id = 0
		prefix = 0
	}
	if expected, ok := reports[id]; !ok || len(packet) != expected {
		return
	}
	if id == 5 {
		// Record only event headers, never GNP reply payloads or serial reads.
		if len(packet) >= 7 && packet[4]&0xc0 == 0 {
			a.gnpEvents[fmt.Sprintf("class=%02x op=%02x", packet[5], packet[6])]++
		}
		return
	}
	a.counts[id]++
	if previous := a.previous[id]; len(previous) == len(packet) {
		if a.changes[id] == nil {
			a.changes[id] = map[int]bool{}
		}
		for i := prefix; i < len(packet); i++ {
			difference := previous[i] ^ packet[i]
			for bit := 0; bit < 8; bit++ {
				if difference&(1<<bit) != 0 {
					a.changes[id][(i-prefix)*8+bit] = true
				}
			}
		}
	}
	a.previous[id] = append([]byte(nil), packet...)
}

func observeHIDActivity(ctx context.Context) string {
	var out bytes.Buffer
	type inputNode struct {
		path     string
		fd       int
		reports  map[byte]int
		activity *hidActivity
	}
	var nodes []inputNode
	var poll []unix.PollFd
	defer func() {
		for _, node := range nodes {
			_ = unix.Close(node.fd)
		}
	}()
	devices, _ := enumerateJabraUSB()
	seen := map[string]bool{}
	for _, device := range devices {
		for _, path := range findHidrawPathsForPID(device.vendorID, device.productID) {
			if seen[path] {
				continue
			}
			seen[path] = true
			reports, err := firmware.InspectHIDReports(path)
			if err != nil {
				fmt.Fprintf(&out, "%s HID observation: %s\n", filepath.Base(path), diagnosticError(err))
				continue
			}
			inputs := map[byte]int{}
			for _, report := range reports {
				if report.Kind == "input" {
					inputs[report.ID] = report.Bytes
				}
			}
			if len(inputs) == 0 {
				continue
			}
			fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			if err != nil {
				fmt.Fprintf(&out, "%s HID observation: %s\n", filepath.Base(path), diagnosticError(err))
				continue
			}
			nodes = append(nodes, inputNode{path: filepath.Base(path), fd: fd, reports: inputs, activity: newHIDActivity()})
			poll = append(poll, unix.PollFd{Fd: int32(fd), Events: unix.POLLIN})
		}
	}
	buffer := make([]byte, 8192)
observation:
	for len(nodes) > 0 && ctx.Err() == nil {
		if _, err := unix.Poll(poll, 100); err != nil {
			if err == unix.EINTR {
				continue
			}
			fmt.Fprintln(&out, "HID observation stopped: poll failed")
			break
		}
		for index, fd := range poll {
			if fd.Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
				fmt.Fprintf(&out, "%s disconnected during observation\n", nodes[index].path)
				break observation
			}
			if fd.Revents&unix.POLLIN == 0 {
				continue
			}
			n, err := unix.Read(int(fd.Fd), buffer)
			if err == nil && n > 0 {
				nodes[index].activity.observe(buffer[:n], nodes[index].reports)
			}
		}
	}
	fmt.Fprintln(&out, "Passive HID activity (changed bit positions only, no input values):")
	for _, node := range nodes {
		fmt.Fprint(&out, node.activity.summary(node.path))
	}
	fmt.Fprintln(&out, "No traffic does not prove absent buttons. Vendor event subscription/decoding may still be needed. Observed changes alone do not assign a button meaning.")
	return out.String()
}

func (a *hidActivity) summary(node string) string {
	var out bytes.Buffer
	var ids []int
	for id := range a.counts {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	for _, id := range ids {
		var bits []int
		for bit := range a.changes[byte(id)] {
			bits = append(bits, bit)
		}
		sort.Ints(bits)
		fmt.Fprintf(&out, "  %s report=%d observed=%d changed-bits=%v\n", node, id, a.counts[byte(id)], bits)
	}
	var events []string
	for event := range a.gnpEvents {
		events = append(events, event)
	}
	sort.Strings(events)
	for _, event := range events {
		fmt.Fprintf(&out, "  %s GNP event %s observed=%d\n", node, event, a.gnpEvents[event])
	}
	if len(ids) == 0 && len(events) == 0 {
		fmt.Fprintf(&out, "  %s: no report activity observed\n", node)
	}
	return out.String()
}
