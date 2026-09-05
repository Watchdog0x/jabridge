package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
	firmware := device.firmwareVersion
	if firmware == "" {
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
		kind := deviceKindLabel(device)
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
		if capabilities.DeviceType != "" {
			fmt.Printf("  Type:     %s\n", readableDeviceType(capabilities.DeviceType))
		}
		profileLabel := capabilities.Firmware
		if capabilities.DeviceFirmware == "" {
			profileLabel += " (catalog only; installed firmware unavailable)"
		}
		if capabilities.DeviceFirmware != "" && !capabilities.ExactFirmwareProfile {
			profileLabel += fmt.Sprintf(" (newest populated profile; device %s)", capabilities.DeviceFirmware)
		}
		fmt.Printf("  Profile:  firmware %s, %d SDK properties\n", profileLabel, len(capabilities.Properties))
		if capabilities.FirmwareProtocolKnown {
			fmt.Printf("  FWU:      protocol %d\n", capabilities.FirmwareProtocol)
		} else {
			fmt.Println("  FWU:      model-specific")
		}
	}
	return nil
}

type modelGroup struct {
	Name                     string
	DeviceType               string
	Profiles                 int
	PIDs                     map[uint16]struct{}
	FirmwareProtocols        map[int]struct{}
	HasUnspecifiedFWProtocol bool
}

