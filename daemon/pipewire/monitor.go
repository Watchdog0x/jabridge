// pipewire/monitor — PipeWire audio graph monitor for Jabridge.
//
// Polls `pw-dump` to snapshot the audio graph and detect when
// communication streams are linked to Jabra microphone nodes.
// Pure Go — no libpipewire, no cgo.

package pipewire

import (
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
	MediaClass string `json:"media.class"`
	AppName    string `json:"application.name"`
	MediaRole  string `json:"media.role"`
	NodeName   string `json:"node.name"`
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
	Nodes []Node
	Links []Link
	Time  time.Time
}

// pwDumpObject is the raw JSON structure from pw-dump.
type pwDumpObject struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	Info struct {
		State        string          `json:"state"`
		OutputNodeID int             `json:"output-node-id"`
		InputNodeID  int             `json:"input-node-id"`
		Props        json.RawMessage `json:"props"`
	} `json:"info"`
}

// TakeSnapshot runs pw-dump and parses the output into a Snapshot.
func TakeSnapshot() (*Snapshot, error) {
	cmd := exec.Command("pw-dump")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("pw-dump: %w", err)
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

	snap := &Snapshot{Time: time.Now()}
	for _, obj := range objects {
		switch {
		case strings.Contains(obj.Type, "Node"):
			var props NodeProps
			if obj.Info.Props != nil {
				if err := json.Unmarshal(obj.Info.Props, &props); err != nil {
					return nil, fmt.Errorf("parse PipeWire node %d properties: %w", obj.ID, err)
				}
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
		if strings.Contains(n.Props.NodeName, "0b0e") &&
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
		if strings.Contains(n.Props.NodeName, "0b0e") &&
			(n.Props.MediaClass == "Audio/Sink" || strings.Contains(n.Props.NodeName, "output")) {
			out = append(out, n)
		}
	}
	return out
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
