package main

import (
	"reflect"
	"testing"

	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

func TestParseSoundVolume(t *testing.T) {
	volume, err := parseSoundVolume("Volume: 0.46 [MUTED]\n")
	if err != nil {
		t.Fatal(err)
	}
	if volume.Percent != 46 || !volume.Muted {
		t.Fatalf("volume = %#v", volume)
	}
	if _, err := parseSoundVolume("no volume here"); err == nil {
		t.Fatal("missing volume was accepted")
	}
}

func TestBestMatchingAudioNodeSeparatesUSBAndDonglePaths(t *testing.T) {
	nodes := []pipewire.Node{
		{ID: 10, State: "idle", Props: pipewire.NodeProps{NodeDescription: "Jabra Evolve2 65 Analog Stereo"}},
		{ID: 20, State: "running", Props: pipewire.NodeProps{NodeDescription: "Jabra Link 380 Analog Stereo"}},
	}
	if node, found := bestMatchingAudioNode(nodes, "Jabra Evolve2 65"); !found || node.ID != 10 {
		t.Fatalf("direct USB match = %#v, %v", node, found)
	}
	if node, found := bestMatchingAudioNode(nodes, "Jabra Link 380"); !found || node.ID != 20 {
		t.Fatalf("dongle match = %#v, %v", node, found)
	}
}

func TestFollowSelectedDeviceAudioSetsOutputAndMicrophone(t *testing.T) {
	oldSnapshot := takeAudioSnapshot
	oldSetDefault := setDefaultAudioNode
	t.Cleanup(func() {
		takeAudioSnapshot = oldSnapshot
		setDefaultAudioNode = oldSetDefault
	})
	takeAudioSnapshot = func() (*pipewire.Snapshot, error) {
		return &pipewire.Snapshot{Nodes: []pipewire.Node{
			{ID: 45, Props: pipewire.NodeProps{MediaClass: "Audio/Sink", NodeDescription: "Jabra Link 380 Analog Stereo"}},
			{ID: 44, Props: pipewire.NodeProps{MediaClass: "Audio/Source", NodeDescription: "Jabra Link 380 Mono"}},
			{ID: 55, Props: pipewire.NodeProps{MediaClass: "Audio/Sink", NodeDescription: "Jabra Evolve2 65 Analog Stereo"}},
		}}, nil
	}
	var selected []int
	setDefaultAudioNode = func(nodeID int) error {
		selected = append(selected, nodeID)
		return nil
	}
	if err := followSelectedDeviceAudio("Jabra Link 380"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selected, []int{45, 44}) {
		t.Fatalf("default nodes = %v", selected)
	}
}
