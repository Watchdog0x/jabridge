package pipewire

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SoundTarget struct {
	ID    int    `json:"id"`
	Token string `json:"token"`
}
type SoundNode struct {
	State          string      `json:"state"`
	DeviceID       int         `json:"audioDeviceId,omitempty"`
	MicrophoneBusy bool        `json:"microphoneBusy"`
	Channels       int         `json:"channels,omitempty"`
	Rate           int         `json:"rate,omitempty"`
	AudioMode      string      `json:"audioMode"`
	Modes          []string    `json:"modes,omitempty"`
	Active         bool        `json:"active"`
	Target         SoundTarget `json:"target"`
	Name           string      `json:"name"`
	Kind           string      `json:"kind"`
	Connection     string      `json:"connection"`
	Default        bool        `json:"default"`
	Volume         *int        `json:"volume,omitempty"`
	Muted          *bool       `json:"muted,omitempty"`
	Editable       bool        `json:"editable"`
	Note           string      `json:"note,omitempty"`
	AboveLimit     bool        `json:"aboveLimit,omitempty"`
}
type SoundState struct {
	Available bool        `json:"available"`
	InCall    bool        `json:"inCall"`
	Error     string      `json:"error,omitempty"`
	Nodes     []SoundNode `json:"nodes"`
	Omitted   int         `json:"omitted,omitempty"`
}
type SoundVolume struct {
	Percent    int
	Muted      bool
	AboveLimit bool
}

type SoundBackend struct {
	Snapshot     func(context.Context) (*Snapshot, error)
	Command      func(context.Context, ...string) (string, error)
	Capture      func(context.Context, string, string) (RecoveryCapture, error)
	NodeCommand  func(context.Context, int, string) error
	RecoveryWait func(context.Context, time.Duration) error
}

// SoundController is owned by the service. It exposes no raw node names,
// device serials, MAC addresses or user-supplied shell arguments.
type SoundController struct {
	opMu    sync.Mutex
	mu      sync.RWMutex
	state   SoundState
	salt    [32]byte
	backend SoundBackend
	changed func(SoundState)
	graph   func(*Snapshot)
}

func NewSoundController(backend SoundBackend, changed func(SoundState), graph func(*Snapshot)) *SoundController {
	if backend.Snapshot == nil {
		backend.Snapshot = TakeSnapshotContext
	}
	if backend.Command == nil {
		backend.Command = SoundCommand
	}
	if backend.Capture == nil {
		backend.Capture = startRecoveryCapture
	}
	if backend.NodeCommand == nil {
		backend.NodeCommand = recoveryNodeCommand
	}
	if backend.RecoveryWait == nil {
		backend.RecoveryWait = waitRecoveryStage
	}
	c := &SoundController{backend: backend, changed: changed, graph: graph, state: SoundState{Error: "Checking PipeWire", Nodes: []SoundNode{}}}
	if _, err := rand.Read(c.salt[:]); err != nil {
		panic("cannot initialize sound target bindings")
	}
	return c
}

func SoundCommand(parent context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "wpctl", args...)
	output, err := command.Output()
	if ctx.Err() != nil {
		return "", errors.New("PipeWire control timed out")
	}
	if err != nil {
		return "", errors.New("PipeWire control failed; check WirePlumber and wpctl")
	}
	return string(output), nil
}

func ParseSoundVolume(output string) (SoundVolume, error) {
	fields := strings.Fields(output)
	if len(fields) < 2 || fields[0] != "Volume:" {
		return SoundVolume{}, errors.New("PipeWire volume unavailable")
	}
	v, err := strconv.ParseFloat(fields[1], 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return SoundVolume{}, errors.New("invalid PipeWire volume")
	}
	return SoundVolume{Percent: int(math.Round(min(v, 1) * 100)), Muted: strings.Contains(output, "[MUTED]"), AboveLimit: v > 1}, nil
}

func SoundConnection(node Node) string {
	if node.Props.DeviceAPI == "bluez5" || node.Props.DeviceBus == "bluetooth" || strings.HasPrefix(node.Props.NodeName, "bluez_") {
		return "bluetooth"
	}
	if node.Props.DeviceBus == "usb" || strings.Contains(node.Props.NodeName, ".usb-") {
		return "usb"
	}
	return "unknown"
}

func soundVendorVerified(node Node) bool {
	v := strings.ToLower(node.Props.VendorID)
	return v == "0x0b0e" || v == "0b0e" || v == "usb:0b0e"
}

func soundCandidate(node Node) bool {
	if node.Props.MediaClass != "Audio/Sink" && node.Props.MediaClass != "Audio/Source" {
		return false
	}
	return soundVendorVerified(node) || isJabraNode(node)
}

