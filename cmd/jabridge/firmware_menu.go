package main

import "github.com/Watchdog0x/jabridge/internal/firmware"

func latestFirmwareForTUI(device *jabra_DeviceInfo) (firmware.LatestInfo, error) {
	return firmware.LatestForPID(device.productID)
}

func downloadFirmwareForTUI(device *jabra_DeviceInfo) (firmware.DownloadResult, error) {
	return firmware.DownloadLatestQuiet(device.productID, "./firmware")
}
