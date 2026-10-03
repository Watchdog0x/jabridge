package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

var (
	serviceSoundController atomic.Pointer[pipewire.SoundController]
	takeAudioSnapshot      = pipewire.TakeSnapshot
	setDefaultAudioNode    = func(nodeID int) error {
		controller := serviceSoundController.Load()
		if controller == nil {
			return errors.New("sound control requires the running Jabridge service")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		controller.Refresh(ctx)
		for _, node := range controller.State().Nodes {
			if node.Target.ID == nodeID {
				_, err := controller.Change(ctx, node.Target, "default", 0, "")
				return err
			}
		}
		return errors.New("matching Jabra audio device is unavailable")
	}
)

func runSound(args []string) error {
	return runIPCSound(args)
}

func followSelectedDeviceAudio(targetName string) error {
	if strings.TrimSpace(targetName) == "" {
		return nil
	}
	snapshot, err := takeAudioSnapshot()
	if err != nil {
		return nil // Device control still works on systems without PipeWire tools.
	}
	selected := make([]pipewire.Node, 0, 2)
	if sink, found := bestMatchingAudioNode(snapshot.JabraSinkNodes(), targetName); found {
		selected = append(selected, sink)
	}
	if source, found := bestMatchingAudioNode(snapshot.JabraSourceNodes(), targetName); found {
		selected = append(selected, source)
	}
	for _, node := range selected {
		if err := setDefaultAudioNode(node.ID); err != nil {
			return fmt.Errorf("set PipeWire default for %s: %w", soundNodeName(node), err)
		}
	}
	return nil
}

func bestMatchingAudioNode(nodes []pipewire.Node, targetName string) (pipewire.Node, bool) {
	tokens := audioMatchTokens(targetName)
	if len(tokens) == 0 {
		return pipewire.Node{}, false
	}
	bestIndex, bestScore := -1, -1
	for index, node := range nodes {
		candidate := normalizeAudioName(strings.Join([]string{
			node.Props.NodeName, node.Props.NodeDescription, node.Props.NodeNick, node.Props.CardName,
		}, " "))
		score := 0
		matched := true
		for _, token := range tokens {
			if !strings.Contains(candidate, token) {
				matched = false
				break
			}
			score += len(token)
		}
		if !matched {
			continue
		}
		if strings.EqualFold(node.State, "running") {
			score += 2
		} else if strings.EqualFold(node.State, "idle") {
			score++
		}
		if score > bestScore || (score == bestScore && bestIndex >= 0 && node.ID < nodes[bestIndex].ID) {
			bestIndex, bestScore = index, score
		}
	}
	if bestIndex < 0 {
		return pipewire.Node{}, false
	}
	return nodes[bestIndex], true
}

func audioMatchTokens(name string) []string {
	ignored := map[string]bool{
		"jabra": true, "usb": true, "headset": true, "stereo": true, "mono": true,
		"analog": true, "audio": true, "device": true,
	}
	fields := strings.Fields(normalizeAudioName(name))
	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		if !ignored[field] && len(field) > 1 {
			tokens = append(tokens, field)
		}
	}
	return tokens
}

func normalizeAudioName(value string) string {
	var builder strings.Builder
	space := true
	for _, character := range strings.ToLower(value) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			builder.WriteRune(character)
			space = false
		} else if !space {
			builder.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func parseSoundVolume(output string) (pipewire.SoundVolume, error) {
	return pipewire.ParseSoundVolume(output)
}

func soundNodeName(node pipewire.Node) string { return pipewire.SafeSoundName(node) }

func printSoundUsage() {
	fmt.Println(`Usage:
  jabridge sound
  jabridge sound output [NODE_ID]
  jabridge sound input [NODE_ID]
  jabridge sound volume PERCENT [NODE_ID]
  jabridge sound mute on|off|toggle [NODE_ID]
  jabridge sound mic volume PERCENT [NODE_ID]
  jabridge sound mic mute on|off|toggle [NODE_ID]
  jabridge sound music [NODE_ID]   USB music profile (headset mic off)
  jabridge sound calls [NODE_ID]   restore USB microphone availability
  jabridge sound recover [NODE_ID] recover silent Evolve2 30 SE USB playback

Recovery is experimental. Start playback first and stop calls or recordings.
It briefly opens the headset microphone, discards capture and restarts playback.
It does not change volume, mute, the audio profile or default device.

The service handles sound. Direct Bluetooth audio can appear here, but
Bluetooth headset settings and firmware are not supported.`)
}
