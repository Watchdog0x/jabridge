package main

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type settingEditor struct {
	scope           settingScope
	setting         deviceSettingValue
	device          *jabra_DeviceInfo
	parentSelection int
	choices         []string
	choice          int
	text            string
	limit           int
	textMode        bool
	replaceText     bool
	save            func(string) error
}

// Owned only by the UI event loop. Typed contents are never history events.
var activeSettingEditor *settingEditor

func settingIsText(setting deviceSettingValue) bool {
	return setting.Text != nil || setting.Remote != nil && setting.Remote.Kind == "text"
}

func settingChoices(setting deviceSettingValue) []string {
	if setting.Remote != nil {
		return append([]string(nil), setting.Remote.Choices...)
	}
	if setting.Boolean != nil {
		return []string{"Off", "On"}
	}
	var choices []string
	if setting.Choice != nil {
		for _, choice := range setting.Choice.Definition.Choices {
			choices = append(choices, choice.Name)
		}
	}
	return choices
}

func openSettingEditor(scope settingScope, setting deviceSettingValue, device *jabra_DeviceInfo) error {
	if device == nil || !setting.editable() {
		return errors.New("setting is not editable")
	}
	if setting.Remote != nil && setting.Remote.Target == nil {
		return errors.New("reload settings before editing")
	}
	editor := &settingEditor{scope: scope, setting: setting, device: cloneDeviceInfo(device), parentSelection: currentSelection,
		choices: settingChoices(setting), text: setting.valueName(), textMode: settingIsText(setting), replaceText: true}
	editor.save = func(value string) error {
		if setting.Remote != nil {
			return setIPCSetting(setting, value)
		}
		_, _, err := applyDeviceSettingFromText(device, setting, value)
		return err
	}
	if editor.textMode {
		if !utf8.ValidString(editor.text) || strings.IndexFunc(editor.text, unicode.IsControl) >= 0 {
			return errors.New("device returned non-displayable text; use debug before editing")
		}
		if setting.Text != nil {
			editor.limit = setting.Text.Definition.MaxBytes
		} else {
			editor.limit = setting.Remote.MaxBytes
		}
		if editor.limit < 1 || editor.limit > 4096 {
			return errors.New("text length limit is unavailable; use the CLI")
		}
	} else {
		if len(editor.choices) == 0 {
			return errors.New("no allowed values were supplied")
		}
		for index, value := range editor.choices {
			if value == setting.valueName() {
				editor.choice = index
				break
			}
		}
	}
	activeSettingEditor = editor
	setStatus("", false)
	requestUIRedraw()
	return nil
}

func editorRune(event keyEvent) (rune, bool) {
	if event >= keyPasteBase {
		event -= keyPasteBase
	} else if event >= keyRuneBase {
		event -= keyRuneBase
	} else {
		return 0, false
	}
	r := rune(event)
	return r, utf8.ValidRune(r) && !unicode.IsControl(r)
}

func handleSettingEditorKey(event keyEvent, results chan<- actionResult) {
	editor := activeSettingEditor
	if editor == nil {
		return
	}
	if event == keyEscape || !editor.textMode && navigationKey(event) == keyBack {
		closeSettingEditor()
		setStatus("Edit cancelled. No setting changed.", false)
		return
	}
	if width < 40 || height < 20 {
		return
	}
	if editor.textMode {
		if r, ok := editorRune(event); ok {
			value := editor.text
			if editor.replaceText {
				value = ""
			}
			value += string(r)
			if len(value) > editor.limit {
				setStatus(fmt.Sprintf("Maximum length is %d bytes", editor.limit), true)
				return
			}
			editor.text, editor.replaceText = value, false
			requestUIRedraw()
			return
		}
		switch event {
		case keyBackspace:
			if editor.replaceText {
				editor.text = ""
			} else if len(editor.text) > 0 {
				_, size := utf8.DecodeLastRuneInString(editor.text)
				editor.text = editor.text[:len(editor.text)-size]
			}
			editor.replaceText = false
		case keyClearText:
			editor.text, editor.replaceText = "", false
		case keyEnter:
			saveSettingEditor(results)
		}
	} else {
		switch navigationKey(event) {
		case keyUp:
			editor.choice = max(0, editor.choice-1)
		case keyDown:
			editor.choice = min(len(editor.choices)-1, editor.choice+1)
		case keyEnter:
			saveSettingEditor(results)
		}
	}
	requestUIRedraw()
}

func closeSettingEditor() {
	if activeSettingEditor != nil {
		currentSelection = activeSettingEditor.parentSelection
		activeSettingEditor = nil
	}
	requestUIRedraw()
}

func saveSettingEditor(results chan<- actionResult) {
	if width < 40 || height < 20 {
		setStatus("Make the terminal larger before saving", true)
		return
	}
	editor := *activeSettingEditor
	wanted := editor.text
	if !editor.textMode {
		wanted = editor.choices[editor.choice]
	}
	closeSettingEditor()
	if wanted == editor.setting.valueName() {
		setStatus("No change made.", false)
		startSettingsLoad(results, editor.scope)
		return
	}
	setStatus("Saving "+editor.setting.label()+"...", false)
	message := editor.setting.label() + " saved and read back from device"

	runUIAction(results, message, func() error {
		return editor.save(wanted)
	}, withSettingsRefresh(editor.scope))
}

func renderSettingEditor() {
	editor := activeSettingEditor
	if editor == nil {
		return
	}
	drawingBox()
	if width < 40 || height < 20 {
		drawCentered(6, trimToWidth("Needs 40x20 to edit", max(1, width-8)), false)
		drawSplitActionBar([]string{hintCancel}, nil)
		return
	}
	left, right, bottom := panelBounds()
	if editor.textMode {
		drawCenteredStyled(6, editor.setting.label(), styleTitle)
	} else {
		drawListHeading(editor.setting.label(), editor.choice, len(editor.choices))
	}
	drawListItem(8, left+4, "Device: "+editor.device.deviceName, false)
	if editor.setting.needsConfigMode() {
		drawListItem(9, left+4, "Saving may restart this device. Do not edit during a call.", false)
	}
	if editor.textMode {
		drawListItem(10, left+4, "Type a new value. Backspace edits; Ctrl+U clears.", false)
		value := editor.text
		if value == "" {
			value = " "
		}
		drawListItem(11, left+4, value, true)
		drawListItem(bottom-1, left+4, fmt.Sprintf("%d/%d bytes", len(editor.text), editor.limit), false)
		drawSplitActionBar([]string{"Esc Cancel"}, []string{"Enter Save"})
		return
	}
	top, end := 10, bottom-1
	if editor.setting.needsConfigMode() {
		top++
	}
	visible := max(1, end-top)
	start, windowEnd := listWindow(editor.choice, len(editor.choices), visible)
	drawListWindowHint(top-1, start, windowEnd, len(editor.choices))
	for index := start; index < windowEnd; index++ {
		label := editor.choices[index]
		if label == editor.setting.valueName() {
			label += " (current)"
		}
		drawListItem(top+index-start, left+4, label, index == editor.choice)
	}
	help := editor.setting.help()
	if help == "" {
		help = "Choose a value. Nothing changes until you press Enter."
	}
	drawListItem(bottom-1, left+4, trimToWidth(strings.TrimSpace(help), max(1, right-left-6)), false)
	drawSplitActionBar([]string{"Esc/Q Cancel"}, []string{hintMove, hintSave})
}
