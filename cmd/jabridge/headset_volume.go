package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/headsetvolume"
	"github.com/Watchdog0x/jabridge/internal/history"
)

var openHeadsetVolume = headsetvolume.Open

func runHeadset(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") || len(args) == 2 && args[0] == "volume" && (args[1] == "--help" || args[1] == "-h") {
		fmt.Println("Usage: jabridge headset volume [get | info | 0..100]\nWithout a value, reads the headset's saved volume, which may differ from current playback.\nUse info to check support for the selected model, firmware and connection.\nStart playback before changing the volume. At its limits, the headset may also send volume keys to Linux.")
		return nil
	}
	command, err := parseHeadsetVolumeCommand(args)
	if err != nil {
		return err
	}
	backend, err := connectTUIService()
	if err != nil {
		return err
	}
	defer backend.close()
	return runHeadsetVolumeCommand(backend.clientSnapshot(), command, os.Stdout)
}

type headsetVolumeCommand struct {
	Action  string
	Percent int
}

func parseHeadsetVolumeCommand(args []string) (headsetVolumeCommand, error) {
	if len(args) == 1 && args[0] == "volume" || len(args) == 2 && args[0] == "volume" && args[1] == "get" {
		return headsetVolumeCommand{Action: "get"}, nil
	}
	if len(args) == 2 && args[0] == "volume" && args[1] == "info" {
		return headsetVolumeCommand{Action: "info"}, nil
	}
	percent, err := parseHeadsetVolume(args)
	return headsetVolumeCommand{Action: "set", Percent: percent}, err
}

func parseHeadsetVolume(args []string) (int, error) {
	if len(args) != 2 || args[0] != "volume" {
		return 0, errors.New("usage: jabridge headset volume <0..100>")
	}
	percent, err := strconv.Atoi(args[1])
	if err != nil || percent < 0 || percent > 100 {
		return 0, errors.New("headset volume must be from 0 to 100")
	}
	return percent, nil
}

func runHeadsetVolumeClient(client *ipc.Client, percent int, out io.Writer) error {
	return runHeadsetVolumeCommand(client, headsetVolumeCommand{Action: "set", Percent: percent}, out)
}

func runHeadsetVolumeCommand(client *ipc.Client, command headsetVolumeCommand, out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var devices []ipc.DeviceInfo
	if err := client.Call(ctx, "devices.list", nil, &devices); err != nil {
		return err
	}
	var selected *ipc.DeviceInfo
	for i := range devices {
		if devices[i].Selected && !devices[i].IsDongle {
			if selected != nil {
				return errors.New("more than one headset is selected")
			}
			selected = &devices[i]
		}
	}
	if selected == nil {
		return errors.New("no headset is selected; connect and select the headset first")
	}
	if len(selected.Instance) != 32 {
		return errors.New("headset attachment identity is unavailable; reconnect it")
	}
	target := ipc.SettingTarget{ID: selected.ID, Instance: selected.Instance, Topology: selected.Topology}
	if command.Action == "info" {
		var c headsetvolume.Capabilities
		if err := client.Call(ctx, "device.volume.info", map[string]any{"target": target}, &c); err != nil {
			return err
		}
		if c.Reason != "" {
			_, err := fmt.Fprintln(out, c.Reason)
			return err
		}
		_, err := fmt.Fprintf(out, "Headset volume support: %s (%s)\nRead: %s level\nSet percentage: %t\n", c.Model, c.Connection, c.Read, c.SetPercent)
		return err
	}
	if command.Action == "get" {
		var value headsetvolume.Value
		if err := client.Call(ctx, "device.volume.get", map[string]any{"target": target}, &value); err != nil {
			return err
		}
		if value.Source != "saved" || value.Percent < 0 || value.Percent > 100 {
			return errors.New("headset returned an invalid saved volume reply")
		}
		_, err := fmt.Fprintf(out, "Saved headset volume: %d%%\nThis may differ from the current playback volume.\n", value.Percent)
		return err
	}
	if command.Action != "set" || command.Percent < 0 || command.Percent > 100 {
		return errors.New("invalid headset volume command")
	}
	params := struct {
		Target  ipc.SettingTarget `json:"target"`
		Percent int               `json:"percent"`
	}{target, command.Percent}
	var value headsetvolume.Value
	if err := client.Call(ctx, "device.volume", params, &value); err != nil {
		return err
	}
	if value.Percent != command.Percent {
		return errors.New("headset volume reply does not match the request")
	}
	_, err := fmt.Fprintf(out, "Headset accepted volume request: %d%%\n", value.Percent)
	return err
}

func (j *jabraAPIBridge) SetHeadsetVolume(target ipc.SettingTarget, percent int) (_ headsetvolume.Value, resultErr error) {
	device, capability, err := headsetVolumeDevice(target)
	if err != nil {
		return headsetvolume.Value{}, err
	}
	if !capability.SetPercent {
		return headsetvolume.Value{}, errors.New(capability.Reason)
	}
	if percent < 0 || percent > 100 {
		return headsetvolume.Value{}, errors.New("headset volume must be from 0 to 100")
	}
	event := historyDeviceEvent(device, "volume")
	event.VolumePercent = &percent
	finish := history.Begin(event)
	defer history.EndDeferred(finish, &resultErr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	usb, err := openHeadsetVolume(ctx, device.volumeAttachment, device.productID, device.firmwareVersion)
	if err != nil {
		return headsetvolume.Value{}, err
	}
	defer func() { _ = usb.Close() }()
	if err := currentSettingDevice(device); err != nil {
		return headsetvolume.Value{}, err
	}
	return headsetvolume.Set(ctx, usb, percent)
}

func headsetVolumeDevice(target ipc.SettingTarget) (*jabra_DeviceInfo, headsetvolume.Capabilities, error) {
	device, ok := selectedHeadsetSnapshot()
	if !ok || device.isDongle {
		return nil, headsetvolume.Capabilities{}, errors.New("no headset is selected")
	}
	if err := validateSettingTarget(device, &target); err != nil {
		return nil, headsetvolume.Capabilities{}, err
	}
	connection := "dongle"
	if device.deviceConnection == deviceConnectionType_USB {
		connection = "usb"
	}
	return device, headsetvolume.ForDevice(device.productID, device.firmwareVersion, connection), nil
}

func (j *jabraAPIBridge) HeadsetVolumeCapabilities(target ipc.SettingTarget) (headsetvolume.Capabilities, error) {
	_, c, err := headsetVolumeDevice(target)
	return c, err
}

func (j *jabraAPIBridge) GetHeadsetVolume(target ipc.SettingTarget) (_ headsetvolume.Value, resultErr error) {
	device, c, err := headsetVolumeDevice(target)
	if err != nil {
		return headsetvolume.Value{}, err
	}
	if c.Read != "saved" {
		return headsetvolume.Value{}, errors.New(c.Reason)
	}
	finish := history.Begin(historyDeviceEvent(device, "volume-read"))
	defer history.EndDeferred(finish, &resultErr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	usb, err := openHeadsetVolume(ctx, device.volumeAttachment, device.productID, device.firmwareVersion)
	if err != nil {
		return headsetvolume.Value{}, err
	}
	defer func() { _ = usb.Close() }()
	if err := currentSettingDevice(device); err != nil {
		return headsetvolume.Value{}, err
	}
	return headsetvolume.ReadSaved(ctx, usb)
}
