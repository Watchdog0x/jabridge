package main

import "testing"

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
