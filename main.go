package main

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/Watchdog0x/jabridge/daemon"
	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/buildinfo"
)

func main() {
	if len(os.Args) == 1 {
		if err := offerDeviceAccessSetup(); err != nil {
			fmt.Fprintf(os.Stderr, "jabridge: %v\n", err)
			os.Exit(1)
		}
		if err := runTUI(); err != nil {
			fmt.Fprintf(os.Stderr, "jabridge: %v\n", err)
			os.Exit(1)
		}
		return
	}

	var err error
	resumeService := func() error { return nil }
	if commandNeedsDirectHardware(os.Args[1]) {
		resumeService, err = pauseUserServiceForDirectCommand()
		if err != nil {
			fmt.Fprintf(os.Stderr, "jabridge: %v\n", err)
			os.Exit(1)
		}
	}
	switch os.Args[1] {
	case "--help", "-h", "help":
		printUsage()
	case "--version", "-v", "version":
		fmt.Printf("%s %s\n", buildinfo.Name, buildinfo.Version)
	case "status":
		err = runStatus()
	case "battery":
		err = runBattery()
	case "--daemon", "-d", "daemon":
		err = runDaemon()
	case "update":
		err = runUpdate(os.Args[2:])
	case "firmware", "fw":
		err = runFirmware(os.Args[2:])
	case "settings":
		err = runSettings(os.Args[2:])
	case "model", "models":
		err = runModel()
	case "sound", "audio":
		err = runSound(os.Args[2:])
	case "setup":
		err = runSetup(os.Args[2:])
	case "ipc":
		err = runIPC(os.Args[2:])
	case "service":
		err = runService(os.Args[2:])
	case "completion":
		err = runCompletion(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q; run jabridge --help", os.Args[1])
	}
	if resumeErr := resumeService(); err == nil && resumeErr != nil {
		err = fmt.Errorf("restart background service: %w", resumeErr)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "jabridge: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`jabridge — Linux control for supported Jabra devices

Usage:
  jabridge             open the terminal UI through the background service
  jabridge status      show connected USB devices
  jabridge battery     show headset battery from 0 to 100 percent
  jabridge update      update the app
  jabridge firmware    check or download device firmware
  jabridge settings    list or change supported device settings
  jabridge model       match devices with the online capability catalog
  jabridge sound       show or change Jabra PipeWire sound controls
  jabridge setup       set up one-time Linux device access
  jabridge ipc         use the background-service API
  jabridge service     start, stop, restart, or check the service
  jabridge --help      show help

More:
  jabridge --daemon           run the local service
  jabridge completion bash    print Bash completion

The TUI starts the service when needed and reconnects after service restarts.`)
}

func runDaemon() error {
	cfg := daemon.DefaultConfig()
	cfg.BusylightSender = &jabraBusylightSender{}
	return daemon.Start(cfg, pollDevices, &jabraAPIBridge{})
}

func runTUI() error {
	backend, err := connectTUIService()
	if err != nil {
		return err
	}
	setTUIBackend(backend)
	defer func() {
		setTUIBackend(nil)
		backend.close()
	}()
	if err := initialTUIServiceSync(backend); err != nil {
		return fmt.Errorf("load service state: %w", err)
	}

	oldSettings, err := enableRawMode()
	if err != nil {
		return fmt.Errorf("enable raw mode: %w", err)
	}
	defer restoreTerminal(oldSettings)

	pollContext, stopPoll := context.WithCancel(context.Background())
	defer stopPoll()
	updateStartMenu()
	go runTUIServiceSync(pollContext, backend)

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
		connection := "usb"
		if dev.deviceConnection == deviceConnectionType_BT {
			connection = "dongle"
		}
		d := ipc.DeviceInfo{
			ID: dev.deviceID, Name: dev.deviceName, PID: dev.productID,
			Variant: dev.variantType, Serial: "", IsDongle: dev.isDongle,
			Connection: connection, ParentID: dev.parentDeviceID,
			Firmware: getFirmwareVersion(dev.deviceID),
		}
		if dev.batteryStatus != nil {
			d.Battery = ipcBatteryInfo(dev.batteryStatus)
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
	return ipcBatteryInfo(bs), nil
}

func ipcBatteryInfo(status *batteryStatus) *ipc.BatteryInfo {
	if status == nil {
		return nil
	}
	result := &ipc.BatteryInfo{
		Level:     status.levelInPercent,
		Charging:  status.charging,
		Low:       status.batteryLow,
		Component: int(status.component),
	}
	for _, component := range status.components {
		result.Components = append(result.Components, ipc.BatteryComponentInfo{
			Name:      component.label,
			Level:     component.levelInPercent,
			Charging:  component.charging,
			Component: int(component.component),
		})
	}
	return result
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
	for index, pd := range dongle.pairingList.pairedDevices {
		out = append(out, ipc.PairedDeviceInfo{
			ID:        index,
			Name:      pd.deviceName,
			Addr:      "",
			Connected: pd.isConnected,
		})
	}
	return out
}

func (j *jabraAPIBridge) GetSearchList() []ipc.PairedDeviceInfo {
	dongle, exists := selectedDongleSnapshot()
	if !exists {
		return nil
	}
	list := getSearchDeviceList(dongle.deviceID)
	if list == nil {
		return nil
	}
	result := make([]ipc.PairedDeviceInfo, 0, len(list.pairedDevices))
	for index, device := range list.pairedDevices {
		result = append(result, ipc.PairedDeviceInfo{ID: index, Name: device.deviceName, Connected: device.isConnected})
	}
	return result
}

func (j *jabraAPIBridge) SearchNewDevices() error { return searchForNewDevices() }
func (j *jabraAPIBridge) ConnectSearchDevice(index int) error {
	return connectNewDevice(uint16(index))
}
func (j *jabraAPIBridge) ConnectRememberedDevice(index int) error {
	return connectRememberedDevice(index)
}
func (j *jabraAPIBridge) DisconnectRememberedDevice(index int) error {
	return disconnectRememberedDevice(index)
}
func (j *jabraAPIBridge) ForgetRememberedDevice(index int) error {
	return forgetRememberedDevice(index)
}
func (j *jabraAPIBridge) SetBTPairing(e bool) error     { return setDongleInBTPairing(e) }
func (j *jabraAPIBridge) GetAutoPairing() (bool, error) { return getAutoPairing() }
func (j *jabraAPIBridge) SetAutoPairing(e bool) error   { return setAutoPairing(e) }
func (j *jabraAPIBridge) FactoryReset() error {
	dongle, exists := selectedDongleSnapshot()
	if !exists {
		return fmt.Errorf("no dongle found")
	}
	return factoryResetConfirmed(dongle.deviceID)
}
func (j *jabraAPIBridge) SetBusylightMode(mode string) error { return nil } // wired in daemon
func (j *jabraAPIBridge) GetBusylightMode() string           { return "auto" }

func (j *jabraAPIBridge) ListSettings(deviceName string) ([]ipc.SettingInfo, error) {
	scope, err := ipcSettingScope(deviceName)
	if err != nil {
		return nil, err
	}
	device, exists := selectedSettingsDevice(scope)
	if !exists {
		return nil, fmt.Errorf("no %s connected", deviceName)
	}
	values := readSupportedDeviceSettings(device, scope)
	result := make([]ipc.SettingInfo, 0, len(values))
	for _, value := range values {
		result = append(result, ipcSettingInfo(deviceName, value))
	}
	return result, nil
}

func (j *jabraAPIBridge) SetSetting(deviceName, key, value string) (ipc.SettingInfo, error) {
	scope, err := ipcSettingScope(deviceName)
	if err != nil {
		return ipc.SettingInfo{}, err
	}
	device, exists := selectedSettingsDevice(scope)
	if !exists {
		return ipc.SettingInfo{}, fmt.Errorf("no %s connected", deviceName)
	}
	settings := readSupportedDeviceSettings(device, scope)
	setting, exists := findDeviceSettingValue(settings, key)
	if !exists {
		return ipc.SettingInfo{}, fmt.Errorf("setting %s is not supported", key)
	}
	if _, _, err := applyDeviceSettingFromText(device, setting, value); err != nil {
		return ipc.SettingInfo{}, err
	}
	refreshed := readSupportedDeviceSettings(refreshedSettingsDevice(device), scope)
	updated, exists := findDeviceSettingValue(refreshed, key)
	if !exists {
		return ipc.SettingInfo{}, fmt.Errorf("setting %s disappeared after update", key)
	}
	return ipcSettingInfo(deviceName, updated), nil
}

func (j *jabraAPIBridge) SelectDevice(id uint16) error {
	_, err := selectRegistryDevice(int(id))
	return err
}

func (j *jabraAPIBridge) Shutdown() error {
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
	return nil
}

func ipcSettingScope(deviceName string) (settingScope, error) {
	switch deviceName {
	case "dongle":
		return settingScopeDongle, nil
	case "headset":
		return settingScopeHeadset, nil
	default:
		return 0, fmt.Errorf("device must be dongle or headset")
	}
}

func ipcSettingInfo(deviceName string, setting deviceSettingValue) ipc.SettingInfo {
	info := ipc.SettingInfo{
		Device: deviceName, Key: setting.key(), Label: setting.label(),
		Value: setting.valueName(), Editable: setting.editable(),
	}
	if setting.Boolean != nil {
		info.Choices = []string{"Off", "On"}
	} else if setting.Choice != nil {
		for _, choice := range setting.Choice.Definition.Choices {
			info.Choices = append(info.Choices, choice.Name)
		}
	}
	return info
}
