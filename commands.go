package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

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
	devices, err := enumerateJabraUSB()
	if err != nil {
		return fmt.Errorf("scan USB devices: %w", err)
	}
	filtered := devices[:0]
	for _, device := range devices {
		if !isAccessoryName(device.product) {
			filtered = append(filtered, device)
		}
	}
	devices = filtered
	fmt.Printf("Jabridge %s\n", buildinfo.Version)
	if len(devices) == 0 {
		fmt.Println("No supported Jabra USB device found.")
		return nil
	}
	fmt.Printf("%d supported USB device(s):\n", len(devices))
	for _, usb := range devices {
		name := usb.product
		if name == "" {
			name = "Unknown device"
		}
		kind := "Headset"
		if isKnownDonglePID(usb.productID) {
			kind = "Dongle"
		}
		fmt.Printf("\n%s: %s\n", kind, name)
		fmt.Printf("  USB:      %04x:%04x\n", usb.vendorID, usb.productID)
		device := &jabra_DeviceInfo{
			productID:  usb.productID,
			vendorID:   usb.vendorID,
			deviceName: name,
			isDongle:   isKnownDonglePID(usb.productID),
			hidrawPath: findHidrawForPID(usb.vendorID, usb.productID),
		}
		if device.hidrawPath == "" {
			fmt.Println("  Control:  unavailable (no matching hidraw device)")
			continue
		}
		version, err := readFirmwareVersion(device)
		if err != nil {
			fmt.Printf("  Firmware: unavailable (%v)\n", err)
			continue
		}
		fmt.Printf("  Firmware: %s\n", version)
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