// Only a known model label is copied. Custom text and serial-bearing node
// names are not display labels. Unknown names use a generic Jabra label.
var soundModel = regexp.MustCompile(`(?i)\bjabra\s+(?:link\s+(?:360|370|380|390|400)|evolve[23]?\s+(?:20|30|40|50|55|65|75|80|85)(?:\s+(?:se|flex))?|engage\s+(?:40|50|55|65|75)(?:\s+ii)?|speak2?\s*(?:40|50|55|75|410|510|710|750|810))\b`)

func SafeSoundName(node Node) string {
	for _, text := range []string{node.Props.NodeDescription, node.Props.NodeNick, node.Props.CardName, node.Props.DeviceDescription} {
		if label := soundModel.FindString(text); label != "" {
			return label
		}
	}
	return "Jabra audio"
}

func (c *SoundController) token(snap *Snapshot, node Node) string {
	if snap.Cookie == "" || node.Props.ObjectSerial == "" {
		return ""
	}
	h := sha256.New()
	_, _ = h.Write(c.salt[:])
	_, _ = fmt.Fprintf(h, "%s/%d/%s/%d/%s/%s/%s/%s/%s", snap.Cookie, node.ID, node.Props.ObjectSerial, node.Props.DeviceID, node.Props.MediaClass, node.Props.NodeName, node.Props.VendorID, node.Props.ProductID, SoundConnection(node))
	return hex.EncodeToString(h.Sum(nil))
}

func (c *SoundController) describe(snap *Snapshot, node Node) SoundNode {
	kind, defaultName := "output", snap.DefaultSink
	if node.Props.MediaClass == "Audio/Source" {
		kind, defaultName = "microphone", snap.DefaultSource
	}
	value := SoundNode{Target: SoundTarget{ID: node.ID, Token: c.token(snap, node)}, Name: SafeSoundName(node), Kind: kind, Connection: SoundConnection(node), Default: defaultName != "" && node.Props.NodeName == defaultName}
	value.DeviceID = node.Props.DeviceID
	value.MicrophoneBusy = microphoneBusy(snap, node.Props.DeviceID)
	value.Channels, _ = strconv.Atoi(string(node.Props.Channels))
	if value.Channels < 0 || value.Channels > 64 {
		value.Channels = 0
	}
	value.Rate, _ = strconv.Atoi(string(node.Props.Rate))
	if value.Rate < 0 || value.Rate > 768000 {
		value.Rate = 0
	}
	value.Active = strings.EqualFold(node.State, "running")
	value.State = strings.ToLower(node.State)
	if value.State != "running" && value.State != "idle" && value.State != "suspended" {
		value.State = "unknown"
	}
	value.AudioMode, value.Modes = describeAudioMode(snap, node)
	value.Editable = value.Target.Token != "" && soundVendorVerified(node)
	if !soundVendorVerified(node) {
		value.Note = "Name match only; device identity is not verified"
	}
	if value.Target.Token == "" {
		value.Note = "Audio identity unavailable; refresh before editing"
	}
	if value.Connection == "bluetooth" {
		value.Note = strings.TrimSpace(value.Note + " Bluetooth audio only; headset settings and firmware need USB or Jabra Link")
	}
	return value
}

func copySoundState(state SoundState) SoundState {
	state.Nodes = append([]SoundNode{}, state.Nodes...)
	for i := range state.Nodes {
		state.Nodes[i].Modes = append([]string(nil), state.Nodes[i].Modes...)
		if state.Nodes[i].Volume != nil {
			v := *state.Nodes[i].Volume
			state.Nodes[i].Volume = &v
		}
		if state.Nodes[i].Muted != nil {
			v := *state.Nodes[i].Muted
			state.Nodes[i].Muted = &v
		}
	}
	return state
}
func (c *SoundController) State() SoundState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return copySoundState(c.state)
}
func (c *SoundController) store(state SoundState) {
	c.mu.Lock()
	changed := !reflect.DeepEqual(c.state, state)
	c.state = copySoundState(state)
	c.mu.Unlock()
	if changed && c.changed != nil {
		c.changed(copySoundState(state))
	}
}

func (c *SoundController) refreshLocked(ctx context.Context) {
	snap, err := c.backend.Snapshot(ctx)
	if err != nil || snap == nil {
		// Invalidate all tokens after loss of the graph, even if IDs return.
		if _, err := rand.Read(c.salt[:]); err != nil {
			panic("cannot refresh sound bindings")
		}
		c.store(SoundState{Error: "PipeWire unavailable; check PipeWire and pw-dump", Nodes: []SoundNode{}})
		if c.graph != nil {
			c.graph(&Snapshot{})
		}
		return
	}
	state := SoundState{Available: true, InCall: DetectCall(snap).InCall, Nodes: []SoundNode{}}
	var candidates []Node
	for _, node := range snap.Nodes {
		if soundCandidate(node) {
			candidates = append(candidates, node)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	if len(candidates) > 16 {
		state.Omitted = len(candidates) - 16
		candidates = candidates[:16]
	}
	state.Nodes = make([]SoundNode, len(candidates))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i, node := range candidates {
		state.Nodes[i] = c.describe(snap, node)
		wg.Add(1)
		go func(i int, node Node) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			out, err := c.backend.Command(ctx, "get-volume", strconv.Itoa(node.ID))
			if err != nil {
				return
			}
			volume, err := ParseSoundVolume(out)
			if err == nil {
				state.Nodes[i].Volume = &volume.Percent
				state.Nodes[i].Muted = &volume.Muted
				state.Nodes[i].AboveLimit = volume.AboveLimit
			}
		}(i, node)
	}
	wg.Wait()
	c.store(state)
	if c.graph != nil {
		c.graph(snap)
	}
}
func (c *SoundController) Refresh(ctx context.Context) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.refreshLocked(ctx)
}
func (c *SoundController) Run(ctx context.Context) {
	c.Refresh(ctx)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Refresh(ctx)
		}
	}
}

