package main

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

var soundViewMu sync.RWMutex
var soundView = pipewire.SoundState{Error: "Checking PipeWire"}

// Selection and editor fields are owned only by the UI event loop.
var soundSelectionToken string
var soundDetailTarget pipewire.SoundTarget
var soundEditVolume int
var soundMusicConfirm bool

func soundDetailItems(node pipewire.SoundNode) []string {
	volume, mute := "unavailable", "unavailable"
	if node.Volume != nil {
		volume = fmt.Sprintf("%d%%", *node.Volume)
	}
	if node.Muted != nil {
		mute = onOff(*node.Muted)
	}
	items := []string{"Volume: " + volume, "Mute: " + mute, "Use as default " + node.Kind}
	if len(node.Modes) > 0 {
		items = append(items, "Music (headset mic off)", "Calls (headset mic available)")
	}
	return append(items, "Back")
}

func soundModeAction(results chan<- actionResult, mode string) {
	target := soundDetailTarget
	node, ok := soundTargetNode(target)
	if !ok {
		setStatus("Audio device changed; reopen Sound", true)
		return
	}
	if !node.Editable || !strings.Contains("|"+strings.Join(node.Modes, "|")+"|", "|"+mode+"|") {
		setStatus("This audio mode is not available", true)
		return
	}
	if mode == "music" && node.MicrophoneBusy && !soundMusicConfirm {
		soundMusicConfirm = true
		requestUIRedraw()
		return
	}
	soundMusicConfirm = false
	menuState = screenSound
	setStatus("Changing audio profile...", false)
	runUIAction(results, "Audio profile read back. Music disables the headset microphone; Calls restores it.", func() error {
		var state pipewire.SoundState
		if err := tuiIPCCall("sound.mode", map[string]any{"target": target, "mode": mode}, &state); err != nil {
			return err
		}
		updateSoundView(state)
		return nil
	})
}

func updateSoundView(state pipewire.SoundState) {
	soundViewMu.Lock()
	changed := !reflect.DeepEqual(soundView, state)
	soundView = state
	soundViewMu.Unlock()
	if changed {
		requestUIRedraw()
	}
}
func currentSoundView() pipewire.SoundState {
	soundViewMu.RLock()
	defer soundViewMu.RUnlock()
	return soundView
}
func soundTargetNode(target pipewire.SoundTarget) (pipewire.SoundNode, bool) {
	for _, node := range currentSoundView().Nodes {
		if node.Target == target {
			return node, true
		}
	}
	return pipewire.SoundNode{}, false
}
func rememberSoundSelection() {
	if menuState != screenSound {
		return
	}
	nodes := currentSoundView().Nodes
	if currentSelection >= 0 && currentSelection < len(nodes) {
		soundSelectionToken = nodes[currentSelection].Target.Token
	}
}
func syncSoundSelection() {
	if menuState != screenSound {
		return
	}
	nodes := currentSoundView().Nodes
	for i, node := range nodes {
		if node.Target.Token != "" && node.Target.Token == soundSelectionToken {
			currentSelection = i
			return
		}
	}
	currentSelection = clampSelection(currentSelection, len(nodes))
	rememberSoundSelection()
}
func soundAction(results chan<- actionResult, action string, percent int, mode string) {
	target := soundDetailTarget
	node, ok := soundTargetNode(target)
	if !ok {
		setStatus("Audio device changed. Reopen Sound.", true)
		return
	}
	if !node.Editable {
		setStatus("Audio identity is not verified. Controls are read only.", true)
		return
	}
	params := map[string]any{"target": target}
	if action == "volume" {
		params["percent"] = percent
	}
	if action == "mute" {
		params["mode"] = mode
	}
	message := "Sound change read back"

	runUIAction(results, message, func() error {
		var changed pipewire.SoundNode
		if err := tuiIPCCall("sound."+action, params, &changed); err != nil {
			return err
		}
		var state pipewire.SoundState
		if err := tuiIPCCall("sound.list", nil, &state); err != nil {
			return err
		}
		updateSoundView(state)
		return nil
	})
}
func handleSoundEnter(results chan<- actionResult) {
	if menuState == screenSound {
		nodes := currentSoundView().Nodes
		if currentSelection < 0 || currentSelection >= len(nodes) {
			return
		}
		soundDetailTarget = nodes[currentSelection].Target
		soundMusicConfirm = false
		menuState = screenSoundDetail
		currentSelection = 0
		return
	}
	node, ok := soundTargetNode(soundDetailTarget)
	if !ok {
		setStatus("Audio device changed. Go back and select it again.", true)
		return
	}
	switch currentSelection {
	case 0:
		if !node.Editable || node.Volume == nil {
			setStatus("Volume is unavailable or read only", true)
			return
		}
		soundEditVolume = *node.Volume
		menuState = screenSoundVolume
	case 1:
		soundAction(results, "mute", 0, "toggle")
	case 2:
		soundAction(results, "default", 0, "")
	case 3:
		if len(node.Modes) > 0 {
			soundModeAction(results, "music")
			return
		}
		menuState = screenSound
		currentSelection = 0
		syncSoundSelection()
	case 4:
		soundModeAction(results, "calls")
	case 5:
		menuState = screenSound
		currentSelection = 0
		syncSoundSelection()
	}
}
func handleSoundVolumeKey(event keyEvent, results chan<- actionResult) {
	switch navigationKey(event) {
	case keyUp:
		soundEditVolume = min(100, soundEditVolume+1)
	case keyDown:
		soundEditVolume = max(0, soundEditVolume-1)
	case keyEnter:
		menuState = screenSoundDetail
		currentSelection = 0
		soundAction(results, "volume", soundEditVolume, "")
	case keyBack, keyEscape:
		menuState = screenSoundDetail
		currentSelection = 0
		setStatus("Volume edit cancelled", false)
	}
	requestUIRedraw()
}

