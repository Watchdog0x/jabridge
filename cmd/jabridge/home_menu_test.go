package main

import (
	"strings"
	"testing"
)

func TestLink380ShowsRememberedDevicesOnlyWhenNonempty(t *testing.T) {
	for _, test := range []struct {
		name string
		list *pairingList
		want string
	}{
		{"unavailable", nil, ""},
		{"empty", &pairingList{}, ""},
		{"stale count", &pairingList{count: 2}, ""},
		{"one remembered", &pairingList{pairedDevices: []pairedDevice{{deviceName: "Headset"}}}, "Remembered devices (1)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			withMenuState(t)
			d := metadataTestDongle()
			d.pairingList = test.list
			withDeviceState(t, devices{0: d}, -1, 0)
			updateStartMenu()
			got := ""
			for _, item := range startMenu {
				if item.id == 1 {
					got = item.label
				}
			}
			if got != test.want {
				t.Fatalf("remembered menu = %q; want %q", got, test.want)
			}
		})
	}
}

func TestNormalHeaderHasNoPreviewBadge(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, nil, -1, -1)
	t.Setenv(experimentalWritesEnv, "")
	for _, size := range [][2]int{{60, 24}, {120, 40}, {61, 24}, {80, 32}} {
		f := newRenderTarget(t, size[0], size[1])
		header()
		drawingBox()
		line := rowText(f, 1)
		if strings.Contains(line, "PREVIEW") || strings.Contains(line, "EXPERIMENTAL") {
			t.Fatal(line)
		}
		left, right, bottom := panelBounds()
		if textColumn(t, f, 1, "Jabridge") != left {
			t.Fatal("header shifted after resize")
		}
		if f.cells[3*f.width+left-1].ch != '┏' || f.cells[(bottom-1)*f.width+right-1].ch != '┛' {
			t.Fatal("panel border shifted after resize")
		}
	}
}

func TestHomeMenuLabelsStayAlignedBelowSound(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, nil, -1, -1)
	startMenu = []menuItem{{id: 8, label: "Sound"}, {id: 5, label: "Quit"}}
	for _, w := range []int{60, 61, 80, 100, 101, 140} {
		for _, selected := range []int{0, 1} {
			currentSelection = selected
			f := newRenderTarget(t, w, 24)
			menu()
			soundCol := textColumn(t, f, 11, "Sound")
			quitCol := textColumn(t, f, 12, "Quit")
			if quitCol != soundCol {
				t.Fatalf("width %d selection %d: Sound=%d Quit=%d", w, selected, soundCol, quitCol)
			}
		}
	}
}

func TestHomeMenuCentersOddAndEvenLabelsTogether(t *testing.T) {
	withMenuState(t)
	for _, w := range []int{60, 61, 80, 81, 100, 101, 140} {
		width, height = w, 30
		left, right, _ := panelBounds()
		for _, label := range []string{"Dongle settings", "Firmware", "Sound", "Quit"} {
			centerTwice := 2*labelColumnFor(label) + displayWidth(label) - 1
			if centerTwice > left+right || centerTwice < left+right-1 {
				t.Fatalf("%s is off center at width %d", label, w)
			}
		}
	}
}