func (c *SoundController) resolve(snap *Snapshot, target SoundTarget) (Node, error) {
	for _, node := range snap.Nodes {
		if node.ID == target.ID && soundCandidate(node) && c.token(snap, node) == target.Token && target.Token != "" {
			if !soundVendorVerified(node) {
				return Node{}, errors.New("audio device identity is not verified; controls are read only")
			}
			return node, nil
		}
	}
	return Node{}, errors.New("audio device changed or disconnected; refresh Sound before trying again")
}

// Fresh identity checks reject stale IDs/restarts. wpctl still resolves its
// numeric ID separately; this is not an atomic native PipeWire transaction.
func (c *SoundController) Change(parent context.Context, target SoundTarget, action string, percent int, mode string) (SoundNode, error) {
	if target.ID <= 0 || len(target.Token) != 64 {
		return SoundNode{}, errors.New("invalid sound target")
	}
	if action != "default" && action != "volume" && action != "mute" {
		return SoundNode{}, errors.New("invalid sound action")
	}
	if action == "volume" && (percent < 0 || percent > 100) {
		return SoundNode{}, errors.New("volume must be from 0 to 100")
	}
	if action == "mute" && mode != "on" && mode != "off" && mode != "toggle" {
		return SoundNode{}, errors.New("mute must be on, off or toggle")
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	snap, err := c.backend.Snapshot(ctx)
	if err != nil || snap == nil {
		return SoundNode{}, errors.New("PipeWire unavailable; refresh Sound")
	}
	node, err := c.resolve(snap, target)
	if err != nil {
		return SoundNode{}, err
	}
	args := []string{"set-default", strconv.Itoa(node.ID)}
	wantedMute := false
	if action == "volume" {
		args = []string{"set-volume", strconv.Itoa(node.ID), fmt.Sprintf("%d%%", percent), "--limit", "1.0"}
	}
	if action == "mute" {
		wantedMute = mode == "on"
		if mode == "toggle" {
			out, err := c.backend.Command(ctx, "get-volume", strconv.Itoa(node.ID))
			if err != nil {
				return SoundNode{}, err
			}
			v, err := ParseSoundVolume(out)
			if err != nil {
				return SoundNode{}, err
			}
			wantedMute = !v.Muted
		}
		value := "0"
		if wantedMute {
			value = "1"
		}
		args = []string{"set-mute", strconv.Itoa(node.ID), value}
	}
	// Recheck after any preceding reads, immediately before the mutation.
	snap, err = c.backend.Snapshot(ctx)
	if err != nil || snap == nil {
		return SoundNode{}, errors.New("PipeWire unavailable before change")
	}
	if _, err = c.resolve(snap, target); err != nil {
		return SoundNode{}, err
	}
	if action != "default" || !c.describe(snap, node).Default {
		if _, err = c.backend.Command(ctx, args...); err != nil {
			return SoundNode{}, err
		}
	}
	var result SoundNode
	for attempt := 0; attempt < 3; attempt++ {
		snap, err = c.backend.Snapshot(ctx)
		if err != nil || snap == nil {
			return SoundNode{}, errors.New("sound command sent, but readback unavailable")
		}
		node, err = c.resolve(snap, target)
		if err != nil {
			return SoundNode{}, err
		}
		result = c.describe(snap, node)
		out, readErr := c.backend.Command(ctx, "get-volume", strconv.Itoa(node.ID))
		if readErr == nil {
			if v, e := ParseSoundVolume(out); e == nil {
				result.Volume = &v.Percent
				result.Muted = &v.Muted
				result.AboveLimit = v.AboveLimit
			}
		}
		matched := action == "default" && result.Default || action == "volume" && result.Volume != nil && *result.Volume == percent && !result.AboveLimit || action == "mute" && result.Muted != nil && *result.Muted == wantedMute
		if matched {
			c.refreshLocked(ctx)
			return result, nil
		}
		select {
		case <-ctx.Done():
			return SoundNode{}, errors.New("sound readback timed out")
		case <-time.After(100 * time.Millisecond):
		}
	}
	return SoundNode{}, errors.New("sound command sent, but readback did not match; refresh Sound")
}
