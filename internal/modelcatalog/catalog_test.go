package modelcatalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLookupReturnsExactVariantProperties(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/bundles.json":
			_, _ = fmt.Fprint(writer, `{"bundles":[],"unbundledProducts":[{"productName":"Test Headset","variants":[{"vendorId":2830,"productId":4660,"variantType":"01-02"}],"firmwareReleases":[{"version":"1.2.0","revoked":false},{"version":"1.10.0","revoked":false}]}]}`)
		case "/models/vendors/2830/products/4660/variants/01-02/firmware-versions/1.10.0/device-models/Jabra SDK V4/schema-versions/1.10.0.json":
			_, _ = fmt.Fprint(writer, `{"device":{"settings":[{"settingAccess":"ReadWrite","requiresRestart":false,"sdkProperties":["sidetoneEnabled"],"possibleValues":[{"value":true},{"value":false}]}]}}`)
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
	property, exists := capabilities.Properties["sidetoneEnabled"]
	if !exists || property.Access != "ReadWrite" || len(property.PossibleValues) != 2 {
		t.Fatalf("property = %#v, exists=%v", property, exists)
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
