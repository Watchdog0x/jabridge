package buttons

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/Watchdog0x/jabridge/internal/history"
)

type Event struct {
	Session    string `json:"session"`
	Sequence   uint64 `json:"sequence"`
	Source     string `json:"source"`
	PID        uint16 `json:"pid"`
	Connection string `json:"connection"`
	Edge
}

type ActionResult struct {
	Reason   string `json:"reason"`
	Action   string `json:"action"`
	Session  string `json:"session"`
	Sequence uint64 `json:"sequence"`
	Result   string `json:"result"`
}

type Status struct {
	HistoryOmitted     uint64        `json:"historyOmitted"`
	LastControls       []Event       `json:"lastControls"`
	LastSignals        []SignalEvent `json:"lastSignals"`
	SignalsObserved    uint64        `json:"signalsObserved"`
	SignalsOmitted     uint64        `json:"signalsOmitted"`
	SignalsInvalidated uint64        `json:"signalsInvalidated"`
	Session            string        `json:"session"`
	Mode               string        `json:"mode"`
	Sources            []Source      `json:"sources"`
	Observed           uint64        `json:"observed"`
	Actions            uint64        `json:"actions"`
	Suppressed         uint64        `json:"suppressed"`
	LastAction         *ActionResult `json:"lastAction,omitempty"`
	Error              string        `json:"error,omitempty"`
}

type Config struct {
	Monitor    Monitor
	Publish    func(string, any)
	PlayPause  func(context.Context) error
	Media      func(context.Context, string) error
	AllowMusic func(Source) bool
	Record     func(history.Event)
}

type queuedAction struct {
	event      Event
	source     Source
	at         time.Time
	generation uint64
}
type Controller struct {
	mu            sync.Mutex
	dispatchMu    sync.Mutex
	state         Status
	config        Config
	generation    uint64
	lastMusic     time.Time
	historyWindow time.Time
	historyCount  int
	actions       chan queuedAction
}

func NewController(config Config) *Controller {
	if config.Monitor == nil {
		config.Monitor = NativeMonitor
	}
	if config.Publish == nil {
		config.Publish = func(string, any) {}
	}
	if config.Record == nil {
		config.Record = history.Record
	}
	if config.Media == nil {
		config.Media = ControlMedia
		if config.PlayPause != nil {
			config.Media = func(ctx context.Context, _ string) error { return config.PlayPause(ctx) }
		}
	}
	return &Controller{state: Status{Session: token(), Mode: "off", Sources: []Source{}}, config: config, actions: make(chan queuedAction, 1)}
}

func (c *Controller) State() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.state
	s.LastControls = append([]Event{}, s.LastControls...)
	s.LastSignals = append([]SignalEvent{}, s.LastSignals...)
	s.Sources = append([]Source{}, s.Sources...)
	for i := range s.Sources {
		s.Sources[i].Controls = append([]Control{}, s.Sources[i].Controls...)
	}
	if s.LastAction != nil {
		copy := *s.LastAction
		s.LastAction = &copy
	}
	return s
}

// Mode is deliberately session-local. Restarting the service never silently
// resumes control of a media player. Multiple frontends share this one mode.
func (c *Controller) Configure(mode string) (Status, error) {
	if mode != "off" && mode != "play-pause" {
		return Status{}, errors.New("mode must be off or play-pause")
	}
	c.dispatchMu.Lock()
	c.mu.Lock()
	c.state.Mode = mode
	c.generation++
	c.lastMusic = time.Time{}
	c.mu.Unlock()
	c.dispatchMu.Unlock()
	state := c.State()
	c.config.Publish("buttons.changed", state)
	return state, nil
}

