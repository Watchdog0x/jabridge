package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModelSettingMetadataPreservesChoicesAndUnknownRestart(t *testing.T) {
	var document any
	err := json.Unmarshal([]byte(`{"groupName":"Audio","settings":[{"settingId":"NOISE_CONTROL","type":"Enum","sdkProperties":["ancAmbienceMode"],"helpText":"Choose listening mode","settingAccess":"ReadOnly","requiresRestart":false,"possibleValues":[{"value":"off"},{"value":"anc"}]},{"settingId":"NAME","type":"String","sdkProperties":["bluetoothName"]}]}`), &document)
	if err != nil {
		t.Fatal(err)
	}
	properties := map[string]Property{}
	collectProperties(document, properties)
	mode := properties["ancAmbienceMode"]
	if mode.SettingID != "NOISE_CONTROL" || mode.Group != "Audio" || mode.Access != "ReadOnly" || !mode.RestartKnown || mode.RequiresRestart || len(mode.PossibleValues) != 2 {
		t.Fatal(mode)
	}
	if properties["bluetoothName"].RestartKnown {
		t.Fatal("missing restart information treated as false")
	}
}

func TestLookupReturnsExactVariantProperties(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/bundles.json":
			_, _ = fmt.Fprint(writer, `{"bundles":[],"unbundledProducts":[{"productName":"Test Headset","productGroupName":"Test Family","deviceType":"OverTheEar","firmwareDowngradeAllowed":true,"supportedProtocols":["GNP"],"variants":[{"vendorId":2830,"productId":4660,"variantType":"01-02","name":"Test Headset UC","fwuProtocolId":7}],"firmwareReleases":[{"version":"1.2.0","revoked":false},{"version":"1.10.0","revoked":false}]}]}`)
		case "/models/vendors/2830/products/4660/variants/01-02/firmware-versions/1.10.0/device-models/Jabra SDK V4/schema-versions/1.10.0.json":
			_, _ = fmt.Fprint(writer, `{"device":{"settings":[{"settingAccess":"ReadWrite","requiresRestart":false,"sdkProperties":["sidetoneEnabled"],"possibleValues":[{"value":true},{"value":false}]}]}}`)
		case "/models/vendors/2830/products/4660/variants/01-02/firmware-versions/1.2.0/device-models/Jabra SDK V4/schema-versions/1.10.0.json":
			_, _ = fmt.Fprint(writer, `{"device":{"settings":[]}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := &Client{
		HTTPClient: server.Client(), BundlesURL: server.URL + "/bundles.json",
		ModelsBaseURL: server.URL + "/models", ModelName: "Jabra SDK V4", SchemaVersion: "1.10.0",
	}
	capabilities, err := client.Lookup(context.Background(), 0x1234, "01-02", "")
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.ProductName != "Test Headset" || capabilities.Firmware != "1.10.0" {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if capabilities.FirmwareProtocol != 7 {
		t.Fatalf("firmware protocol = %d, want 7", capabilities.FirmwareProtocol)
	}
	if !capabilities.FirmwareProtocolKnown || !capabilities.FirmwareDowngradeAllowed {
		t.Fatalf("catalog metadata missing: %#v", capabilities)
	}
	if capabilities.ProductGroupName != "Test Family" || capabilities.DeviceType != "OverTheEar" {
		t.Fatalf("model identity = %#v", capabilities)
	}
	property, exists := capabilities.Properties["sidetoneEnabled"]
	if !exists || property.Access != "ReadWrite" || len(property.PossibleValues) != 2 {
		t.Fatalf("property = %#v, exists=%v", property, exists)
	}
	withoutVariant, err := client.Lookup(context.Background(), 0x1234, "", "")
	if err != nil || withoutVariant.Variant != "01-02" {
		t.Fatalf("unambiguous PID fallback = %#v, %v", withoutVariant, err)
	}
	fallback, err := client.Lookup(context.Background(), 0x1234, "01-02", "1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Firmware != "1.10.0" || fallback.DeviceFirmware != "1.2.0" || fallback.ExactFirmwareProfile {
		t.Fatalf("populated profile fallback = %#v", fallback)
	}
}

func TestLookupFindsNewestPopulatedProfileAcrossOlderReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/bundles.json":
			_, _ = fmt.Fprint(writer, `{"bundles":[],"unbundledProducts":[{"productName":"Test Headset","variants":[{"vendorId":2830,"productId":4660,"variantType":"01-02"}],"firmwareReleases":[{"version":"3.0.0"},{"version":"2.0.0"},{"version":"1.0.0"}]}]}`)
		case "/models/vendors/2830/products/4660/variants/01-02/firmware-versions/3.0.0/device-models/Jabra SDK V4/schema-versions/1.10.0.json",
			"/models/vendors/2830/products/4660/variants/01-02/firmware-versions/2.0.0/device-models/Jabra SDK V4/schema-versions/1.10.0.json":
			_, _ = fmt.Fprint(writer, `{"device":{"settings":[]}}`)
		case "/models/vendors/2830/products/4660/variants/01-02/firmware-versions/1.0.0/device-models/Jabra SDK V4/schema-versions/1.10.0.json":
			_, _ = fmt.Fprint(writer, `{"device":{"settings":[{"sdkProperty":"voicePrompts","possibleValues":[{"value":"voice"}]}]}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := &Client{
		HTTPClient: server.Client(), BundlesURL: server.URL + "/bundles.json",
		ModelsBaseURL: server.URL + "/models", ModelName: "Jabra SDK V4", SchemaVersion: "1.10.0",
	}
	capabilities, err := client.Lookup(context.Background(), 0x1234, "01-02", "2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.Firmware != "1.0.0" || capabilities.DeviceFirmware != "2.0.0" || capabilities.ExactFirmwareProfile {
		t.Fatalf("older populated fallback = %#v", capabilities)
	}
	if _, exists := capabilities.Properties["voicePrompts"]; !exists {
		t.Fatalf("fallback properties = %#v", capabilities.Properties)
	}
}

func TestListSummarizesJabraModelsAndExcludesPartnerUSB(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(writer, `{
			"bundles":[{"products":[
				{"productName":"Jabra One","productGroupName":"Jabra One","deviceType":"Speaker","variants":[
					{"vendorId":2830,"productId":4660,"variantType":"01-02","fwuProtocolId":7},
					{"vendorId":2830,"productId":4661,"variantType":"01-03","fwuProtocolId":4}
				]}
			]}],
			"unbundledProducts":[
				{"productName":"Jabra One Teams","productGroupName":"Jabra One","variants":[{"vendorId":2830,"productId":4660,"variantType":"01-04"}]},
				{"productName":"Partner Camera","variants":[{"vendorId":1234,"productId":99,"variantType":""}]}
			]
		}`)
	}))
	defer server.Close()

	client := &Client{HTTPClient: server.Client(), BundlesURL: server.URL}
	inventory, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inventory.AllProductProfiles != 3 || inventory.JabraProductProfiles != 2 || inventory.PartnerProductProfiles != 1 {
		t.Fatalf("profile counts = %#v", inventory)
	}
	if inventory.ProductGroups != 1 || inventory.Variants != 3 || inventory.USBProductIDs != 2 {
		t.Fatalf("catalog counts = %#v", inventory)
	}
	if len(inventory.FirmwareProtocols) != 2 || inventory.FirmwareProtocols[0] != 4 || inventory.FirmwareProtocols[1] != 7 {
		t.Fatalf("firmware protocols = %#v", inventory.FirmwareProtocols)
	}
	if !inventory.HasUnspecifiedFirmwareProtocol {
		t.Fatal("missing firmware protocol was not recorded")
	}
}

