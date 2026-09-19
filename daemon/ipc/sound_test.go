package ipc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

type soundMockAPI struct {
	mockAPI
	writes int
}

func (a *soundMockAPI) GetSound() pipewire.SoundState {
	return pipewire.SoundState{Available: true, Nodes: []pipewire.SoundNode{}}
}
func (a *soundMockAPI) ChangeSound(t pipewire.SoundTarget, action string, p int, m string) (pipewire.SoundNode, error) {
	a.writes++
	return pipewire.SoundNode{Target: t}, nil
}

func (a *soundMockAPI) RecoverSound(pipewire.SoundTarget) (pipewire.RecoveryResult, error) {
	a.writes++
	return pipewire.RecoveryResult{SequenceCompleted: true, CaptureStopped: true}, nil
}

func TestSoundRecoveryIPCRequiresBoundTarget(t *testing.T) {
	a := &soundMockAPI{}
	for _, params := range []string{`{}`, `{"target":{"id":1}}`, `{"target":{"id":1,"token":"bad"}}`, `{"target":{"id":1,"token":"` + strings.Repeat("a", 64) + `"},"command":"PRIVATE"}`} {
		response := dispatch(Request{ID: json.RawMessage("1"), Method: "sound.recover", Params: json.RawMessage(params)}, a)
		if response.Error == nil || a.writes != 0 {
			t.Fatal("invalid recovery request accepted", response)
		}
	}
	response := dispatch(Request{ID: json.RawMessage("2"), Method: "sound.recover", Params: json.RawMessage(`{"target":{"id":1,"token":"` + strings.Repeat("a", 64) + `"}}`)}, a)
	if response.Error != nil || a.writes != 1 {
		t.Fatal(response, a.writes)
	}
}
func TestSoundIPCRejectsMalformedParameters(t *testing.T) {
	a := &soundMockAPI{}
	target := `"target":{"id":1,"token":"` + strings.Repeat("a", 64) + `"}`
	for _, test := range []struct{ method, params string }{
		{"sound.volume", `{` + target + `,"percent":101}`}, {"sound.volume", `{` + target + `,"percent":-1}`}, {"sound.volume", `{` + target + `,"percent":"50"}`}, {"sound.volume", `{` + target + `,"percent":50.5}`},
		{"sound.volume", `{` + target + `}`}, {"sound.mute", `{` + target + `,"mode":"maybe"}`}, {"sound.default", `{"target":{"id":1}}`}, {"sound.default", `{` + target + `,"command":"PRIVATE"}`}, {"sound.list", `{"raw":true}`},
	} {
		res := dispatch(Request{ID: json.RawMessage("1"), Method: test.method, Params: json.RawMessage(test.params)}, a)
		if res.Error == nil || res.Error.Code != ErrCodeInvalidP {
			t.Fatal(test, res)
		}
	}
	if a.writes != 0 {
		t.Fatal("invalid request reached backend")
	}
	res := dispatch(Request{ID: json.RawMessage("1"), Method: "sound.volume", Params: json.RawMessage(`{` + target + `,"percent":50}`)}, a)
	if res.Error != nil || a.writes != 1 {
		t.Fatal(res)
	}
}
