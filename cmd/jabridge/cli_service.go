package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/buildinfo"
)

// Use the existing service without stopping it or changing its selected
// device. Standalone direct access remains available when no service exists.
func tryServiceCLI(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "status", "battery", "settings", "diagnose", "model":
	default:
		return false, nil
	}
	if args[0] == "settings" && len(args) == 2 && (args[1] == "--help" || args[1] == "-h" || args[1] == "help") {
		printSettingsUsage()
		return true, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	client, err := ipc.Dial(ctx, ipcSocketPath())
	cancel()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			if os.Getenv("JABRIDGE_SOCKET") != "" {
				return true, err
			}
			return false, nil
		}
		return true, err
	}
	defer func() { _ = client.Close() }()
	var out bytes.Buffer
	err = runServiceCLI(client, args, &out)
	if out.Len() > 0 {
		if _, writeErr := os.Stdout.Write(out.Bytes()); writeErr != nil {
			return true, writeErr
		}
	}
	return true, err
}

func runServiceCLI(client *ipc.Client, args []string, out *bytes.Buffer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if args[0] == "settings" && len(args) == 4 && args[1] == "set" {
		scope, key, err := parseSettingSelector(args[2])
		if err != nil {
			return err
		}
		var setting ipc.SettingInfo
		if err := client.Call(ctx, "settings.set", map[string]string{"device": settingScopeName(scope), "key": key, "value": args[3]}, &setting); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s = %s (read back from device)\n", args[2], setting.Value)
		if setting.Help != "" {
			fmt.Fprintln(out, setting.Help)
		}
		return nil
	}
	if len(args) > 1 && (args[0] != "settings" || len(args) != 2 || args[1] != "list") {
		return fmt.Errorf("invalid arguments; run jabridge --help")
	}
	var devices []ipc.DeviceInfo
	if err := client.Call(ctx, "devices.list", nil, &devices); err != nil {
		return err
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
	switch args[0] {
	case "status":
		fmt.Fprintf(out, "Jabridge %s\n%d detected Jabra device(s):\n", buildinfo.Version, len(devices))
		for _, d := range devices {
			kind := "Headset"
			if d.IsDongle {
				kind = "Dongle"
			}
			headsetReady, controllerReady := false, false
			for _, part := range d.Parts {
				headsetReady = headsetReady || part.Role == "headset" && part.Ready
				controllerReady = controllerReady || part.Role == "controller" && part.Ready
			}
			if controllerReady && !headsetReady {
				kind = "Link Call Control"
			}
			connection := "USB"
			if d.Connection == "dongle" {
				connection = "through dongle"
			}
			fmt.Fprintf(out, "\n%s: %s\n  ID:         0b0e:%04x\n  Connection: %s\n", kind, d.Name, d.PID, connection)
			if d.Variant != "" {
				fmt.Fprintln(out, "  Variant:   ", d.Variant)
			}
			if d.Battery != nil {
				fmt.Fprintln(out, "  "+formatBatteryLine("Battery", batteryStatusFromIPC(d.Battery)))
			}
			version := d.Firmware
			if version == "" {
				version = "unavailable"
			}
			fmt.Fprintln(out, "  Firmware:  ", version)
			for _, part := range d.Parts {
				state := "not detected"
				if part.Ready {
					state = "ready"
				}
				fmt.Fprintf(out, "  %s: %s\n", part.Role, state)
			}
		}
	case "battery":
		name := ""
		for _, d := range devices {
			if d.Selected && !d.IsDongle {
				name = d.Name
				if name == "" {
					name = "Headset"
				}
				break
			}
		}
		if name == "" {
			fmt.Fprintln(out, "No headset connected.")
			return nil
		}
		var battery *ipc.BatteryInfo
		if err := client.Call(ctx, "device.battery", nil, &battery); err != nil {
			return err
		}
		fmt.Fprintln(out, formatBatteryLine(name, batteryStatusFromIPC(battery)))
	case "settings":
		found := false
		for _, d := range devices {
			if !d.Selected {
				continue
			}
			found = true
			scope := "headset"
			if d.IsDongle {
				scope = "dongle"
			}
			var settings []ipc.SettingInfo
			if err := client.Call(ctx, "settings.list", map[string]string{"device": scope}, &settings); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s: %s\n", scope, d.Name)
			if len(settings) == 0 {
				fmt.Fprintln(out, "  Settings unavailable; run jabridge debug.")
			}
			for _, s := range settings {
				prefix := scope
				if s.Component == "controller" {
					prefix = "controller"
				}
				mode := "read only"
				if s.Editable {
					mode = "editable"
				}
				fmt.Fprintf(out, "  %s.%s = %s (%s", prefix, s.Key, s.Value, mode)
				if len(s.Choices) > 0 {
					fmt.Fprintf(out, "; choices: %s", strings.Join(s.Choices, ", "))
				}
				fmt.Fprintln(out, ")")
				if s.Help != "" {
					fmt.Fprintln(out, "    "+s.Help)
				}
			}
		}
		if !found {
			return errors.New("no selected Jabra device found")
		}
	case "diagnose":
		for _, d := range devices {
			fmt.Fprintf(out, "%s (0b0e:%04x)\n", d.Name, d.PID)
			var checks []ipc.DiagnosticCheck
			if err := client.Call(ctx, "diagnostics.device", map[string]uint16{"id": d.ID}, &checks); err != nil {
				return err
			}
			for _, check := range checks {
				fmt.Fprintf(out, "  %s %s: %s\n", check.State, check.Feature, check.Detail)
			}
		}
		fmt.Fprintln(out, "No device was changed. Device reads were performed by the service.")
	case "model":
		for _, d := range devices {
			fmt.Fprintf(out, "%s (0b0e:%04x)\n", d.Name, d.PID)
			capabilities, err := deviceModelClient.Lookup(ctx, d.PID, d.Variant, d.Firmware)
			if err != nil {
				fmt.Fprintln(out, "  Catalog unavailable:", err)
				continue
			}
			fmt.Fprintf(out, "  Model: %s\n  Variant: %s\n  Profile: %s; %d properties (catalog metadata)\n", capabilities.ProductName, capabilities.Variant, capabilities.Firmware, len(capabilities.Properties))
		}
	}
	return nil
}
