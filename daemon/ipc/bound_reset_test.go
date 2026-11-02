package ipc

import (
	"encoding/json"
	"strings"
	"testing"
)

type boundResetMock struct {
	mockAPI
	calls  int
	target SettingTarget
}

func (m *boundResetMock) FactoryResetBound(target SettingTarget) (BoundResetResult, error) {
	m.calls++
	m.target = target
	return BoundResetResult{Outcome: "restart-verified", Attempted: true, Reconnected: true, Message: "The dongle restarted and returned."}, nil
}
func TestBoundResetStrictConfirmationAndTarget(t *testing.T) {
	api := &boundResetMock{}
	for _, params := range []string{
		`null`, `{}`, `{"confirm":"yes"}`, `{"confirm":"ERASE_REMEMBERED_HEADSETS"}`,
		`{"confirm":"RESET_LINK390_DONGLE","target":{}}`,
		`{"confirm":"RESET_LINK390_DONGLE","target":{"id":1,"instance":"bad"}}`,
		`{"confirm":"RESET_LINK390_DONGLE","target":{"id":1,"instance":"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"}}`,
		`{"confirm":"RESET_LINK390_DONGLE","target":{"id":1,"instance":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","other":1}}`,
		`{"confirm":"RESET_LINK390_DONGLE","target":{"id":1,"instance":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"force":true}`,
	} {
		response := dispatch(Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "device.reset.bound", Params: json.RawMessage(params)}, api)
		if response.Error == nil || response.Error.Code != ErrCodeInvalidP || api.calls != 0 {
			t.Fatal(params, response, api.calls)
		}
	}
	params := `{"confirm":"RESET_LINK390_DONGLE","target":{"id":7,"instance":"` + strings.Repeat("a", 32) + `","topology":"x"}}`
	response := dispatch(Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "device.reset.bound", Params: json.RawMessage(params)}, api)
	if response.Error != nil || api.calls != 1 || api.target.ID != 7 || api.target.Topology != "x" {
		t.Fatal(response, api.calls, api.target)
	}
}
