package pipewire

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type AudioProfile struct {
	Index     int    `json:"index"`
	Name      string `json:"name"`
	Priority  int    `json:"priority"`
	Available string `json:"available"`
}
type AudioDevice struct {
	ID       int
	Props    NodeProps
	Profiles []AudioProfile
	Profile  AudioProfile
	Known    bool
}

func audioModeName(name string) string {
	switch name {
	case "output:analog-stereo", "output:iec958-stereo":
		return "music"
	case "output:analog-stereo+input:mono-fallback", "output:iec958-stereo+input:mono-fallback":
		return "calls"
	}
	return "unknown"
}

func selectAudioProfile(device AudioDevice, mode string) (AudioProfile, error) {
	if mode != "music" && mode != "calls" {
		return AudioProfile{}, errors.New("audio mode must be music or calls")
	}
	var best AudioProfile
	found := false
	bestFamily := false
	for _, profile := range device.Profiles {
		if audioModeName(profile.Name) != mode || profile.Index < 0 || profile.Index > 65535 || profile.Available == "no" {
			continue
		}
		family := strings.Contains(profile.Name, "iec958") == strings.Contains(device.Profile.Name, "iec958")
		if !found || family && !bestFamily || family == bestFamily && profile.Priority > best.Priority {
			best = profile
			found = true
			bestFamily = family
		}
	}
	if !found {
		return AudioProfile{}, errors.New("this USB device does not advertise that audio mode")
	}
	return best, nil
}

func audioDeviceBinding(snapshot *Snapshot, device AudioDevice) string {
	if snapshot.Cookie == "" || device.Props.ObjectSerial == "" {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%s/%s/%s/%s", snapshot.Cookie, device.ID, device.Props.ObjectSerial, device.Props.NodeName, device.Props.VendorID, device.Props.ProductID))))
}

func microphoneBusy(snapshot *Snapshot, deviceID int) bool {
	for _, node := range snapshot.Nodes {
		if node.Props.DeviceID == deviceID && node.Props.MediaClass == "Audio/Source" {
			switch strings.ToLower(node.State) {
			case "idle", "suspended":
			default:
				return true
			}
		}
	}
	return false
}

// ChangeMode switches an advertised USB sound-card profile. It does not send
// Bluetooth or firmware commands. Music removes the headset microphone from
// PipeWire; Calls makes it available again. Physical audio needs user testing.
func (c *SoundController) ChangeMode(ctx context.Context, target SoundTarget, mode string) (SoundState, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	c.opMu.Lock()
	defer c.opMu.Unlock()
	snapshot, err := c.backend.Snapshot(ctx)
	if err != nil || snapshot == nil {
		return SoundState{}, errors.New("PipeWire unavailable before audio mode change")
	}
	node, err := c.resolve(snapshot, target)
	if err != nil {
		return SoundState{}, err
	}
	if SoundConnection(node) != "usb" {
		return SoundState{}, errors.New("this recovery is for USB and Jabra Link audio; direct Bluetooth uses different profiles")
	}
	device, ok := snapshot.Devices[node.Props.DeviceID]
	if !ok || !device.Known {
		return SoundState{}, errors.New("audio-card profiles unavailable; run debug")
	}
	profile, err := selectAudioProfile(device, mode)
	if err != nil {
		return SoundState{}, err
	}
	binding := audioDeviceBinding(snapshot, device)
	if binding == "" {
		return SoundState{}, errors.New("audio-card identity unavailable; cannot switch safely")
	}
	check, err := c.backend.Snapshot(ctx)
	if err != nil || check == nil {
		return SoundState{}, errors.New("audio graph unavailable before profile change")
	}
	if _, err := c.resolve(check, target); err != nil {
		return SoundState{}, err
	}
	if audioDeviceBinding(check, check.Devices[device.ID]) != binding {
		return SoundState{}, errors.New("audio device changed before profile switch")
	}
	confirmed, err := selectAudioProfile(check.Devices[device.ID], mode)
	if err != nil || confirmed.Name != profile.Name || confirmed.Index != profile.Index {
		return SoundState{}, errors.New("audio profiles changed; reopen Sound")
	}
	if check.Devices[device.ID].Profile.Name != profile.Name {
		if _, err := c.backend.Command(ctx, "set-profile", strconv.Itoa(device.ID), strconv.Itoa(profile.Index)); err != nil {
			return SoundState{}, err
		}
	}
	for attempt := 0; attempt < 25; attempt++ {
		check, err = c.backend.Snapshot(ctx)
		if err == nil && check != nil {
			current, exists := check.Devices[device.ID]
			if !exists || audioDeviceBinding(check, current) != binding {
				return SoundState{}, errors.New("audio device disappeared during profile switch; reopen Sound")
			}
			if current.Known && current.Profile.Name == profile.Name && current.Profile.Index == profile.Index {
				c.refreshLocked(ctx)
				state := c.State()
				if !state.Available {
					return SoundState{}, errors.New("profile changed but audio graph became unavailable")
				}
				outputReady, microphoneReady := false, false
				for _, node := range state.Nodes {
					if node.DeviceID == device.ID {
						outputReady = outputReady || node.Kind == "output" && node.AudioMode == mode
						microphoneReady = microphoneReady || node.Kind == "microphone"
					}
				}
				if outputReady && (mode == "music" && !microphoneReady || mode == "calls" && microphoneReady) {
					return state, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return SoundState{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return SoundState{}, errors.New("profile command sent but readback did not match; an audio app or policy may be switching it back")
}

func describeAudioMode(snapshot *Snapshot, node Node) (mode string, choices []string) {
	mode = "unknown"
	device, ok := snapshot.Devices[node.Props.DeviceID]
	if !ok || !device.Known || SoundConnection(node) != "usb" {
		return
	}
	mode = audioModeName(device.Profile.Name)
	for _, wanted := range []string{"music", "calls"} {
		if _, err := selectAudioProfile(device, wanted); err == nil {
			choices = append(choices, wanted)
		}
	}
	return
}
