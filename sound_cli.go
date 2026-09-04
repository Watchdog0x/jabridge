package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

type soundVolume struct {
	Percent int
	Muted   bool
}

var (
	takeAudioSnapshot   = pipewire.TakeSnapshot
	setDefaultAudioNode = func(nodeID int) error {
		_, err := runWPCTL("set-default", strconv.Itoa(nodeID))
		return err
	}
)

func runSound(args []string) error {
	if len(args) == 0 || (len(args) == 1 && args[0] == "status") {
		return printSoundStatus()
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		printSoundUsage()
		return nil
	}

	switch args[0] {
	case "output":
		if len(args) > 2 {
			return fmt.Errorf("usage: jabridge sound output [NODE_ID]")
		}
		node, err := resolveJabraSink(optionalArg(args, 1))
		if err != nil {
			return err
		}
		if _, err := runWPCTL("set-default", strconv.Itoa(node.ID)); err != nil {
			return err
		}
		fmt.Printf("%s is now the default output.\n", soundNodeName(node))
		return nil
	case "volume":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("usage: jabridge sound volume PERCENT [NODE_ID]")
		}
		percent, err := strconv.Atoi(args[1])
		if err != nil || percent < 0 || percent > 100 {
			return fmt.Errorf("volume must be from 0 to 100")
		}
		node, err := resolveJabraSink(optionalArg(args, 2))
		if err != nil {
			return err
		}
		if _, err := runWPCTL("set-volume", strconv.Itoa(node.ID), fmt.Sprintf("%d%%", percent)); err != nil {
			return err
		}
		fmt.Printf("%s volume set to %d%%.\n", soundNodeName(node), percent)
		return nil
	case "mute":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("usage: jabridge sound mute on|off|toggle [NODE_ID]")
		}
		mode := strings.ToLower(args[1])
		if mode != "on" && mode != "off" && mode != "toggle" {
			return fmt.Errorf("mute value must be on, off, or toggle")
		}
		node, err := resolveJabraSink(optionalArg(args, 2))
		if err != nil {
			return err
		}
		if _, err := runWPCTL("set-mute", strconv.Itoa(node.ID), mode); err != nil {
			return err
		}
		fmt.Printf("%s mute set to %s.\n", soundNodeName(node), mode)
		return nil
	default:
		return fmt.Errorf("unknown sound command %q; run jabridge sound --help", args[0])
	}
}

func printSoundStatus() error {
	snapshot, err := pipewire.TakeSnapshot()
	if err != nil {
		return err
	}
	sinks := snapshot.JabraSinkNodes()
	sources := snapshot.JabraSourceNodes()
	if len(sinks) == 0 && len(sources) == 0 {
		fmt.Println("No Jabra PipeWire audio nodes found.")
		return nil
	}
	for _, sink := range sinks {
		volume, volumeErr := readSoundVolume(sink.ID)
		if volumeErr != nil {
			fmt.Printf("Output %d: %s (volume unavailable)\n", sink.ID, soundNodeName(sink))
			continue
		}
		mute := ""
		if volume.Muted {
			mute = ", muted"
		}
		fmt.Printf("Output %d: %s, %d%%%s\n", sink.ID, soundNodeName(sink), volume.Percent, mute)
	}
	for _, source := range sources {
		fmt.Printf("Microphone %d: %s\n", source.ID, soundNodeName(source))
	}
	return nil
}

func resolveJabraSink(nodeID string) (pipewire.Node, error) {
	snapshot, err := takeAudioSnapshot()
	if err != nil {
		return pipewire.Node{}, err
	}
	sinks := snapshot.JabraSinkNodes()
	if nodeID == "" {
		if len(sinks) == 1 {
			return sinks[0], nil
		}
		if len(sinks) == 0 {
			return pipewire.Node{}, fmt.Errorf("no Jabra PipeWire output found")
		}
		return pipewire.Node{}, fmt.Errorf("more than one Jabra output found; choose a NODE_ID from jabridge sound")
	}
	wanted, err := strconv.Atoi(nodeID)
	if err != nil || wanted < 0 {
		return pipewire.Node{}, fmt.Errorf("invalid PipeWire node ID %q", nodeID)
	}
	for _, sink := range sinks {
		if sink.ID == wanted {
			return sink, nil
		}
	}
	return pipewire.Node{}, fmt.Errorf("PipeWire node %d is not a Jabra output", wanted)
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

func readSoundVolume(nodeID int) (soundVolume, error) {
	output, err := runWPCTL("get-volume", strconv.Itoa(nodeID))
	if err != nil {
		return soundVolume{}, err
	}
	return parseSoundVolume(output)
}

func parseSoundVolume(output string) (soundVolume, error) {
	fields := strings.Fields(output)
	for index, field := range fields {
		if strings.TrimSuffix(field, ":") != "Volume" || index+1 >= len(fields) {
			continue
		}
		value, err := strconv.ParseFloat(fields[index+1], 64)
		if err != nil {
			return soundVolume{}, fmt.Errorf("parse PipeWire volume: %w", err)
		}
		percent := int(value*100 + 0.5)
		if percent < 0 {
			percent = 0
		}
		if percent > 100 {
			percent = 100
		}
		return soundVolume{Percent: percent, Muted: strings.Contains(output, "[MUTED]")}, nil
	}
	return soundVolume{}, fmt.Errorf("PipeWire volume was not found")
}

func runWPCTL(arguments ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "wpctl", arguments...)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return "", fmt.Errorf("wpctl timed out")
	}
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("wpctl %s: %s", arguments[0], detail)
	}
	return string(output), nil
}

func soundNodeName(node pipewire.Node) string {
	for _, value := range []string{node.Props.NodeDescription, node.Props.NodeNick, node.Props.CardName} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "Jabra audio"
}

func optionalArg(arguments []string, index int) string {
	if index < len(arguments) {
		return arguments[index]
	}
	return ""
}

func printSoundUsage() {
	fmt.Println(`Usage:
  jabridge sound
  jabridge sound output [NODE_ID]
  jabridge sound volume PERCENT [NODE_ID]
  jabridge sound mute on|off|toggle [NODE_ID]`)
}