func TestFirmwareReleaseUsesChecksumForSiblingPIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(writer, `{"bundles":[],"unbundledProducts":[
			{"productName":"Adapter","variants":[
				{"vendorId":2830,"productId":9415,"variantType":"04-0B","fwuProtocolId":7},
				{"vendorId":2830,"productId":9416,"variantType":"04-0B","fwuProtocolId":7}
			],"firmwareReleases":[
				{"version":"1.16.0","md5Checksum":"AQIDBA==","revoked":false},
				{"version":"1.15.0","md5Checksum":"revoked","revoked":true}
			]}
		]}`)
	}))
	defer server.Close()

	client := &Client{HTTPClient: server.Client(), BundlesURL: server.URL}
	evidence, err := client.FirmwareRelease(context.Background(), 0x24c8, "1.16.0")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.MD5Checksum != "AQIDBA==" || len(evidence.CompatiblePIDs) != 2 {
		t.Fatalf("evidence = %#v", evidence)
	}
	if evidence.CompatiblePIDs[0] != 0x24c7 || evidence.CompatiblePIDs[1] != 0x24c8 {
		t.Fatalf("compatible PIDs = %#v", evidence.CompatiblePIDs)
	}
	if len(evidence.FirmwareProtocols) != 1 || evidence.FirmwareProtocols[0] != 7 {
		t.Fatalf("protocols = %#v", evidence.FirmwareProtocols)
	}
	if _, err := client.FirmwareRelease(context.Background(), 0x24c8, "1.15.0"); err == nil {
		t.Fatal("revoked release was accepted")
	}
}

func TestLookupRejectsUnknownPID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = fmt.Fprint(writer, `{"bundles":[],"unbundledProducts":[]}`)
	}))
	defer server.Close()
	client := &Client{HTTPClient: server.Client(), BundlesURL: server.URL, ModelsBaseURL: server.URL}
	if _, err := client.Lookup(context.Background(), 0xffff, "", ""); err == nil {
		t.Fatal("unknown PID was accepted")
	}
}

func TestLookupWithoutVariantRequiresOneUnambiguousVariant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/bundles.json":
			_, _ = fmt.Fprint(writer, `{"bundles":[],"unbundledProducts":[{"productName":"Test","variants":[
				{"vendorId":2830,"productId":4660,"variantType":"01-01"},
				{"vendorId":2830,"productId":4660,"variantType":"01-02"}
			],"firmwareReleases":[{"version":"1.0.0","revoked":false}]}]}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := &Client{HTTPClient: server.Client(), BundlesURL: server.URL, ModelsBaseURL: server.URL}
	if _, err := client.Lookup(context.Background(), 0x1234, "", ""); err == nil {
		t.Fatal("ambiguous PID was accepted without a variant")
	}
}
