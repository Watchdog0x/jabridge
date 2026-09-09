package ipc

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestCapabilitiesDescribeInterfacesWithoutClaimingEveryDeviceSupportsThem(t *testing.T) {
	base := serviceCapabilities(&mockAPI{})
	if slices.Contains(base.Reads, "buttons.status") || !base.LossyEvents || base.Version != 1 {
		t.Fatal(base)
	}
	buttons := serviceCapabilities(&buttonMock{})
	if !slices.Contains(buttons.Reads, "buttons.status") || !slices.Contains(buttons.Events, "device.button") {
		t.Fatal(buttons)
	}
	if result := dispatch(Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "service.capabilities", Params: json.RawMessage(`{"unexpected":true}`)}, &mockAPI{}); result.Error == nil {
		t.Fatal("invalid parameters accepted")
	}
}
