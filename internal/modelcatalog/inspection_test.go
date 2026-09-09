package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInspectionCollectsAllVariantsAndEventsWithoutSelectingOne(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/bundles.json" {
			_, _ = fmt.Fprint(w, `{"unbundledProducts":[{"supportedProtocols":["unknown-future"],"variants":[{"vendorId":2830,"productId":4660,"variantType":"01-01","fwuProtocolId":21},{"vendorId":2830,"productId":4660,"variantType":"01-02","fwuProtocolId":21}],"firmwareReleases":[{"version":"1.0.0"},{"version":"2.0.0","revoked":true}]}]}`)
			return
		}
		if !strings.Contains(r.URL.Path, "/firmware-versions/1.0.0/") {
			t.Error("revoked/latest selection wrong", r.URL.Path)
		}
		if strings.Contains(r.URL.Path, "/01-01/") {
			_, _ = fmt.Fprint(w, `{"device":{"vendorId":2830,"productId":4660,"variantType":"01-01","settings":[{"settingId":"NEW_MODE","type":"Enum","sdkProperties":["newMode"],"possibleValues":[{"value":"off"},{"value":"automatic"},{"value":"on"}],"default":"PRIVATE_DEFAULT","current":"PRIVATE_VALUE"}],"commands":[{"commandId":"RESET","sdkProperty":"factoryReset"}],"attributes":[],"deviceEvents":[{"name":"CustomButton","property":"customButton","type":"bool"}]}}`)
		} else {
			_, _ = fmt.Fprint(w, `{"device":{"vendorId":2830,"productId":4660,"variantType":"01-02","settings":[],"deviceEvents":[{"name":"Wheel","property":"wheel","type":"int"}]}}`)
		}
	}))
	defer server.Close()
	client := &Client{HTTPClient: server.Client(), BundlesURL: server.URL + "/bundles.json", ModelsBaseURL: server.URL, ModelName: "test", SchemaVersion: "1"}
	results, err := client.InspectProfiles(context.Background(), []uint16{0x1234, 0x1234, 0xffff})
	if err != nil || len(results) != 2 || len(requests) != 3 {
		t.Fatal(results, requests, err)
	}
	if results[0].TotalProfiles != 2 || len(results[0].Profiles) != 2 || results[1].TotalProfiles != 0 {
		t.Fatal(results)
	}
	for _, request := range requests {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatal(request)
		}
	}
	first, second := results[0].Profiles[0], results[0].Profiles[1]
	if first.Failure != "" || len(first.Definitions) != 3 || len(first.Definitions[0].Choices) != 3 || second.Sections["settings"] != 0 || len(second.Definitions) != 1 {
		t.Fatal(results)
	}
	encoded, _ := json.Marshal(results)
	if strings.Contains(string(encoded), "PRIVATE") {
		t.Fatal("raw/default values entered evidence")
	}
}

func TestInspectionReportsIdentityMismatchAndBudgetOmissions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bundles.json" {
			variants := []map[string]any{}
			for i := 0; i < maxInspectionProfiles+1; i++ {
				variants = append(variants, map[string]any{"vendorId": 2830, "productId": 4660, "variantType": fmt.Sprintf("V-%02d", i)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"unbundledProducts": []any{map[string]any{"variants": variants, "firmwareReleases": []any{map[string]any{"version": "1.0.0"}}}}})
			return
		}
		_, _ = fmt.Fprint(w, `{"device":{"vendorId":2830,"productId":999,"variantType":"WRONG","settings":[]}}`)
	}))
	defer server.Close()
	client := &Client{HTTPClient: server.Client(), BundlesURL: server.URL + "/bundles.json", ModelsBaseURL: server.URL}
	results, err := client.InspectProfiles(context.Background(), []uint16{0x1234})
	if err != nil || results[0].OmittedProfiles != 1 || len(results[0].Profiles) != maxInspectionProfiles {
		t.Fatal(results, err)
	}
	for _, profile := range results[0].Profiles {
		if !strings.Contains(profile.Failure, "identity") {
			t.Fatal(profile)
		}
	}
}

func TestNestedFutureSettingsPreserveRangesAndBoundChoiceEvidence(t *testing.T) {
	choices := []any{}
	for i := 0; i < 80; i++ {
		choices = append(choices, map[string]any{"value": fmt.Sprintf("mode-%d", i)})
	}
	device := map[string]any{"settings": []any{map[string]any{"groupName": "Sensors", "settings": []any{map[string]any{"settingId": "FUTURE_RANGE", "sdkProperty": "futureRange", "minimum": float64(-12), "maximum": float64(12), "possibleValues": choices, "current": "PRIVATE_CURRENT"}}}}}
	definitions, _, omitted := inspectDefinitions(device)
	if omitted != 0 || len(definitions) != 2 {
		t.Fatal(definitions, omitted)
	}
	definition := definitions[1]
	if definition.Group != "Sensors" || definition.Limits["minimum"] != "-12" || len(definition.Choices) != 64 || definition.OmittedChoices != 16 {
		t.Fatal(definition)
	}
	data, _ := json.Marshal(definitions)
	if strings.Contains(string(data), "PRIVATE_CURRENT") {
		t.Fatal("current value entered evidence")
	}
}
