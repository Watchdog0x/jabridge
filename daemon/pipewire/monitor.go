// pipewire/monitor — PipeWire audio graph monitor for Jabridge.
//
// Polls `pw-dump` to snapshot the audio graph and detect when
// communication streams are linked to Jabra microphone nodes.
// Pure Go — no libpipewire, no cgo.

package pipewire

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Node is a PipeWire graph node (device or stream).
type Node struct {
	ID    int
	Type  string // "PipeWire:Interface:Node"
	State string // "suspended", "idle", "running"
	Props NodeProps
}

type NodeProps struct {
	Channels          json.Number `json:"audio.channels"`
	Rate              json.Number `json:"audio.rate"`
	ProfileName       string      `json:"device.profile.name"`
	MediaClass        string      `json:"media.class"`
	AppName           string      `json:"application.name"`
	MediaRole         string      `json:"media.role"`
	NodeName          string      `json:"node.name"`
	NodeDescription   string      `json:"node.description"`
	NodeNick          string      `json:"node.nick"`
	CardName          string      `json:"alsa.card_name"`
	DeviceID          int         `json:"device.id"`
	ObjectSerial      json.Number `json:"object.serial"`
	DeviceAPI         string      `json:"device.api"`
	DeviceBus         string      `json:"device.bus"`
	VendorID          string      `json:"device.vendor.id"`
	ProductID         string      `json:"device.product.id"`
	DeviceDescription string      `json:"device.description"`
}

// Link is a PipeWire graph link connecting two nodes.
type Link struct {
	ID           int
	OutputNodeID int
	InputNodeID  int
	State        string
}

// Snapshot is a point-in-time view of the PipeWire graph.
type Snapshot struct {
	Devices       map[int]AudioDevice
	Nodes         []Node
	Links         []Link
	Time          time.Time
	Cookie        json.Number
	DefaultSink   string // private PipeWire name, never exposed over sound IPC
	DefaultSource string
}

// pwDumpObject is the raw JSON structure from pw-dump.
type pwDumpObject struct {
	ID       int                        `json:"id"`
	Type     string                     `json:"type"`
	Props    map[string]json.RawMessage `json:"props"`
	Metadata []struct {
		Subject int             `json:"subject"`
		Key     string          `json:"key"`
		Value   json.RawMessage `json:"value"`
	} `json:"metadata"`
	Info struct {
		Params struct {
			Profile     []AudioProfile `json:"Profile"`
			EnumProfile []AudioProfile `json:"EnumProfile"`
		} `json:"params"`
		Cookie       json.Number     `json:"cookie"`
		State        string          `json:"state"`
		OutputNodeID int             `json:"output-node-id"`
		InputNodeID  int             `json:"input-node-id"`
		Props        json.RawMessage `json:"props"`
	} `json:"info"`
}

// TakeSnapshot runs pw-dump and parses the output into a Snapshot.
func TakeSnapshot() (*Snapshot, error) {
	return TakeSnapshotContext(context.Background())
}

func TakeSnapshotContext(parent context.Context) (*Snapshot, error) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pw-dump")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("PipeWire unavailable; check PipeWire and pw-dump")
	}
	return ParseSnapshot(out)
}

