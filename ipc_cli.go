package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Watchdog0x/jabridge/daemon"
	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

func runIPC(args []string) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help")) {
		printIPCUsage()
		return nil
	}

	connectContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := ipc.Dial(connectContext, ipcSocketPath())
	if err != nil {
		return fmt.Errorf("service is not running; run 'jabridge service start': %w", err)
	}
	defer func() { _ = client.Close() }()

	switch args[0] {
	case "ping":
		if len(args) != 1 {
			return errors.New("usage: jabridge ipc ping")
		}
		ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if err := client.Ping(ctx); err != nil {
			return err
		}
		fmt.Println("Service is ready.")
		return nil
	case "devices":
		if len(args) != 1 {
			return errors.New("usage: jabridge ipc devices")
		}
		var devices []ipc.DeviceInfo
		if err := ipcCall(client, "devices.list", nil, &devices); err != nil {
			return err
		}
		return printIPCJSON(devices)
	case "battery":
		if len(args) != 1 {
			return errors.New("usage: jabridge ipc battery")
		}
		var battery ipc.BatteryInfo
		if err := ipcCall(client, "device.battery", nil, &battery); err != nil {
			return err
		}
		return printIPCJSON(battery)
	case "settings":
		if len(args) != 2 || (args[1] != "dongle" && args[1] != "headset") {
			return errors.New("usage: jabridge ipc settings dongle|headset")
		}
		var settings []ipc.SettingInfo
		if err := ipcCall(client, "settings.list", map[string]string{"device": args[1]}, &settings); err != nil {
			return err
		}
		return printIPCJSON(settings)
	case "set":
		if len(args) != 4 || (args[1] != "dongle" && args[1] != "headset") {
			return errors.New("usage: jabridge ipc set dongle|headset SETTING VALUE")
		}
		var setting ipc.SettingInfo
		params := map[string]string{"device": args[1], "key": args[2], "value": args[3]}
		if err := ipcCall(client, "settings.set", params, &setting); err != nil {
			return err
		}
		return printIPCJSON(setting)
	case "select":
		if len(args) != 2 {
			return errors.New("usage: jabridge ipc select DEVICE_ID")
		}
		id, parseErr := strconv.ParseUint(args[1], 0, 16)
		if parseErr != nil {
			return fmt.Errorf("device ID must be a number: %w", parseErr)
		}
		var result map[string]bool
		if err := ipcCall(client, "device.select", map[string]uint16{"id": uint16(id)}, &result); err != nil {
			return err
		}
		fmt.Println("Device selected.")
		return nil
	case "watch":
		if len(args) != 1 {
			return errors.New("usage: jabridge ipc watch")
		}
		return watchIPC(client)
	default:
		return fmt.Errorf("unknown IPC command %q; run jabridge ipc --help", args[0])
	}
}

func ipcSocketPath() string {
	if path := os.Getenv("JABRIDGE_SOCKET"); path != "" {
		return path
	}
	return daemon.DefaultConfig().SocketPath
}

func ipcCall(client *ipc.Client, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return client.Call(ctx, method, params, result)
}

func printIPCJSON(value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

func watchIPC(client *ipc.Client) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	subscribeContext, stopSubscribe := context.WithTimeout(ctx, 5*time.Second)
	err := client.Subscribe(subscribeContext)
	stopSubscribe()
	if err != nil {
		return err
	}
	fmt.Println("Watching device events. Press Ctrl+C to stop.")
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-client.Done():
			return errors.New("service connection closed")
		case notification := <-client.Notifications():
			if err := printIPCJSON(notification); err != nil {
				return err
			}
		case <-keepalive.C:
			pingContext, stop := context.WithTimeout(ctx, 3*time.Second)
			err := client.Ping(pingContext)
			stop()
			if err != nil {
				return fmt.Errorf("service keepalive failed: %w", err)
			}
		}
	}
}

func printIPCUsage() {
	fmt.Println(`Usage:
  jabridge ipc ping
  jabridge ipc devices
  jabridge ipc battery
  jabridge ipc settings dongle|headset
  jabridge ipc set dongle|headset SETTING VALUE
  jabridge ipc select DEVICE_ID
  jabridge ipc watch

Start ` + "`jabridge --daemon`" + ` first. Commands use the private per-user Unix
socket. The set command changes hardware; list, ping, devices, battery, and
watch are read-only.`)
}
