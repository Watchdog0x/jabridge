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
		fmt.Println("Usage: jabridge headset volume <0..100>\nRequests a headset volume level. Start playback before adjusting it.\nAt its volume limits, the headset may also send volume keys to Linux.\nCurrently supports Evolve2 30 SE (0b0e:0e36), firmware 1.11.0, over direct USB.")
		return nil
	}
	percent, err := parseHeadsetVolume(args)
	if err != nil {
		return err
	}
	backend, err := connectTUIService()
	if err != nil {
		return err
	}
	defer backend.close()
	return runHeadsetVolumeClient(backend.clientSnapshot(), percent, os.Stdout)
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
	if selected.Connection != "usb" || !headsetvolume.Supported(selected.PID, selected.Firmware) {
		return errors.New("direct volume currently requires an Evolve2 30 SE (0b0e:0e36), firmware 1.11.0, over USB")
	}
	if len(selected.Instance) != 32 {
		return errors.New("headset attachment identity is unavailable; reconnect it")
	}
	params := struct {
		Target  ipc.SettingTarget `json:"target"`
		Percent int               `json:"percent"`
	}{ipc.SettingTarget{ID: selected.ID, Instance: selected.Instance, Topology: selected.Topology}, percent}
	var value headsetvolume.Value
	if err := client.Call(ctx, "device.volume", params, &value); err != nil {
		return err
	}
	if value.Percent != percent {
		return errors.New("headset volume reply does not match the request")
	}
	_, err := fmt.Fprintf(out, "Headset accepted volume request: %d%%\n", value.Percent)
	return err
}

func (j *jabraAPIBridge) SetHeadsetVolume(target ipc.SettingTarget, percent int) (_ headsetvolume.Value, resultErr error) {
	device, ok := selectedHeadsetSnapshot()
	if !ok {
		return headsetvolume.Value{}, errors.New("no headset is selected")
	}
	if err := validateSettingTarget(device, &target); err != nil {
		return headsetvolume.Value{}, err
	}
	if device.deviceConnection != deviceConnectionType_USB || device.isDongle || !headsetvolume.Supported(device.productID, device.firmwareVersion) {
		return headsetvolume.Value{}, errors.New("direct volume is not validated for this headset and firmware")
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
	usb, err := openHeadsetVolume(ctx, device.volumeAttachment, device.firmwareVersion)
	if err != nil {
		return headsetvolume.Value{}, err
	}
	defer func() { _ = usb.Close() }()
	if err := currentSettingDevice(device); err != nil {
		return headsetvolume.Value{}, err
	}
	return headsetvolume.Set(ctx, usb, percent)
}
