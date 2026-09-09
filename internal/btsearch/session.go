package btsearch

import (
	"context"
	"errors"
	"sync"
	"time"
)

// IO has a dedicated event reader. Start and Stop are the only radio commands;
// the service serializes them with other management writes.
type IO interface {
	Start(context.Context) error
	Stop(context.Context) error
	Read(context.Context) ([]byte, error)
	Close() error
}

type Snapshot struct {
	State   string   `json:"state"`
	Error   string   `json:"error,omitempty"`
	Devices []Device `json:"-"`
}

type Session struct {
	mu                sync.Mutex
	io                IO
	ctx               context.Context
	cancel            context.CancelFunc
	started, done     chan struct{}
	startErr, stopErr error
	state             string
	result            Results
}

func Start(parent context.Context, transport IO, timeout time.Duration) (*Session, error) {
	if transport == nil || timeout <= 0 {
		return nil, errors.New("invalid discovery session")
	}
	if err := parent.Err(); err != nil {
		_ = transport.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	s := &Session{io: transport, ctx: ctx, cancel: cancel, started: make(chan struct{}), done: make(chan struct{}), state: "starting"}
	go s.run()
	err := transport.Start(ctx)
	s.mu.Lock()
	s.startErr = err
	if err != nil {
		s.state = "failed"
		cancel()
	} else if s.state == "starting" {
		s.state = "searching"
	}
	s.mu.Unlock()
	close(s.started)
	if err != nil {
		<-s.done
		return nil, err
	}
	return s, nil
}

func (s *Session) run() {
	var readErr error
	complete := false
	for s.ctx.Err() == nil {
		packet, err := s.io.Read(s.ctx)
		if err != nil {
			readErr = err
			break
		}
		event, relevant, err := Decode(packet, 1)
		if err != nil {
			readErr = err
			break
		}
		if !relevant {
			continue
		}
		s.mu.Lock()
		err = s.result.Apply(event)
		complete = s.result.Complete
		s.mu.Unlock()
		if err != nil {
			readErr = err
			break
		}
		if complete {
			break
		}
	}
	<-s.started
	var stopErr error
	if !complete {
		stopContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		stopErr = s.io.Stop(stopContext)
		cancel()
	}
	_ = s.io.Close()
	s.mu.Lock()
	s.stopErr = stopErr
	switch {
	case s.startErr != nil:
		s.state = "failed"
	case stopErr != nil:
		s.state = "failed"
	case complete:
		s.state = "complete"
	case errors.Is(s.ctx.Err(), context.Canceled):
		s.state = "stopped"
	case errors.Is(s.ctx.Err(), context.DeadlineExceeded):
		s.state = "timed_out"
	default:
		s.state = "failed"
	}
	if s.state == "failed" && s.startErr == nil && s.stopErr == nil {
		s.stopErr = readErr
	}
	s.mu.Unlock()
	s.cancel()
	close(s.done)
}

func (s *Session) Stop() error {
	s.cancel()
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopErr
}

func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := Snapshot{State: s.state, Devices: append([]Device(nil), s.result.Devices...)}
	if s.startErr != nil {
		state.Error = s.startErr.Error()
	} else if s.stopErr != nil {
		state.Error = s.stopErr.Error()
	}
	return state
}

func (s *Session) Done() <-chan struct{} { return s.done }
