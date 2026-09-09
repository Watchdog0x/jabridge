package ipc

import (
	"encoding/json"
	"strings"
	"testing"
)

type searchAPIStub struct {
	mockAPI
	connections, stops int
}

func (s *searchAPIStub) GetSearchState() SearchState {
	return SearchState{State: "complete", Count: 1, Session: strings.Repeat("a", 32), Devices: []PairedDeviceInfo{{ID: 0, Name: "Office"}}}
}
func (s *searchAPIStub) StopSearch() error { s.stops++; return nil }
func (s *searchAPIStub) ConnectSearchDeviceBound(index int, token string) error {
	s.connections++
	return nil
}

func TestSearchIPCStatusStopAndBinding(t *testing.T) {
	api := &searchAPIStub{}
	call := func(method, params string) Response {
		return dispatch(Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: method, Params: json.RawMessage(params)}, api)
	}
	if got := call("bt.search.status", `{}`); got.Error != nil {
		t.Fatal(got)
	}
	if got := call("bt.search.stop", `{}`); got.Error != nil || api.stops != 1 {
		t.Fatal(got)
	}
	for _, params := range []string{`{"index":0}`, `{"index":-1,"session":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, `{"index":64,"session":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, `{"index":0,"session":"old"}`} {
		if got := call("bt.search.connect", params); got.Error == nil || got.Error.Code != ErrCodeInvalidP {
			t.Fatal(got)
		}
	}
	if api.connections != 0 {
		t.Fatal("invalid request reached the device")
	}
	if got := call("bt.search.connect", `{"index":0,"session":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`); got.Error != nil || api.connections != 1 {
		t.Fatal(got)
	}
	for _, method := range []string{"bt.search.status", "bt.search.stop", "bt.search"} {
		if got := call(method, `{"unexpected":true}`); got.Error == nil {
			t.Fatal(method)
		}
	}
}
