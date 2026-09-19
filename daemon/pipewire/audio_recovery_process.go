package pipewire

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// RecoveryCapture owns the short-lived capture process. Its output is discarded,
// never saved or passed to another audio device.
type RecoveryCapture interface {
	Alive() bool
	Close() error
}

type recoveryCaptureProcess struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *recoveryCaptureProcess) Alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *recoveryCaptureProcess) Close() error {
	p.cancel()
	select {
	case <-p.done:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("temporary microphone process has not stopped")
	}
}

func startRecoveryCapture(parent context.Context, serial, name string) (RecoveryCapture, error) {
	if _, err := strconv.ParseUint(serial, 10, 64); err != nil || serial == "0" {
		return nil, errors.New("microphone identity unavailable")
	}
	if _, err := exec.LookPath("pw-cli"); err != nil {
		return nil, errors.New("audio recovery needs pw-cli from PipeWire")
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	props, _ := json.Marshal(map[string]any{"node.name": name, "application.name": "Jabridge audio recovery", "media.role": "Music", "node.dont-fallback": true, "node.dont-reconnect": true, "node.dont-move": true})
	command := exec.CommandContext(ctx, "pw-cat", "--record", "--raw", "--target", serial, "--channels", "1", "--rate", "48000", "--format", "s16", "--properties", string(props), "-")
	command.Stdout, command.Stderr = io.Discard, io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	command.WaitDelay = time.Second
	if err := command.Start(); err != nil {
		cancel()
		return nil, errors.New("cannot start temporary capture; audio recovery needs pw-cat from PipeWire")
	}
	p := &recoveryCaptureProcess{cancel: cancel, done: make(chan struct{})}
	go func() { _ = command.Wait(); close(p.done) }()
	return p, nil
}

func recoveryNodeCommand(parent context.Context, id int, action string) error {
	if id <= 0 || action != "Suspend" && action != "Start" {
		return errors.New("invalid playback recovery command")
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "pw-cli", "send-command", strconv.Itoa(id), action)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("PipeWire playback restart command failed")
	}
	return nil
}
