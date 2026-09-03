// busylight — automatic busy light control for Jabra devices.
//
// Toggles the device's busy light based on PipeWire call detection.
// Three modes: auto (follow calls), on (always), off (disabled).

package daemon

import (
	"fmt"
	"os"
	"sync"

	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

// BusylightMode controls how the busy light behaves.
type BusylightMode int

const (
	BusylightAuto BusylightMode = iota // follow call state
	BusylightOn                        // always on
	BusylightOff                       // always off
)

func (m BusylightMode) String() string {
	switch m {
	case BusylightAuto:
		return "auto"
	case BusylightOn:
		return "on"
	case BusylightOff:
		return "off"
	}
	return "unknown"
}

// ParseBusylightMode converts a string to a BusylightMode.
func ParseBusylightMode(s string) (BusylightMode, error) {
	switch s {
	case "auto":
		return BusylightAuto, nil
	case "on":
		return BusylightOn, nil
	case "off":
		return BusylightOff, nil
	}
	return BusylightOff, fmt.Errorf("unknown busylight mode: %q (use auto/on/off)", s)
}

// BusylightSender is the interface for sending busy light commands to hardware.
// Implementations: real GNP sender (jabraApi.go) or mock (tests).
type BusylightSender interface {
	SetBusylight(on bool) error
}

// BusylightController manages busy light state based on call detection.
type BusylightController struct {
	mu      sync.Mutex
	mode    BusylightMode
	lightOn bool // current physical state
	sender  BusylightSender
}

// NewBusylightController creates a controller with the given sender.
func NewBusylightController(sender BusylightSender) *BusylightController {
	return &BusylightController{
		mode:   BusylightAuto,
		sender: sender,
	}
}

// SetMode changes the busy light mode and immediately applies it.
func (c *BusylightController) SetMode(mode BusylightMode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mode = mode

	switch mode {
	case BusylightOn:
		c.setLight(true)
	case BusylightOff:
		c.setLight(false)
	}
	// Auto mode: light state is managed by OnCallStateChange
}

// Mode returns the current mode.
func (c *BusylightController) Mode() BusylightMode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}

// IsOn returns the current physical light state.
func (c *BusylightController) IsOn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lightOn
}

// OnCallStateChange is the callback for the PipeWire monitor.
// It toggles the busy light when in auto mode.
func (c *BusylightController) OnCallStateChange(state pipewire.CallState) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.mode != BusylightAuto {
		return // manual mode — ignore call state
	}

	if state.InCall && !c.lightOn {
		c.setLight(true)
		fmt.Fprintf(os.Stderr, "[busylight] auto ON — call from %s\n", state.AppName)
	} else if !state.InCall && c.lightOn {
		c.setLight(false)
		fmt.Fprintln(os.Stderr, "[busylight] auto OFF — call ended")
	}
}

func (c *BusylightController) setLight(on bool) {
	if c.sender != nil {
		if err := c.sender.SetBusylight(on); err != nil {
			fmt.Fprintf(os.Stderr, "[busylight] GNP error: %v\n", err)
			return
		}
	}
	c.lightOn = on
}
