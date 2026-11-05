package ipc

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type dectMock struct {
	mockAPI
	calls int
}

func (m *dectMock) DECTComponents(uint16) ([]DiagnosticCheck, error) { m.calls++; return nil, nil }
func TestDECTComponentRPCRejectsInvalidParams(t *testing.T) {
	api := &dectMock{}
	for _, params := range []string{`null`, `{}`, `{"id":-1}`, `{"id":65536}`, `{"id":"1"}`, `{"id":1,"force":true}`} {
		r := dispatch(Request{ID: json.RawMessage(`1`), Method: "diagnostics.dect_components", Params: json.RawMessage(params)}, api)
		if r.Error == nil || r.Error.Code != ErrCodeInvalidP || api.calls != 0 {
			t.Fatal(params, r, api.calls)
		}
	}
	r := dispatch(Request{ID: json.RawMessage(`1`), Method: "diagnostics.dect_components", Params: json.RawMessage(`{"id":0}`)}, api)
	if r.Error != nil || api.calls != 1 {
		t.Fatal("registry id zero rejected", r, api.calls)
	}
	r = dispatch(Request{ID: json.RawMessage(`1`), Method: "diagnostics.dect_components", Params: json.RawMessage(`{"id":0}`)}, &mockAPI{})
	if r.Error == nil || r.Error.Code != ErrCodeMethodNF {
		t.Fatal("old service ambiguity", r)
	}
}

type dectInfoMock struct {
	mockAPI
	calls int
}

func (m *dectInfoMock) DECTImageInfo(context.Context, uint16) ([]DiagnosticCheck, error) {
	m.calls++
	return nil, nil
}
func TestDECTImageInfoRPCRejectsInvalidParams(t *testing.T) {
	api := &dectInfoMock{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, params := range []string{`null`, `{}`, `{"id":-1}`, `{"id":65536}`, `{"id":"1"}`, `{"id":1,"force":true}`} {
		r := dispatchContext(ctx, Request{ID: json.RawMessage(`1`), Method: "diagnostics.dect_image_info", Params: json.RawMessage(params)}, api)
		if r.Error == nil || r.Error.Code != ErrCodeInvalidP || api.calls != 0 {
			t.Fatal(params, r, api.calls)
		}
	}
	r := dispatchContext(ctx, Request{ID: json.RawMessage(`1`), Method: "diagnostics.dect_image_info", Params: json.RawMessage(`{"id":0}`)}, api)
	if r.Error != nil || api.calls != 1 {
		t.Fatal("registry id zero rejected", r, api.calls)
	}
	r = dispatchContext(ctx, Request{ID: json.RawMessage(`1`), Method: "diagnostics.dect_image_info", Params: json.RawMessage(`{"id":0}`)}, &mockAPI{})
	if r.Error == nil || r.Error.Code != ErrCodeMethodNF {
		t.Fatal("old service ambiguity", r)
	}
}
