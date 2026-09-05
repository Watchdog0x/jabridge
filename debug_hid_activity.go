package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Watchdog0x/jabridge/internal/firmware"
	"golang.org/x/sys/unix"
)

type hidActivity struct {
	previous         map[byte][]byte
	counts           map[byte]int
	changes          map[byte]map[int]bool
	gnpEvents        map[string]int
	layouts          map[byte]firmware.HIDReport
	gnp              bool
	started          time.Time
	samples          []string
	omitted, invalid int
}

func newHIDActivity() *hidActivity {
	return &hidActivity{previous: map[byte][]byte{}, counts: map[byte]int{}, changes: map[byte]map[int]bool{}, gnpEvents: map[string]int{}, layouts: map[byte]firmware.HIDReport{}, started: time.Now()}
}

func hidActivityForReports(reports []firmware.HIDReport) *hidActivity {
	a := newHIDActivity()
	input, output := false, false
	for _, report := range reports {
		if report.Kind == "input" {
			a.layouts[report.ID] = report
		}
		if report.ID != 5 || report.Bytes != 63 && report.Bytes != 64 {
			continue
		}
		vendor := false
		for _, field := range report.Fields {
			if field.UsagePage == 0xff00 {
				vendor = true
			}
		}
		if vendor && report.Kind == "input" {
			input = true
		}
		if vendor && report.Kind == "output" {
			output = true
		}
	}
	a.gnp = input && output
	return a
}

func (a *hidActivity) sample(text string) {
	if len(a.samples) >= 128 {
		a.omitted++
		return
	}
	a.samples = append(a.samples, fmt.Sprintf("at=%dms %s", time.Since(a.started).Milliseconds(), text))
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
		a.invalid++
		return
	}
	if id == 5 && a.gnp {
		// Record only event headers, never GNP reply payloads or serial reads.
		if len(packet) >= 7 && packet[4]&0xc0 == 0 {
			key := fmt.Sprintf("class=%02x op=%02x", packet[5], packet[6])
			if len(a.gnpEvents) < 64 || a.gnpEvents[key] > 0 {
				a.gnpEvents[key]++
			} else {
				a.omitted++
			}
			a.sample("GNP event " + key)
		}
		return
	}
	a.counts[id]++
	var changed []int
	if previous := a.previous[id]; len(previous) == len(packet) {
		if a.changes[id] == nil {
			a.changes[id] = map[int]bool{}
		}
		for i := prefix; i < len(packet); i++ {
			difference := previous[i] ^ packet[i]
			for bit := 0; bit < 8; bit++ {
				if difference&(1<<bit) != 0 {
					position := (i-prefix)*8 + bit
					if len(a.changes[id]) < 256 {
						a.changes[id][position] = true
					}
					if len(changed) < 32 {
						changed = append(changed, position)
					} else {
						a.omitted++
					}
				}
			}
		}
	}
	if len(changed) > 0 {
		a.sample(fmt.Sprintf("report=%d changed-bits=%v fields=%s", id, changed, describeHIDChanges(a.layouts[id], changed)))
	} else if len(a.previous[id]) == 0 {
		a.sample(fmt.Sprintf("report=%d baseline received (%d bytes); values omitted", id, len(packet)))
	}
	a.previous[id] = append([]byte(nil), packet...)
}

func describeHIDChanges(report firmware.HIDReport, bits []int) string {
	var result []string
	for _, bit := range bits {
		for _, field := range report.Fields {
			if bit < 0 || uint64(bit) < field.OffsetBits || uint64(bit) >= field.OffsetBits+uint64(field.SizeBits)*uint64(field.Count) || field.SizeBits == 0 {
				continue
			}
			if field.Flags&1 != 0 {
				break
			} // Constant/padding fields are not controls.
			index := (uint64(bit) - field.OffsetBits) / uint64(field.SizeBits)
			if field.Flags&2 == 0 {
				result = append(result, fmt.Sprintf("bit%d:page=%04x,array-range=%x..%x", bit, field.UsagePage, field.UsageMin, field.UsageMax))
			} else {
				usage := uint32(0)
				if index < uint64(len(field.Usages)) {
					usage = field.Usages[index]
				} else if len(field.Usages) > 0 {
					usage = field.Usages[len(field.Usages)-1]
				} else if uint64(field.UsageMin)+index <= uint64(field.UsageMax) {
					usage = field.UsageMin + uint32(index)
				}
				page := field.UsagePage
				if usage > 0xffff {
					page, usage = usage>>16, usage&0xffff
				}
				result = append(result, fmt.Sprintf("bit%d:page=%04x,usage=%04x", bit, page, usage))
			}
			break
		}
	}
	if len(result) == 0 {
		return "unmapped"
	}
	return strings.Join(result, ";")
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
	for _, device := range diagnosticHIDNodes() {
		path := device.Path
		if len(nodes) >= 16 {
			fmt.Fprintln(&out, "INCOMPLETE: HID observer limited to 16 nodes")
			break
		}
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
		nodes = append(nodes, inputNode{path: device.label(), fd: fd, reports: inputs, activity: hidActivityForReports(reports)})
		poll = append(poll, unix.PollFd{Fd: int32(fd), Events: unix.POLLIN})
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
			} else if err != unix.EAGAIN && err != unix.EINTR {
				fmt.Fprintf(&out, "%s observation stopped: %s\n", nodes[index].path, diagnosticError(err))
				break observation
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
	for _, sample := range a.samples {
		fmt.Fprintf(&out, "  %s %s\n", node, sample)
	}
	if a.omitted > 0 || a.invalid > 0 {
		fmt.Fprintf(&out, "  %s omitted-details=%d wrong-size-or-unknown-report=%d\n", node, a.omitted, a.invalid)
	}
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