// ParseSnapshot parses raw pw-dump JSON into a Snapshot.
// Exported for testing with fixture data.
func ParseSnapshot(data []byte) (*Snapshot, error) {
	var objects []pwDumpObject
	if err := json.Unmarshal(data, &objects); err != nil {
		return nil, fmt.Errorf("parse pw-dump: %w", err)
	}

	snap := &Snapshot{Time: time.Now(), Devices: map[int]AudioDevice{}}
	devices := map[int]NodeProps{}
	for _, obj := range objects {
		if obj.Type == "PipeWire:Interface:Device" {
			var props NodeProps
			if err := json.Unmarshal(obj.Info.Props, &props); err == nil {
				devices[obj.ID] = props
				device := AudioDevice{ID: obj.ID, Props: props, Profiles: obj.Info.Params.EnumProfile}
				if len(obj.Info.Params.Profile) == 1 {
					device.Profile = obj.Info.Params.Profile[0]
					device.Known = true
				}
				snap.Devices[obj.ID] = device
			}
		}
		if obj.Type == "PipeWire:Interface:Core" {
			snap.Cookie = obj.Info.Cookie
		}
		if obj.Type == "PipeWire:Interface:Metadata" {
			var name string
			_ = json.Unmarshal(obj.Props["metadata.name"], &name)
			if name != "default" {
				continue
			}
			for _, entry := range obj.Metadata {
				if entry.Subject != 0 {
					continue
				}
				var value struct {
					Name string `json:"name"`
				}
				raw := entry.Value
				var encoded string
				if json.Unmarshal(raw, &encoded) == nil {
					raw = []byte(encoded)
				}
				if json.Unmarshal(raw, &value) != nil {
					continue
				}
				switch entry.Key {
				case "default.audio.sink":
					snap.DefaultSink = value.Name
				case "default.audio.source":
					snap.DefaultSource = value.Name
				}
			}
		}
	}
	for _, obj := range objects {
		switch {
		case strings.Contains(obj.Type, "Node"):
			var props NodeProps
			if obj.Info.Props != nil {
				if err := json.Unmarshal(obj.Info.Props, &props); err != nil {
					return nil, fmt.Errorf("parse PipeWire node %d properties: %w", obj.ID, err)
				}
			}
			if parent, ok := devices[props.DeviceID]; ok {
				if props.DeviceAPI == "" {
					props.DeviceAPI = parent.DeviceAPI
				}
				if props.DeviceBus == "" {
					props.DeviceBus = parent.DeviceBus
				}
				if props.VendorID == "" {
					props.VendorID = parent.VendorID
				}
				if props.ProductID == "" {
					props.ProductID = parent.ProductID
				}
				props.DeviceDescription = parent.DeviceDescription
			}
			snap.Nodes = append(snap.Nodes, Node{
				ID:    obj.ID,
				Type:  obj.Type,
				State: obj.Info.State,
				Props: props,
			})
		case strings.Contains(obj.Type, "Link"):
			snap.Links = append(snap.Links, Link{
				ID:           obj.ID,
				OutputNodeID: obj.Info.OutputNodeID,
				InputNodeID:  obj.Info.InputNodeID,
				State:        obj.Info.State,
			})
		}
	}
	return snap, nil
}

// JabraSourceNodes returns all Jabra microphone/source nodes.
// Identified by Jabra USB VID (0b0e) in the ALSA node name.
func (s *Snapshot) JabraSourceNodes() []Node {
	var out []Node
	for _, n := range s.Nodes {
		if isJabraNode(n) &&
			(n.Props.MediaClass == "Audio/Source" || strings.Contains(n.Props.NodeName, "input")) {
			out = append(out, n)
		}
	}
	return out
}

// JabraSinkNodes returns all Jabra speaker/sink nodes.
func (s *Snapshot) JabraSinkNodes() []Node {
	var out []Node
	for _, n := range s.Nodes {
		if isJabraNode(n) &&
			(n.Props.MediaClass == "Audio/Sink" || strings.Contains(n.Props.NodeName, "output")) {
			out = append(out, n)
		}
	}
	return out
}

func isJabraNode(node Node) bool {
	if soundVendorVerified(node) {
		return true
	}
	text := strings.ToLower(strings.Join([]string{
		node.Props.NodeName,
		node.Props.NodeDescription,
		node.Props.NodeNick,
		node.Props.CardName,
		node.Props.DeviceDescription,
	}, " "))
	return strings.Contains(text, "0b0e") || strings.Contains(text, "jabra")
}

// StreamNodes returns all active application stream nodes.
func (s *Snapshot) StreamNodes() []Node {
	var out []Node
	for _, n := range s.Nodes {
		if strings.HasPrefix(n.Props.MediaClass, "Stream/") {
			out = append(out, n)
		}
	}
	return out
}

// LinkedTo returns true if nodeID has any link to targetID.
func (s *Snapshot) LinkedTo(nodeID, targetID int) bool {
	for _, l := range s.Links {
		if (l.OutputNodeID == nodeID && l.InputNodeID == targetID) ||
			(l.OutputNodeID == targetID && l.InputNodeID == nodeID) {
			return true
		}
	}
	return false
}
