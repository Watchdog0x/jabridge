package main

import (
	"testing"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func TestMatchingModelGroupsCombinesProfilesAndFormatsHexPIDs(t *testing.T) {
	protocolSeven := modelcatalog.Variant{PID: 0x24c7, FirmwareProtocol: 7, FirmwareProtocolKnown: true}
	protocolUnknown := modelcatalog.Variant{PID: 0x24c8}
	products := []modelcatalog.Product{
		{ProductName: "Jabra Link 380a", ProductGroupName: "Jabra Link 380", Variants: []modelcatalog.Variant{protocolSeven}},
		{ProductName: "Jabra Link 380c", ProductGroupName: "Jabra Link 380", Variants: []modelcatalog.Variant{protocolUnknown}},
		{ProductName: "Jabra Speak2 75", ProductGroupName: "Jabra Speak2 75", Variants: []modelcatalog.Variant{{PID: 0x24ef, FirmwareProtocol: 7, FirmwareProtocolKnown: true}}},
	}

	groups := matchingModelGroups(products, "link")
	if len(groups) != 1 {
		t.Fatalf("groups = %#v", groups)
	}
	group := groups[0]
	if group.Profiles != 2 || !group.HasUnspecifiedFWProtocol {
		t.Fatalf("group = %#v", group)
	}
	if got := formatModelPIDs(group.PIDs); got != "0b0e:24c7, 0b0e:24c8" {
		t.Fatalf("PIDs = %q", got)
	}
	if got := formatFirmwareProtocols(group.FirmwareProtocols, group.HasUnspecifiedFWProtocol); got != "7, model-specific" {
		t.Fatalf("firmware protocols = %q", got)
	}
}

func TestReadableDeviceType(t *testing.T) {
	if got := readableDeviceType("Video"); got != "camera or video bar" {
		t.Fatalf("type = %q", got)
	}
	if got := readableDeviceType(""); got != "adapter or model-specific device" {
		t.Fatalf("empty type = %q", got)
	}
}