func runModels(args []string) error {
	filter := strings.TrimSpace(strings.Join(args, " "))
	if filter == "--help" || filter == "-h" || filter == "help" {
		fmt.Println(`Usage:
  jabridge models              summarize the live Jabra model catalog
  jabridge models FILTER       list matching model groups
  jabridge models --all        list every model group

A catalog match means Jabra publishes model metadata. It does not mean that
the hardware has passed a Jabridge test.`)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	inventory, err := deviceModelClient.List(ctx)
	if err != nil {
		return err
	}

	if filter == "" {
		printModelCatalogSummary(inventory)
		return nil
	}
	if filter == "--all" || strings.EqualFold(filter, "all") {
		filter = ""
	}
	groups := matchingModelGroups(inventory.Products, filter)
	if len(groups) == 0 {
		return fmt.Errorf("no model matches %q", filter)
	}
	for _, group := range groups {
		fmt.Println(group.Name)
		fmt.Printf("  Type: %s\n", readableDeviceType(group.DeviceType))
		fmt.Printf("  USB:  %s\n", formatModelPIDs(group.PIDs))
		fmt.Printf("  FWU:  %s\n", formatFirmwareProtocols(group.FirmwareProtocols, group.HasUnspecifiedFWProtocol))
	}
	fmt.Println("Catalog entries are recognition candidates, not hardware-qualified support.")
	return nil
}

func printModelCatalogSummary(inventory *modelcatalog.Inventory) {
	if inventory == nil {
		return
	}
	protocols := make(map[int]struct{}, len(inventory.FirmwareProtocols))
	for _, protocol := range inventory.FirmwareProtocols {
		protocols[protocol] = struct{}{}
	}
	fmt.Println("Live Jabra model catalog")
	fmt.Printf("  Product profiles: %d\n", inventory.JabraProductProfiles)
	fmt.Printf("  Product groups:   %d\n", inventory.ProductGroups)
	fmt.Printf("  USB product IDs:  %d\n", inventory.USBProductIDs)
	fmt.Printf("  Variant records:  %d\n", inventory.Variants)
	fmt.Printf("  Firmware types:   %s\n", formatFirmwareProtocols(protocols, inventory.HasUnspecifiedFirmwareProtocol))
	if inventory.PartnerProductProfiles > 0 {
		fmt.Printf("  Partner profiles: %d (not Jabra USB vendor ID)\n", inventory.PartnerProductProfiles)
	}

	families := summarizeModelFamilies(inventory.Products)
	fmt.Println("Families:")
	for _, family := range families {
		fmt.Printf("  %-10s %3d model groups, %3d USB IDs\n", family.Name, family.Profiles, len(family.PIDs))
	}
	fmt.Println("Hardware-qualified in Jabridge: Link 380 0b0e:24c7 only.")
	fmt.Println("Use `jabridge models Evolve2` (or another name) for exact IDs and firmware types.")
}

func matchingModelGroups(products []modelcatalog.Product, filter string) []modelGroup {
	groups := make(map[string]*modelGroup)
	needle := strings.ToLower(filter)
	for _, product := range products {
		name := product.ProductGroupName
		if name == "" {
			name = product.ProductName
		}
		if needle != "" && !strings.Contains(strings.ToLower(name+" "+product.ProductName), needle) {
			continue
		}
		group := groups[name]
		if group == nil {
			group = &modelGroup{Name: name, DeviceType: product.DeviceType, PIDs: map[uint16]struct{}{}, FirmwareProtocols: map[int]struct{}{}}
			groups[name] = group
		}
		group.Profiles++
		if group.DeviceType == "" {
			group.DeviceType = product.DeviceType
		}
		for _, variant := range product.Variants {
			group.PIDs[variant.PID] = struct{}{}
			if variant.FirmwareProtocolKnown {
				group.FirmwareProtocols[variant.FirmwareProtocol] = struct{}{}
			} else {
				group.HasUnspecifiedFWProtocol = true
			}
		}
	}
	result := make([]modelGroup, 0, len(groups))
	for _, group := range groups {
		result = append(result, *group)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func summarizeModelFamilies(products []modelcatalog.Product) []modelGroup {
	groups := matchingModelGroups(products, "")
	families := make(map[string]*modelGroup)
	for _, group := range groups {
		name := modelFamily(group.Name)
		family := families[name]
		if family == nil {
			family = &modelGroup{Name: name, PIDs: map[uint16]struct{}{}, FirmwareProtocols: map[int]struct{}{}}
			families[name] = family
		}
		family.Profiles++
		for pid := range group.PIDs {
			family.PIDs[pid] = struct{}{}
		}
	}
	result := make([]modelGroup, 0, len(families))
	for _, family := range families {
		result = append(result, *family)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func modelFamily(name string) string {
	name = strings.TrimSpace(strings.TrimPrefix(name, "Jabra "))
	if strings.HasPrefix(name, "UC Voice") {
		return "UC Voice"
	}
	fields := strings.Fields(name)
	if len(fields) == 0 {
		return "Other"
	}
	return fields[0]
}

func readableDeviceType(value string) string {
	switch value {
	case "OverTheEar":
		return "headset"
	case "TrueWireless":
		return "true-wireless headset"
	case "Speaker":
		return "speakerphone"
	case "Video":
		return "camera or video bar"
	case "TouchController":
		return "touch controller"
	case "RoomScheduler":
		return "room scheduler"
	case "":
		return "adapter or model-specific device"
	default:
		return value
	}
}

func formatModelPIDs(pids map[uint16]struct{}) string {
	values := make([]int, 0, len(pids))
	for pid := range pids {
		values = append(values, int(pid))
	}
	sort.Ints(values)
	parts := make([]string, 0, len(values))
	for _, pid := range values {
		parts = append(parts, fmt.Sprintf("0b0e:%04x", pid))
	}
	return strings.Join(parts, ", ")
}

func formatFirmwareProtocols(protocols map[int]struct{}, unspecified bool) string {
	values := make([]int, 0, len(protocols))
	for protocol := range protocols {
		values = append(values, protocol)
	}
	sort.Ints(values)
	parts := make([]string, 0, len(values)+1)
	for _, protocol := range values {
		parts = append(parts, fmt.Sprintf("%d", protocol))
	}
	if unspecified {
		parts = append(parts, "model-specific")
	}
	if len(parts) == 0 {
		return "model-specific"
	}
	return strings.Join(parts, ", ")
}