func renderSound() {
	drawingBox()
	state := currentSoundView()
	drawListHeading("Sound", currentSelection, len(state.Nodes))
	if !state.Available {
		drawCentered(9, trimToWidth(state.Error, max(12, width-18)), false)
		drawSplitActionBar([]string{"Q Back"}, nil)
		return
	}
	if len(state.Nodes) == 0 {
		drawCentered(9, "No Jabra audio detected", false)
		drawCentered(11, "Connect USB or use your desktop Bluetooth settings.", false)
	}
	_, _, bottom := panelBounds()
	visible := max(1, bottom-9)
	offset, end := listWindow(currentSelection, len(state.Nodes), visible)
	drawListWindowHint(8, offset, end, len(state.Nodes))
	for i := offset; i < end; i++ {
		node := state.Nodes[i]
		value := "unavailable"
		if node.Volume != nil {
			value = fmt.Sprintf("%d%%", *node.Volume)
		}
		if node.Muted != nil && *node.Muted {
			value += " muted"
		}
		if node.Default {
			value += " default"
		}
		if node.Channels > 0 {
			value += fmt.Sprintf(" %dch", node.Channels)
		}
		label := fmt.Sprintf("%s %d: %s (%s)", node.Kind, node.Target.ID, node.Name, node.Connection)
		drawLabelValue(9+i-offset, label, value, i == currentSelection)
	}
	drawSplitActionBar([]string{hintBack}, []string{hintMove, hintOpen})
}
func renderSoundDetail() {
	drawingBox()
	node, ok := soundTargetNode(soundDetailTarget)
	if !ok {
		drawCentered(9, "Audio device changed. Go back and select it again.", false)
		drawSplitActionBar([]string{"Q Back"}, nil)
		return
	}
	left, _, bottom := panelBounds()
	infoRow, startRow := 8, 10
	if height < 22 {
		infoRow, startRow = 7, 8
	}
	items := soundDetailItems(node)
	drawListHeading("Sound controls", currentSelection, len(items))
	drawCentered(infoRow, fmt.Sprintf("%s: %s (%s; mode %s)", node.Kind, node.Name, node.Connection, node.AudioMode), false)
	if soundMusicConfirm {
		drawCentered(min(startRow, bottom-2), "Music will disable this headset microphone.", false)
		drawCentered(min(startRow+2, bottom-1), "This can interrupt a call or recording.", false)
		drawSplitActionBar([]string{"Q Cancel"}, []string{"Enter Use music"})
		return
	}
	if menuState == screenSoundVolume {
		drawCenteredStyled(min(11, bottom-2), fmt.Sprintf("%d%%", soundEditVolume), styleTitle)
		drawCentered(min(14, bottom-1), "Nothing changes until you press Enter.", false)
		drawSplitActionBar([]string{"Q/Esc Cancel"}, []string{"Up/Down Change", "Enter Save"})
		return
	}
	start, end := listWindow(currentSelection, len(items), max(1, bottom-startRow))
	for i := start; i < end; i++ {
		drawListItem(startRow+i-start, left+4, items[i], i == currentSelection)
	}
	if node.Note != "" && 18 < bottom {
		drawListItem(18, left+3, trimToWidth(node.Note, max(12, width-17)), false)
	}
	drawSplitActionBar([]string{"Q Back"}, []string{"Up/Down Select", "Enter Change"})
}
