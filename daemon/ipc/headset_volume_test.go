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
	reads   int
	infos   int
}

func (a *volumeAPI) GetHeadsetVolume(target SettingTarget) (headsetvolume.Value, error) {
	a.reads++
	a.target = target
	return headsetvolume.Value{Percent: 50, Source: "saved"}, nil
}

func (a *volumeAPI) HeadsetVolumeCapabilities(target SettingTarget) (headsetvolume.Capabilities, error) {
	a.infos++
	a.target = target
	return headsetvolume.ForDevice(0x0e36, "1.11.0", "usb"), nil
}

func TestHeadsetVolumeReadIPCRejectsWritesAndMissingTargets(t *testing.T) {
	target := `"target":{"id":2,"instance":"` + strings.Repeat("a", 32) + `"}`
	for _, method := range []string{"device.volume.get", "device.volume.info"} {
		for _, body := range []string{`{}`, `{"target":null}`, `{` + target + `,"percent":50}`, `{` + target + `,"unknown":true}`} {
			a := &volumeAPI{}
			r := dispatch(Request{Method: method, Params: json.RawMessage(body)}, a)
			if r.Error == nil || r.Error.Code != ErrCodeInvalidP || a.reads+a.infos+a.calls != 0 {
				t.Fatal("invalid read reached a device operation", method, body, r)
			}
		}
		a := &volumeAPI{}
		r := dispatch(Request{Method: method, Params: json.RawMessage(`{` + target + `}`)}, a)
		if r.Error != nil || a.target.ID != 2 || a.calls != 0 || a.reads+a.infos != 1 {
			t.Fatal("read changed volume or lost its binding", r)
		}
	}
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
