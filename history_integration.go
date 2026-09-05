package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Watchdog0x/jabridge/internal/buildinfo"
	"github.com/Watchdog0x/jabridge/internal/history"
)

// Accessed only by the TUI event loop.
var lastHistoryNavigation time.Time

func configureHistory() {
	for _, scope := range []settingScope{settingScopeDongle, settingScopeHeadset} {
		for _, definition := range settingDefinitions(scope) {
			history.RegisterSettings(definition.Key)
		}
		for _, definition := range choiceSettingDefinitions(scope) {
			history.RegisterSettings(definition.Key)
		}
	}
	for _, definition := range headsetTextSettingDefinitions {
		history.RegisterSettings(definition.Key)
	}
	history.Configure(buildinfo.Version)
}

func historyDeviceEvent(device *jabra_DeviceInfo, action string) history.Event {
	event := history.Event{Component: "device", Action: action}
	if device != nil {
		event.USBProduct = device.productID
		event.Connection = "usb"
		if device.deviceConnection == deviceConnectionType_BT {
			event.Connection = "dongle"
		}
	}
	return event
}

func tuiHistoryEvent(action string) history.Event {
	device, _ := selectedHeadsetSnapshot()
	if menuState == screenDongleSettings || menuState == screenPairedDevices || device == nil {
		device, _ = selectedDongleSnapshot()
	}
	if menuState == screenFirmware {
		if target, ok := selectedFirmwareTarget(); ok {
			device = target.Device
		}
	} else if menuState == screenSwitchDevice && currentSelection >= 0 && currentSelection < len(switchDeviceItems) {
		device = switchDeviceItems[currentSelection].Device
	}
	event := historyDeviceEvent(device, action)
	event.Component = "tui"
	event.Screen = map[int]string{screenStartMenu: "home", screenSearch: "search", screenPairedDevices: "remembered", screenDongleSettings: "dongle-settings", screenHeadsetSettings: "headset-settings", screenSwitchDevice: "devices", screenFirmware: "firmware"}[menuState]
	event.Selection = currentSelection
	if menuState == screenDongleSettings || menuState == screenHeadsetSettings {
		scope := settingScopeHeadset
		if menuState == screenDongleSettings {
			scope = settingScopeDongle
		}
		if setting, ok := selectedDeviceSetting(scope); ok {
			event.Setting = setting.key()
		}
	}
	return event
}

func writeHistoryReport(out *bytes.Buffer) {
	fmt.Fprintln(out, "\nLocal event history (last 200 entries, up to 7 UTC calendar days):")
	directory, err := history.Directory()
	if err != nil {
		fmt.Fprintln(out, "History location unavailable.")
		return
	}
	recorder := &history.Recorder{Dir: directory}
	events, skipped, err := recorder.Read(200)
	if err != nil {
		fmt.Fprintln(out, "History unavailable:", history.Classify(err))
		return
	}
	if len(events) == 0 {
		fmt.Fprintln(out, "No retained events. History starts with this version; past RC14 TUI activity cannot be recovered.")
	}
	for _, event := range events {
		fmt.Fprintln(out, history.Describe(event))
	}
	if skipped > 0 {
		fmt.Fprintf(out, "Incomplete/unsafe history entries skipped: %d\n", skipped)
	}
	if missed, why := history.Status(); missed > 0 {
		fmt.Fprintf(out, "This process could not record %d events: %s\n", missed, why)
	}
	if os.Getenv("JABRIDGE_HISTORY") == "off" {
		fmt.Fprintln(out, "Recording is disabled for this process.")
	}
	fmt.Fprintln(out, "A start without a finish may mean an active operation, interruption or truncated retention; it does not establish the crash cause.")
}

func runHistory(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Println("Usage: jabridge history [clear]\nShows recent local actions and failures. Clear removes retained events; recording continues.")
		return nil
	}
	if len(args) == 1 && args[0] == "clear" {
		directory, err := history.Directory()
		if err != nil {
			return err
		}
		if err := (&history.Recorder{Dir: directory}).Clear(); err != nil {
			return err
		}
		fmt.Println("Retained history cleared. Recording continues for new actions.")
		return nil
	}
	if len(args) != 0 {
		return fmt.Errorf("usage: jabridge history [clear]")
	}
	var out bytes.Buffer
	writeHistoryReport(&out)
	n, err := os.Stdout.Write(out.Bytes())
	if err == nil && n != out.Len() {
		err = io.ErrShortWrite
	}
	return err
}
