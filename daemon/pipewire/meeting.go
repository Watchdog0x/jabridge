// pipewire/meeting — call/meeting detection for Jabridge.
//
// Detects active voice/video calls by checking PipeWire streams:
// a communication-role stream or a known app's capture stream
// linked to a Jabra microphone node means a call is in progress.

package pipewire

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Known communication apps. When any of these has an active capture
// stream linked to a Jabra mic, we consider it a call.
var commApps = map[string]bool{
	"firefox":         true,
	"chromium":        true,
	"chrome":          true,
	"google-chrome":   true,
	"microsoft teams": true,
	"teams":           true,
	"zoom":            true,
	"discord":         true,
	"skype":           true,
	"webex":           true,
	"slack":           true,
	"signal-desktop":  true,
	"signal":          true,
	"element":         true,
	"obs":             true,
	"pipewire-pulse":  true,
	"teams-for-linux": true,
}

// CallState represents whether a call is currently active.
type CallState struct {
	InCall  bool
	AppName string // which app is in the call
	Since   time.Time
}

// CallCallback is called when call state changes.
type CallCallback func(state CallState)

// Monitor polls PipeWire and detects call state changes.
type Monitor struct {
	mu       sync.Mutex
	state    CallState
	callback CallCallback
	stop     chan struct{}
	stopOnce sync.Once
	interval time.Duration
}

// NewMonitor creates a PipeWire monitor that polls at the given interval.
func NewMonitor(interval time.Duration, cb CallCallback) *Monitor {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	return &Monitor{
		callback: cb,
		stop:     make(chan struct{}),
		interval: interval,
	}
}

// Start begins the polling loop. Blocks until Stop is called.
func (m *Monitor) Start() {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			m.poll()
		}
	}
}

// Stop ends the polling loop.
func (m *Monitor) Stop() {
	m.stopOnce.Do(func() {
		close(m.stop)
	})
}

// State returns the current call state.
func (m *Monitor) State() CallState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *Monitor) poll() {
	snap, err := TakeSnapshot()
	if err != nil {
		return // pw-dump failed — skip this poll
	}

	newState := DetectCall(snap)

	m.mu.Lock()
	changed := newState.InCall != m.state.InCall
	m.state = newState
	m.mu.Unlock()

	if changed && m.callback != nil {
		m.callback(newState)
		if newState.InCall {
			fmt.Fprintf(os.Stderr, "[pipewire] call started: %s\n", newState.AppName)
		} else {
			fmt.Fprintln(os.Stderr, "[pipewire] call ended")
		}
	}
}

// DetectCall checks a snapshot for active communication streams
// linked to Jabra microphone nodes.
func DetectCall(snap *Snapshot) CallState {
	jabraSources := snap.JabraSourceNodes()
	if len(jabraSources) == 0 {
		return CallState{} // no Jabra mic — no call detection possible
	}

	jabraIDs := make(map[int]bool, len(jabraSources))
	for _, n := range jabraSources {
		jabraIDs[n.ID] = true
	}

	for _, stream := range snap.StreamNodes() {
		if !isCommunicationStream(stream) {
			continue
		}
		// Check if this stream is linked to any Jabra source
		for jabraID := range jabraIDs {
			if snap.LinkedTo(stream.ID, jabraID) {
				return CallState{
					InCall:  true,
					AppName: stream.Props.AppName,
					Since:   time.Now(),
				}
			}
		}
	}

	return CallState{}
}

// isCommunicationStream returns true if the stream looks like a voice/video call.
func isCommunicationStream(n Node) bool {
	// media.role=Communication is the strongest signal
	if strings.EqualFold(n.Props.MediaRole, "communication") {
		return true
	}
	// Known app name match
	app := strings.ToLower(n.Props.AppName)
	if app != "" && commApps[app] {
		return true
	}
	return false
}
