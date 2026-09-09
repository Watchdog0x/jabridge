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
