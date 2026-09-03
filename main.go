package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Watchdog0x/jabridge/daemon"
	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/buildinfo"
)

func main() {
	if len(os.Args) == 1 {
		if err := runTUI(); err != nil {
			fmt.Fprintf(os.Stderr, "jabridge: %v\n", err)
			os.Exit(1)
		}
		return
	}

	var err error
	switch os.Args[1] {
	case "--help", "-h", "help":
		printUsage()
	case "--version", "-v", "version":
		fmt.Printf("%s %s\n", buildinfo.Name, buildinfo.Version)
	case "status":
		err = runStatus()
	case "--daemon", "-d", "daemon":
		err = runDaemon()
	case "update":
		err = runUpdate(os.Args[2:])
	case "firmware", "fw":
		err = runFirmware(os.Args[2:])
	case "completion":
		err = runCompletion(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q; run jabridge --help", os.Args[1])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "jabridge: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`jabridge — Linux control for supported Jabra devices

Usage:
  jabridge             open the terminal UI; talks directly to the device
  jabridge status      show connected USB devices
  jabridge update      update the app
  jabridge firmware    check or download device firmware
  jabridge --help      show help

More:
  jabridge --daemon           run the local service
  jabridge completion bash    print Bash completion

The TUI does not use the service yet. Run only one at a time.`)
}

func runDaemon() error {
	cfg := daemon.DefaultConfig()
	cfg.BusylightSender = &jabraBusylightSender{}
	return daemon.Start(cfg, pollDevices, &jabraAPIBridge{})
}

func runTUI() error {
	oldSettings, err := enableRawMode()
	if err != nil {
		return fmt.Errorf("enable raw mode: %w", err)
	}
	defer restoreTerminal(oldSettings)

	pollContext, stopPoll := context.WithCancel(context.Background())
	defer stopPoll()
	updateStartMenu()
	go pollDevices(pollContext)

	fmt.Print("\x1b[?1049h\x1b[?25l\x1b[0;40;97m")
	defer fmt.Print("\x1b[0m\x1b[2J\x1b[H\x1b[?25h\x1b[?1049l")

	clearScreen()
	startUi(pollContext)
	return nil
}

// jabraAPIBridge adapts the jabraApi.go global functions to the ipc.API interface.
type jabraAPIBridge struct{}

func (j *jabraAPIBridge) ListDevices() []ipc.DeviceInfo {
	var out []ipc.DeviceInfo
	for _, dev := range deviceSnapshots() {
		d := ipc.DeviceInfo{
			Name:     dev.deviceName,
			PID:      dev.productID,
			Serial:   "",
			IsDongle: dev.isDongle,
			Firmware: getFirmwareVersion(dev.deviceID),
		}
		if dev.batteryStatus != nil {
			d.Battery = &ipc.BatteryInfo{
				Level:     dev.batteryStatus.levelInPercent,
				Charging:  dev.batteryStatus.charging,
				Low:       dev.batteryStatus.batteryLow,
				Component: int(dev.batteryStatus.component),
			}
		}
		out = append(out, d)
	}
	return out
}

func (j *jabraAPIBridge) GetBattery() (*ipc.BatteryInfo, error) {
	headset, exists := selectedHeadsetSnapshot()
	if !exists {
		return nil, fmt.Errorf("no headset found")
	}
	bs, err := getBatteryStatus(headset.deviceID)
	if err != nil {
		return nil, err
	}
	return &ipc.BatteryInfo{
		Level:     bs.levelInPercent,
		Charging:  bs.charging,
		Low:       bs.batteryLow,
		Component: int(bs.component),
	}, nil
}

func (j *jabraAPIBridge) GetFirmware() string {
	if headset, exists := selectedHeadsetSnapshot(); exists && headset.hidrawPath != "" {
		return getFirmwareVersion(headset.deviceID)
	}
	if dongle, exists := selectedDongleSnapshot(); exists {
		return getFirmwareVersion(dongle.deviceID)
	}
	return ""
}

func (j *jabraAPIBridge) GetFeatures() ipc.FeatureInfo {
	var ff *featureFlags
	if dev := deviceForID(0); dev != nil && dev.featureFlags != nil {
		ff = dev.featureFlags
	} else {
		ff = &featureFlags{}
	}
	return ipc.FeatureInfo{
		BusyLight:    ff.busyLight,
		FactoryReset: ff.factoryReset,
		PairingList:  ff.pairingList,
		RemoteMMI:    ff.remoteMMI,
		MusicEQ:      ff.musicEqualizer,
		OnHeadDetect: ff.onHeadDetection,
	}
}

func (j *jabraAPIBridge) GetPairingList() []ipc.PairedDeviceInfo {
	dongle, exists := selectedDongleSnapshot()
	if !exists || dongle.pairingList == nil {
		return nil
	}
	var out []ipc.PairedDeviceInfo
	for _, pd := range dongle.pairingList.pairedDevices {
		out = append(out, ipc.PairedDeviceInfo{
			Name:      pd.deviceName,
			Addr:      "",
			Connected: pd.isConnected,
		})
	}
	return out
}

func (j *jabraAPIBridge) SearchNewDevices() error       { return searchForNewDevices() }
func (j *jabraAPIBridge) SetBTPairing(e bool) error     { return setDongleInBTPairing(e) }
func (j *jabraAPIBridge) GetAutoPairing() (bool, error) { return getAutoPairing() }
func (j *jabraAPIBridge) SetAutoPairing(e bool) error   { return setAutoPairing(e) }
func (j *jabraAPIBridge) FactoryReset() error {
	dongle, exists := selectedDongleSnapshot()
	if !exists {
		return fmt.Errorf("no dongle found")
	}
	return factoryReset(dongle.deviceID)
}
func (j *jabraAPIBridge) SetBusylightMode(mode string) error { return nil } // wired in daemon
func (j *jabraAPIBridge) GetBusylightMode() string           { return "auto" }
