package ipc

import (
	"encoding/json"
	"testing"

	"github.com/Watchdog0x/jabridge/daemon/buttons"
)

type buttonMock struct {
	mockAPI
	changes int
}

func (a *buttonMock) GetButtons() buttons.Status { return buttons.Status{Mode: "off"} }
func (a *buttonMock) ConfigureButtons(mode string) (buttons.Status, error) {
	a.changes++
	return buttons.Status{Mode: mode}, nil
}

func TestButtonIPCRejectsInvalidModeAndDoesNotMutateOnRead(t *testing.T) {
	api := &buttonMock{}
	for _, params := range []string{`{}`, `{"mode":true}`, `{"mode":"anything"}`, `{"mode":"play-pause","extra":1}`} {
		response := dispatch(Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "buttons.configure", Params: json.RawMessage(params)}, api)
		if response.Error == nil || api.changes != 0 {
			t.Fatal(response)
		}
	}
	if r := dispatch(Request{JSONRPC: "2.0", ID: json.RawMessage("2"), Method: "buttons.status"}, api); r.Error != nil || api.changes != 0 {
		t.Fatal(r)
	}
	if r := dispatch(Request{JSONRPC: "2.0", ID: json.RawMessage("3"), Method: "buttons.configure", Params: json.RawMessage(`{"mode":"play-pause"}`)}, api); r.Error != nil || api.changes != 1 {
		t.Fatal(r)
	}
}