func (c *Controller) Run(ctx context.Context) {
	done := make(chan struct{})
	go func() { defer close(done); c.config.Monitor(ctx, c.sources, c.observe) }()
	defer func() { <-done }()
	for {
		select {
		case <-ctx.Done():
			return
		case action := <-c.actions:
			c.dispatchMu.Lock()
			c.mu.Lock()
			valid := c.state.Mode == "play-pause" && c.generation == action.generation
			present := false
			for _, source := range c.state.Sources {
				present = present || source.ID == action.source.ID && source.Ready
			}
			callSignal := false
			for _, control := range c.state.LastControls {
				if control.Source == action.source.ID && control.Name == "hook-switch" && control.Pressed {
					callSignal = true
				}
			}
			c.mu.Unlock()
			result := "suppressed"
			reason := "audio-or-device-policy"
			if !valid {
				reason = "mode-changed"
			} else if !present {
				reason = "source-disconnected"
			} else if callSignal {
				reason = "call-signal-active"
			} else if time.Since(action.at) >= time.Second {
				reason = "old-event"
			}
			intent := "PlayPause"
			if action.event.Name == "pause" {
				intent = "Pause"
			}
			if action.event.Name == "play" {
				intent = "Play"
			}
			if valid && present && !callSignal && time.Since(action.at) < time.Second && c.config.AllowMusic != nil && c.config.AllowMusic(action.source) {
				callCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
				err := c.config.Media(callCtx, intent)
				cancel()
				result = "sent"
				reason = "command-delivered"
				if err != nil {
					result = "unavailable"
					reason = "media-player-unavailable"
					if errors.Is(err, ErrNoChange) {
						result = "unchanged"
						reason = "already-in-requested-state"
					}
				}
			}
			c.mu.Lock()
			if result == "sent" {
				c.state.Actions++
			} else {
				c.state.Suppressed++
			}
			out := ActionResult{Session: action.event.Session, Sequence: action.event.Sequence, Result: result, Action: intent, Reason: reason}
			c.state.LastAction = &out
			c.mu.Unlock()
			c.dispatchMu.Unlock()
			c.config.Publish("media.action", out)
			phase := "observed"
			failure := ""
			switch result {
			case "sent":
				phase = "ok"
			case "unavailable":
				phase = "error"
				failure = "failed"
			}
			c.config.Record(history.Event{Component: "service", Action: "media", Phase: phase, USBProduct: action.event.PID, Control: action.event.Name, Error: failure})
		}
	}
}

func (c *Controller) sources(sources []Source) {
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	c.mu.Lock()
	c.state.Sources = append([]Source{}, sources...)
	for i := range c.state.Sources {
		c.state.Sources[i].Controls = append([]Control{}, c.state.Sources[i].Controls...)
	}
	retained := c.state.LastControls[:0]
	for _, control := range c.state.LastControls {
		for _, source := range sources {
			if control.Source == source.ID {
				retained = append(retained, control)
				break
			}
		}
	}
	c.state.LastControls = retained
	c.retainSignals(sources)
	c.mu.Unlock()
	c.config.Publish("buttons.changed", c.State())
}

func (c *Controller) observe(observation Observation) {
	if observation.Signal != nil {
		c.observeSignal(observation)
		return
	}
	c.mu.Lock()
	c.state.Observed++
	event := Event{Session: c.state.Session, Sequence: c.state.Observed, Source: observation.Source.ID, PID: observation.Source.PID, Connection: observation.Source.Connection, Edge: observation.Edge}
	found := false
	for i, previous := range c.state.LastControls {
		if previous.Source == event.Source && previous.Name == event.Name && previous.Page == event.Page {
			c.state.LastControls[i] = event
			found = true
			break
		}
	}
	if !found && len(c.state.LastControls) < 128 {
		c.state.LastControls = append(c.state.LastControls, event)
	}
	if observation.At.Sub(c.historyWindow) >= time.Second || c.historyWindow.IsZero() {
		c.historyWindow = observation.At
		c.historyCount = 0
	}
	record := c.historyCount < 64
	c.historyCount++
	if !record {
		c.state.HistoryOmitted++
	}
	eligible := c.state.Mode == "play-pause" && observation.Edge.MusicEligible && observation.Edge.Pressed && !observation.Edge.Initial
	generation := c.generation
	if eligible && (observation.At.Sub(c.lastMusic) < 400*time.Millisecond) {
		eligible = false
		c.state.Suppressed++
	}
	if eligible {
		c.lastMusic = observation.At
	}
	c.mu.Unlock()
	c.config.Publish("device.button", event)
	if record {
		state := "released"
		if event.Pressed {
			state = "pressed"
		}
		if event.Kind == "switch" {
			state = "inactive"
			if event.Pressed {
				state = "active"
			}
		}
		if event.Initial {
			state = "initial-" + state
		}
		c.config.Record(history.Event{Component: "device", Action: "button", Phase: "observed", Operation: event.Sequence, USBProduct: event.PID, Connection: event.Connection, Control: event.Name, ControlState: state, HIDPage: uint16(event.Page), HIDUsage: uint16(event.Usage), HIDReport: event.Report})
	}
	if !eligible {
		return
	}
	select {
	case c.actions <- queuedAction{event: event, source: observation.Source, at: observation.At, generation: generation}:
	default:
		c.mu.Lock()
		c.state.Suppressed++
		c.mu.Unlock()
	}
}
