package pipewire

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Watchdog0x/jabridge/internal/history"
)

type RecoveryResult struct {
	SequenceCompleted bool `json:"sequenceCompleted"`
	CaptureStopped    bool `json:"captureStopped"`
}

func waitRecoveryStage(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func recoveryModel(node Node) bool {
	if SoundConnection(node) != "usb" || !soundVendorVerified(node) || node.Props.DeviceAPI != "alsa" || node.Props.MediaClass != "Audio/Sink" {
		return false
	}
	pid := strings.TrimPrefix(strings.ToLower(node.Props.ProductID), "0x")
	value, err := strconv.ParseUint(pid, 16, 16)
	return err == nil && value >= 0x0e36 && value <= 0x0e39
}

func recoveryMicrophone(snap *Snapshot, output Node) (Node, error) {
	var mic Node
	count := 0
	for _, candidate := range snap.Nodes {
		if candidate.Props.DeviceID == output.Props.DeviceID && candidate.Props.MediaClass == "Audio/Source" {
			count++
			mic = candidate
		}
	}
	if output.Props.DeviceID <= 0 || count != 1 || !soundVendorVerified(mic) || mic.Props.ProductID != output.Props.ProductID || SoundConnection(mic) != "usb" {
		return Node{}, errors.New("a matching headset microphone is required; use a profile with both playback and capture")
	}
	if _, err := strconv.ParseUint(string(mic.Props.ObjectSerial), 10, 64); err != nil || mic.Props.ObjectSerial == "0" {
		return Node{}, errors.New("microphone identity unavailable")
	}
	return mic, nil
}

func recoveryCaptureActive(snap *Snapshot, mic Node, name string) (bool, error) {
	streamID := 0
	streamRunning := false
	for _, node := range snap.Nodes {
		if node.Props.NodeName == name && node.Props.MediaClass == "Stream/Input/Audio" {
			streamID = node.ID
			streamRunning = node.State == "running"
		}
	}
	active := false
	for _, link := range snap.Links {
		if link.OutputNodeID == mic.ID {
			if link.InputNodeID != streamID {
				return false, errors.New("headset microphone is in use; stop the call or recording first")
			}
			active = active || link.State == "active"
		}
	}
	return streamID > 0 && streamRunning && mic.State == "running" && active, nil
}

// RecoverPlayback follows the sequence reported in issue 44. It does not change
// profiles, defaults, mute or volume, and successful graph commands are not proof
// of audible recovery. This is only exposed as an explicit user command.
func (c *SoundController) RecoverPlayback(parent context.Context, target SoundTarget) (result RecoveryResult, resultErr error) {
	if target.ID <= 0 || len(target.Token) != 64 {
		return result, errors.New("invalid sound target")
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	snap, err := c.backend.Snapshot(ctx)
	if err != nil || snap == nil {
		return result, errors.New("PipeWire unavailable")
	}
	output, err := c.resolve(snap, target)
	if err != nil {
		return result, err
	}
	if !recoveryModel(output) {
		return result, errors.New("audio recovery currently supports Evolve2 30 SE over direct USB only")
	}
	if output.State != "running" {
		return result, errors.New("start audio playback on this headset before running recovery")
	}
	mic, err := recoveryMicrophone(snap, output)
	if err != nil {
		return result, err
	}
	if DetectCall(snap).InCall || microphoneBusy(snap, output.Props.DeviceID) {
		return result, errors.New("stop the call or recording before recovering audio")
	}
	if _, err := recoveryCaptureActive(snap, mic, ""); err != nil {
		return result, err
	}
	micTarget := SoundTarget{ID: mic.ID, Token: c.token(snap, mic)}
	device, ok := snap.Devices[output.Props.DeviceID]
	if !ok || !device.Known || audioDeviceBinding(snap, device) == "" {
		return result, errors.New("audio-card identity and profile are required for recovery")
	}
	binding := audioDeviceBinding(snap, device)
	check := func(checkCtx context.Context) (*Snapshot, Node, error) {
		if err := checkCtx.Err(); err != nil {
			return nil, Node{}, err
		}
		next, e := c.backend.Snapshot(checkCtx)
		if e != nil || next == nil {
			return nil, Node{}, errors.New("audio graph became unavailable during recovery")
		}
		if _, e = c.resolve(next, target); e != nil {
			return nil, Node{}, e
		}
		m, e := c.resolve(next, micTarget)
		if e != nil {
			return nil, Node{}, e
		}
		current := next.Devices[output.Props.DeviceID]
		if !current.Known || audioDeviceBinding(next, current) != binding || current.Profile.Name != device.Profile.Name || current.Profile.Index != device.Profile.Index {
			return nil, Node{}, errors.New("audio card or profile changed during recovery")
		}
		return next, m, nil
	}
	if _, _, err := check(ctx); err != nil {
		return result, err
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return result, errors.New("cannot create a temporary capture identity")
	}
	name := "jabridge-recovery-" + hex.EncodeToString(random[:])
	finish := history.Begin(history.Event{Component: "service", Action: "audio-recovery", Connection: "usb"})
	defer history.EndDeferred(finish, &resultErr)
	capture, err := c.backend.Capture(ctx, string(mic.Props.ObjectSerial), name)
	if err != nil {
		return result, err
	}
	if capture == nil {
		return result, errors.New("temporary capture did not start")
	}
	suspended := false
	defer func() {
		// Attempt to resume the original playback node before closing capture.
		// Never target a replacement node or changed audio profile.
		if suspended {
			cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
			if _, _, e := check(cleanup); e == nil {
				resultErr = errors.Join(resultErr, c.backend.NodeCommand(cleanup, output.ID, "Start"))
			}
			stop()
		}
		closeErr := capture.Close()
		result.CaptureStopped = closeErr == nil
		resultErr = errors.Join(resultErr, closeErr)
	}()
	ready := false
	for attempt := 0; attempt < 30; attempt++ {
		if !capture.Alive() {
			return result, errors.New("temporary capture stopped before recovery")
		}
		next, m, e := check(ctx)
		if e != nil {
			return result, e
		}
		if DetectCall(next).InCall {
			return result, errors.New("a call started; audio recovery stopped")
		}
		ready, e = recoveryCaptureActive(next, m, name)
		if e != nil {
			return result, e
		}
		if ready {
			break
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !ready {
		return result, errors.New("temporary capture did not become active; playback was not restarted")
	}
	// Preserve the minimum holds from the reporter's successful script.
	// A running PipeWire graph alone does not mean the USB device has settled.
	holdCapture := func(duration time.Duration) error {
		if err := c.backend.RecoveryWait(ctx, duration); err != nil {
			return err
		}
		next, m, err := check(ctx)
		if err != nil {
			return err
		}
		active, err := recoveryCaptureActive(next, m, name)
		if err != nil {
			return err
		}
		if !active || !capture.Alive() || DetectCall(next).InCall {
			return errors.New("capture or call state changed while recovery was settling")
		}
		return nil
	}
	if err := holdCapture(2 * time.Second); err != nil {
		return result, err
	}
	for _, action := range []string{"Suspend", "Start"} {
		next, m, e := check(ctx)
		if e != nil {
			return result, e
		}
		active, e := recoveryCaptureActive(next, m, name)
		if e != nil {
			return result, e
		}
		if !active || !capture.Alive() || DetectCall(next).InCall {
			return result, errors.New("capture or call state changed during recovery")
		}
		// Set before sending: a failed command may still have reached PipeWire.
		if action == "Suspend" {
			suspended = true
		}
		if e := c.backend.NodeCommand(ctx, output.ID, action); e != nil {
			return result, fmt.Errorf("playback %s failed: %w", strings.ToLower(action), e)
		}
		if action == "Start" {
			suspended = false
		}
		pause := 500 * time.Millisecond
		if action == "Start" {
			pause = 2 * time.Second
		}
		if err := holdCapture(pause); err != nil {
			return result, err
		}
	}
	for attempt := 0; attempt < 20; attempt++ {
		next, _, e := check(ctx)
		if e != nil {
			return result, e
		}
		node, e := c.resolve(next, target)
		if e != nil {
			return result, e
		}
		if node.State == "running" && capture.Alive() {
			result.SequenceCompleted = true
			return result, nil
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return result, errors.New("playback restart was sent, but running state was not confirmed")
}
