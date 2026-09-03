package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

type modelCacheEntry struct {
	capabilities *modelcatalog.Capabilities
}

var (
	deviceModelClient = modelcatalog.NewClient()
	deviceModelCache  = map[string]modelCacheEntry{}
	deviceModelMu     sync.Mutex
)

func lookupDeviceModel(device *jabra_DeviceInfo) (*modelcatalog.Capabilities, error) {
	if device == nil {
		return nil, fmt.Errorf("no device")
	}
	if device.variantType == "" {
		return nil, fmt.Errorf("device variant is unavailable")
	}
	firmware := ""
	if device.hidrawPath != "" {
		if version, err := readFirmwareVersion(device); err == nil {
			firmware = version
		}
	}
	key := fmt.Sprintf("%04x:%s:%s", device.productID, device.variantType, firmware)
	deviceModelMu.Lock()
	entry, exists := deviceModelCache[key]
	deviceModelMu.Unlock()
	if exists {
		return entry.capabilities, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	capabilities, err := deviceModelClient.Lookup(ctx, device.productID, device.variantType, firmware)
	if err == nil {
		deviceModelMu.Lock()
		deviceModelCache[key] = modelCacheEntry{capabilities: capabilities}
		deviceModelMu.Unlock()
	}
	return capabilities, err
}

func runModel() error {
	scanAndAttachDevices()
	refreshDongleChildDevice()
	devices := switchableDevices()
	if len(devices) == 0 {
		return fmt.Errorf("no supported Jabra device found")
	}
	for _, item := range devices {
		device := item.Device
		kind := "Headset"
		if device.isDongle {
			kind = "Dongle"
		}
		fmt.Printf("%s: %s\n", kind, device.deviceName)
		fmt.Printf("  USB:      0b0e:%04x\n", device.productID)
		if device.variantType != "" {
			fmt.Printf("  Variant:  %s\n", device.variantType)
		}
		capabilities, err := lookupDeviceModel(device)
		if err != nil {
			fmt.Printf("  Catalog:  unavailable (%v)\n", err)
			continue
		}
		fmt.Printf("  Model:    %s\n", capabilities.ProductName)
		fmt.Printf("  Profile:  firmware %s, %d SDK properties\n", capabilities.Firmware, len(capabilities.Properties))
	}
	return nil
}
