package ipc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/headsetvolume"
)

type volumeAPI struct {
	mockAPI
	calls   int
	target  SettingTarget
	percent int
}

func (a *volumeAPI) SetHeadsetVolume(target SettingTarget, percent int) (headsetvolume.Value, error) {
	a.calls++
	a.target = target
	a.percent = percent
	return headsetvolume.Value{Percent: 50}, nil
}

func TestHeadsetVolumeIPCValidatesTargetAndPercent(t *testing.T) {
	target := `"target":{"id":2,"instance":"` + strings.Repeat("a", 32) + `"}`
	for _, body := range []string{`{}`, `{` + target + `}`, `{"percent":50}`, `{` + target + `,"percent":-1}`, `{` + target + `,"percent":101}`, `{` + target + `,"percent":50.5}`, `{` + target + `,"percent":"50"}`, `{` + target + `,"unknown":true}`} {
		a := &volumeAPI{}
		r := dispatch(Request{Method: "device.volume", Params: json.RawMessage(body)}, a)
		if r.Error == nil || r.Error.Code != ErrCodeInvalidP || a.calls != 0 {
			t.Fatal("invalid volume reached device", body, r)
		}
	}
	for _, body := range []string{`{` + target + `,"percent":50}`, `{` + target + `,"percent":0}`} {
		a := &volumeAPI{}
		r := dispatch(Request{Method: "device.volume", Params: json.RawMessage(body)}, a)
		if r.Error != nil || a.calls != 1 || a.target.ID != 2 {
			t.Fatal(r)
		}
		if strings.Contains(body, `"percent":0`) && (a.percent != 0) {
			t.Fatal("zero volume treated as missing")
		}
	}
	r := dispatch(Request{Method: "device.volume", Params: json.RawMessage(`{` + target + `,"percent":50}`)}, &mockAPI{})
	if r.Error == nil || r.Error.Code != ErrCodeMethodNF {
		t.Fatal("unsupported API accepted volume request")
	}
}
