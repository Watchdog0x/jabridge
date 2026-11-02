package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
)

var (
	errControlBusy     = errors.New("the dongle is busy changing settings")
	errSearchActive    = errors.New("stop the headset search before changing these settings")
	errControlClosed   = errors.New("the device operation is no longer active")
	errLink390Identity = errors.New("the selected dongle cannot be identified uniquely")
	errLink390Changed  = errors.New("the USB attachment changed")
)

// This gate owns a physical USB port, including the interval when a device is
// absent during its expected restart. Packet locks are always taken after it.
type controlGate struct {
	mu      sync.Mutex
	ports   map[string]*controlPort
	aliases map[string]string
	unbound int
}

type controlPort struct {
	readers, searches int
	waiter, owner     *controlPermit
	changed           chan struct{}
}

type controlPermit struct {
	gate      *controlGate
	port      string
	state     *controlPort
	exclusive bool
	search    bool
	active    bool
	alias     string
	unbound   bool
	once      sync.Once
}

var link390ControlGate controlGate

func scopedControlPID(pid uint16) bool { return pid == 0x2e56 || pid == 0x2e57 }

func (g *controlGate) active() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, port := range g.ports {
		if port.waiter != nil || port.owner != nil {
			return true
		}
	}
	return false
}

func (g *controlGate) portLocked(port string) *controlPort {
	if g.ports == nil {
		g.ports = make(map[string]*controlPort)
		g.aliases = make(map[string]string)
	}
	state := g.ports[port]
	if state == nil {
		state = &controlPort{changed: make(chan struct{})}
		g.ports[port] = state
	}
	return state
}

func (g *controlGate) notifyLocked(state *controlPort) {
	close(state.changed)
	state.changed = make(chan struct{})
}

func (g *controlGate) pruneLocked(port string, state *controlPort) {
	if state.readers == 0 && state.owner == nil && state.waiter == nil {
		delete(g.ports, port)
	}
}

func (g *controlGate) shared(port string, search bool) (*controlPermit, error) {
	if !filepath.IsAbs(port) || filepath.Clean(port) == "/" {
		return nil, errors.New("cannot identify the dongle's physical USB port")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.portLocked(port)
	if state.owner != nil || state.waiter != nil {
		return nil, errControlBusy
	}
	permit := &controlPermit{gate: g, port: port, state: state, search: search, active: true}
	state.readers++
	if search {
		state.searches++
	}
	return permit, nil
}

// An unresolved exchange is still drained before ownership starts. Otherwise
// an owner could start between an idle check and the packet mutex acquisition.
func (g *controlGate) sharedUnbound() (*controlPermit, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, port := range g.ports {
		if port.owner != nil || port.waiter != nil {
			return nil, errControlBusy
		}
	}
	g.unbound++
	return &controlPermit{gate: g, active: true, unbound: true}, nil
}

func (g *controlGate) exclusive(ctx context.Context, port, alias string) (*controlPermit, error) {
	if !filepath.IsAbs(port) || filepath.Clean(port) == "/" {
		return nil, errors.New("cannot identify the dongle's physical USB port")
	}
	g.mu.Lock()
	state := g.portLocked(port)
	if state.searches != 0 {
		g.mu.Unlock()
		return nil, errSearchActive
	}
	if state.owner != nil || state.waiter != nil {
		g.mu.Unlock()
		return nil, errControlBusy
	}
	permit := &controlPermit{gate: g, port: port, state: state, exclusive: true, active: true, alias: alias}
	state.waiter = permit
	if alias != "" {
		g.aliases[filepath.Clean(alias)] = port
	}
	for state.readers != 0 || g.unbound != 0 {
		changed := state.changed
		g.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			g.mu.Lock()
			state.waiter = nil
			permit.active = false
			if alias != "" {
				delete(g.aliases, filepath.Clean(alias))
			}
			g.notifyLocked(state)
			g.pruneLocked(port, state)
			g.mu.Unlock()
			return nil, ctx.Err()
		}
		g.mu.Lock()
	}
	if err := ctx.Err(); err != nil {
		state.waiter = nil
		permit.active = false
		if alias != "" {
			delete(g.aliases, filepath.Clean(alias))
		}
		g.notifyLocked(state)
		g.pruneLocked(port, state)
		g.mu.Unlock()
		return nil, err
	}
	state.waiter, state.owner = nil, permit
	g.mu.Unlock()
	return permit, nil
}

func (p *controlPermit) valid() bool {
	if p == nil {
		return false
	}
	p.gate.mu.Lock()
	defer p.gate.mu.Unlock()
	return p.active && (p.unbound || p.gate.ports[p.port] == p.state && (!p.exclusive || p.state.owner == p))
}

func (p *controlPermit) release() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		g := p.gate
		g.mu.Lock()
		defer g.mu.Unlock()
		p.active = false
		if p.unbound {
			g.unbound--
			for _, state := range g.ports {
				g.notifyLocked(state)
			}
			return
		}
		if p.exclusive {
			p.state.owner = nil
			if p.alias != "" {
				delete(g.aliases, filepath.Clean(p.alias))
			}
		} else {
			p.state.readers--
			if p.search {
				p.state.searches--
			}
		}
		g.notifyLocked(p.state)
		g.pruneLocked(p.port, p.state)
	})
}

func (g *controlGate) busy(path string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	path = filepath.Clean(path)
	if port, exists := g.aliases[path]; exists {
		path = port
	}
	state := g.ports[path]
	return state != nil && (state.owner != nil || state.waiter != nil)
}
