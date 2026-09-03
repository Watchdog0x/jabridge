package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/Watchdog0x/jabridge/internal/buildinfo"
	shellcompletion "github.com/Watchdog0x/jabridge/internal/completion"
	"github.com/Watchdog0x/jabridge/internal/firmware"
	"github.com/Watchdog0x/jabridge/internal/selfupdate"
)

func runUpdate(args []string) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	checkOnly := flags.Bool("check", false, "check without installing")
	prerelease := flags.Bool("prerelease", false, "include test releases")
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			printUpdateUsage()
			return nil
		}
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected update argument %q", flags.Arg(0))
	}

	client := selfupdate.NewClient()
	plan, err := client.Check(context.Background(), buildinfo.Version, *prerelease)
	if err != nil {
		return err
	}
	if !plan.NewerThanCurrent {
		fmt.Printf("Jabridge %s is already up to date.\n", buildinfo.Version)
		return nil
	}

	fmt.Printf("Jabridge %s is available: %s\n", plan.Version, plan.ReleaseURL)
	if *checkOnly {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find running executable: %w", err)
	}
	fmt.Printf("Downloading and verifying %s...\n", plan.ArchiveName)
	if err := client.Install(context.Background(), plan, executable); err != nil {
		return err
	}
	fmt.Printf("Updated Jabridge to %s. Restart any running service.\n", plan.Version)
	return nil
}

func runStatus() error {
	scanAndAttachDevices()
	refreshDongleChildDevice()
	devices := deviceSnapshots()
	fmt.Printf("Jabridge %s\n", buildinfo.Version)
	if len(devices) == 0 {
		fmt.Println("No supported Jabra device found.")
		return nil
	}
	fmt.Printf("%d supported device(s):\n", len(devices))
	ids := make([]int, 0, len(devices))
	for id := range devices {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		device := devices[id]
		name := device.deviceName
		if name == "" {
			name = "Unknown device"
		}
		kind := "Headset"
		if device.isDongle {
			kind = "Dongle"
		}
		fmt.Printf("\n%s: %s\n", kind, name)
		fmt.Printf("  ID:         %04x:%04x\n", device.vendorID, device.productID)
		connection := "USB"
		if device.deviceConnection == deviceConnectionType_BT {
			connection = "through dongle"
		}
		fmt.Printf("  Connection: %s\n", connection)
		if device.batteryStatus != nil {
			charging := ""
			if device.batteryStatus.charging {
				charging = " (charging)"
			}
			fmt.Printf("  Battery:    %d%%%s\n", device.batteryStatus.levelInPercent, charging)
		}
		if device.hidrawPath != "" {
			version, err := readFirmwareVersion(device)
			if err != nil {
				fmt.Printf("  Firmware:   unavailable (%v)\n", err)
			} else {
				fmt.Printf("  Firmware:   %s\n", version)
			}
		}
	}
	return nil
}

func printUpdateUsage() {
	fmt.Println(`Usage:
  jabridge update                 install the latest stable app release
  jabridge update --check         check without changing files
  jabridge update --prerelease    include hardware-test releases

This updates only the Jabridge application. It never updates a headset or
dongle.`)
}

func runCompletion(args []string) error {
	if len(args) != 1 || args[0] != "bash" {
		return errors.New("usage: jabridge completion bash")
	}
	fmt.Print(shellcompletion.JabridgeBash)
	return nil
}

func runFirmware(args []string) error {
	return firmware.Run(args)
}
