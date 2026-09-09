package buttons

import "github.com/Watchdog0x/jabridge/internal/gnpevents"

// SignalEvent is an observed vendor event, separate from actionable HID edges.
// No vendor event is eligible to trigger desktop media or alter device state.
type SignalEvent struct {
	Session    string `json:"session"`
	Sequence   uint64 `json:"sequence"`
	Source     string `json:"source"`
	PID        uint16 `json:"pid"`
	Connection string `json:"connection"`
	gnpevents.Signal
}

// InvalidateSignalsForProducts drops cached vendor observations after the
// registry observes a device/child identity change. It does not change HID
// button baselines, media policy, subscriptions, or hardware state.
func (c *Controller) InvalidateSignalsForProducts(products []uint16) {
	if len(products) == 0 {
		return
	}
	c.mu.Lock()
	before := len(c.state.LastSignals)
	retained := c.state.LastSignals[:0]
	for _, signal := range c.state.LastSignals {
		remove := false
		for _, pid := range products {
			if signal.PID == pid {
				remove = true
				break
			}
		}
		if !remove {
			retained = append(retained, signal)
		}
	}
	c.state.LastSignals = retained
	changed := len(retained) != before
	if changed {
		c.state.SignalsInvalidated += uint64(before - len(retained))
	}
	c.mu.Unlock()
	if changed {
		c.config.Publish("buttons.changed", c.State())
	}
}

func (c *Controller) observeSignal(observation Observation) {
	c.mu.Lock()
	present := false
	for _, source := range c.state.Sources {
		if source.Ready && source.ID == observation.Source.ID && source.PID == observation.Source.PID && source.Connection == observation.Source.Connection {
			present = true
			break
		}
	}
	if !present {
		c.mu.Unlock()
		return
	}
	c.state.SignalsObserved++
	event := SignalEvent{Session: c.state.Session, Sequence: c.state.SignalsObserved, Source: observation.Source.ID, PID: observation.Source.PID, Connection: observation.Source.Connection, Signal: *observation.Signal}
	for i, previous := range c.state.LastSignals {
		if previous.Source != event.Source || previous.Endpoint != event.Endpoint || previous.Kind != event.Kind || previous.Name != event.Name || previous.Code != event.Code {
			continue
		}
		if event.Kind == "state" && previous.Signal == event.Signal {
			c.mu.Unlock()
			return
		}
		c.state.LastSignals = append(c.state.LastSignals[:i], c.state.LastSignals[i+1:]...)
		break
	}
	if len(c.state.LastSignals) >= 128 {
		c.state.LastSignals = c.state.LastSignals[1:]
		c.state.SignalsOmitted++
	}
	c.state.LastSignals = append(c.state.LastSignals, event)
	c.mu.Unlock()
	c.config.Publish("device.signal", event)
}

// Called with c.mu held. Disconnect/not-ready invalidates sensor state; a
// cached observation must never appear to describe a replacement device.
func (c *Controller) retainSignals(sources []Source) {
	retained := c.state.LastSignals[:0]
	for _, signal := range c.state.LastSignals {
		for _, source := range sources {
			if source.Ready && source.ID == signal.Source && source.PID == signal.PID && source.Connection == signal.Connection {
				retained = append(retained, signal)
				break
			}
		}
	}
	c.state.LastSignals = retained
}
