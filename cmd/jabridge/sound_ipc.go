package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

type soundCommand struct {
	action, kind, mode string
	percent, id        int
}

func parseSoundCommand(args []string) (soundCommand, error) {
	command := soundCommand{action: "list", kind: "output"}
	if len(args) == 0 || len(args) == 1 && args[0] == "status" {
		return command, nil
	}
	if args[0] == "mic" || args[0] == "microphone" {
		command.kind = "microphone"
		args = args[1:]
		if len(args) == 0 {
			return command, nil
		}
		if len(args) == 1 && args[0] == "status" {
			return command, nil
		}
	}
	switch args[0] {
	case "recover":
		if command.kind == "microphone" || len(args) > 2 {
			return command, errors.New("use sound recover [ID]")
		}
		command.action = "recover"
		args = args[1:]
	case "music", "calls":
		if command.kind == "microphone" || len(args) > 2 {
			return command, errors.New("use sound music [ID] or sound calls [ID]")
		}
		command.action, command.mode = "mode", args[0]
		args = args[1:]
	case "output", "input":
		if len(args) > 2 {
			return command, errors.New("use sound output [ID] or sound input [ID]")
		}
		command.action = "default"
		if args[0] == "input" {
			command.kind = "microphone"
		}
		args = args[1:]
	case "volume":
		if len(args) < 2 || len(args) > 3 {
			return command, errors.New("use sound volume PERCENT [ID]")
		}
		value, err := strconv.Atoi(args[1])
		if err != nil || value < 0 || value > 100 {
			return command, errors.New("volume must be from 0 to 100")
		}
		command.action, command.percent = "volume", value
		args = args[2:]
	case "mute":
		if len(args) < 2 || len(args) > 3 {
			return command, errors.New("use sound mute on|off|toggle [ID]")
		}
		mode := strings.ToLower(args[1])
		if mode != "on" && mode != "off" && mode != "toggle" {
			return command, errors.New("mute must be on, off or toggle")
		}
		command.action, command.mode = "mute", mode
		args = args[2:]
	default:
		return command, errors.New("unknown sound command; run jabridge sound --help")
	}
	if len(args) > 0 {
		id, err := strconv.Atoi(args[0])
		if err != nil || id <= 0 {
			return command, errors.New("invalid audio ID; run jabridge sound")
		}
		command.id = id
	}
	return command, nil
}

func runIPCSound(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		printSoundUsage()
		return nil
	}
	command, err := parseSoundCommand(args)
	if err != nil {
		return err
	}
	backend, err := connectTUIService()
	if err != nil {
		return err
	}
	defer backend.close()
	return runSoundClient(backend.clientSnapshot(), command, os.Stdout)
}

func runSoundClient(client *ipc.Client, command soundCommand, out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	var state pipewire.SoundState
	if err := client.Call(ctx, "sound.list", nil, &state); err != nil {
		return err
	}
	deadline := time.Now().Add(3 * time.Second)
	for !state.Available && state.Error == "Checking PipeWire" && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if err := client.Call(ctx, "sound.list", nil, &state); err != nil {
			return err
		}
	}
	if command.action == "list" {
		if command.kind == "microphone" {
			var nodes []pipewire.SoundNode
			for _, node := range state.Nodes {
				if node.Kind == "microphone" {
					nodes = append(nodes, node)
				}
			}
			state.Nodes = nodes
		}
		return printIPCSound(out, state)
	}
	if !state.Available {
		return errors.New(state.Error)
	}
	node, err := selectSoundNode(state.Nodes, command.kind, command.id)
	if err != nil {
		return err
	}
	params := map[string]any{"target": node.Target}
	if command.action == "volume" {
		params["percent"] = command.percent
	}
	if command.action == "mute" {
		params["mode"] = command.mode
	}
	if command.action == "recover" {
		if _, err := fmt.Fprintln(out, "Recovery briefly opens the headset microphone and restarts playback. Captured audio is discarded."); err != nil {
			return err
		}
		var result pipewire.RecoveryResult
		if err := client.Call(ctx, "sound.recover", params, &result); err != nil {
			return err
		}
		if !result.SequenceCompleted || !result.CaptureStopped {
			return errors.New("audio recovery did not finish")
		}
		_, err := fmt.Fprintln(out, "Recovery sequence finished. Temporary capture stopped. Check whether you can hear playback now.")
		return err
	}
	if command.action == "mode" {
		params["mode"] = command.mode
		if command.mode == "music" {
			if _, err := fmt.Fprintln(out, "Music mode disables this device's USB microphone. Use sound calls to restore it."); err != nil {
				return err
			}
		}
		var changed pipewire.SoundState
		if err := client.Call(ctx, "sound.mode", params, &changed); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(out, "Audio profile read back. USB stereo does not prove wireless audio quality; check by listening."); err != nil {
			return err
		}
		return printIPCSound(out, changed)
	}
	var changed pipewire.SoundNode
	if err := client.Call(ctx, "sound."+command.action, params, &changed); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "Sound change read back successfully."); err != nil {
		return err
	}
	return printIPCSound(out, pipewire.SoundState{Available: true, Nodes: []pipewire.SoundNode{changed}})
}

func selectSoundNode(nodes []pipewire.SoundNode, kind string, id int) (pipewire.SoundNode, error) {
	var matches []pipewire.SoundNode
	for _, node := range nodes {
		if node.Kind == kind && (id == 0 || node.Target.ID == id) {
			matches = append(matches, node)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if id == 0 {
		for _, node := range matches {
			if node.Default {
				return node, nil
			}
		}
	}
	return pipewire.SoundNode{}, fmt.Errorf("choose a Jabra %s ID from jabridge sound", kind)
}

func printIPCSound(out io.Writer, state pipewire.SoundState) error {
	var lines []string
	if !state.Available {
		lines = append(lines, "Sound unavailable: "+state.Error)
	} else if len(state.Nodes) == 0 {
		lines = append(lines, "No Jabra audio detected. Connect USB or connect Bluetooth in your desktop settings.")
	} else {
		for _, node := range state.Nodes {
			line := fmt.Sprintf("%s %d: %s (%s)", node.Kind, node.Target.ID, node.Name, node.Connection)
			if node.Default {
				line += ", default"
			}
			if node.Channels > 0 {
				line += fmt.Sprintf(", %d channel(s)", node.Channels)
			}
			if node.AudioMode != "" && node.AudioMode != "unknown" {
				line += ", " + node.AudioMode + " mode"
			}
			if node.AboveLimit {
				line += ", system gain exceeds 100%"
			}
			if node.Volume != nil {
				line += fmt.Sprintf(", %d%%", *node.Volume)
			}
			if node.Muted != nil && *node.Muted {
				line += ", muted"
			}
			if !node.Editable {
				line += ", read only"
			}
			lines = append(lines, line)
			if node.Note != "" {
				lines = append(lines, "  "+node.Note)
			}
		}
	}
	_, err := io.WriteString(out, strings.Join(lines, "\n")+"\n")
	return err
}
