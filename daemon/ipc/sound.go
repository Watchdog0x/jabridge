package ipc

import (
	"encoding/hex"

	"github.com/Watchdog0x/jabridge/daemon/pipewire"
)

type SoundAPI interface {
	GetSound() pipewire.SoundState
	ChangeSound(pipewire.SoundTarget, string, int, string) (pipewire.SoundNode, error)
}

type SoundModeAPI interface {
	ChangeSoundMode(pipewire.SoundTarget, string) (pipewire.SoundState, error)
}

type SoundRecoveryAPI interface {
	RecoverSound(pipewire.SoundTarget) (pipewire.RecoveryResult, error)
}

func dispatchSound(req Request, api API) Response {
	sound, ok := api.(SoundAPI)
	if !ok {
		return ErrorResponse(req.ID, ErrCodeMethodNF, "sound IPC unavailable; update Jabridge and restart its service")
	}
	if req.Method == "sound.list" {
		if err := decodeParams(req.Params, &struct{}{}); err != nil {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "sound.list takes no parameters")
		}
		return SuccessResponse(req.ID, sound.GetSound())
	}
	var target pipewire.SoundTarget
	var percent int
	var mode string
	var err error
	action := ""
	switch req.Method {
	case "sound.recover":
		var p struct {
			Target pipewire.SoundTarget `json:"target"`
		}
		if err := decodeParams(req.Params, &p); err != nil {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "invalid sound.recover parameters")
		}
		_, tokenErr := hex.DecodeString(p.Target.Token)
		if tokenErr != nil || len(p.Target.Token) != 64 || p.Target.ID <= 0 {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "sound.recover needs a complete output target")
		}
		recovery, ok := api.(SoundRecoveryAPI)
		if !ok {
			return ErrorResponse(req.ID, ErrCodeMethodNF, "audio recovery unavailable; update the service")
		}
		result, err := recovery.RecoverSound(p.Target)
		if err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, result)
	case "sound.mode":
		var p struct {
			Target pipewire.SoundTarget `json:"target"`
			Mode   string               `json:"mode"`
		}
		if err := decodeParams(req.Params, &p); err != nil {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "invalid sound.mode parameters")
		}
		_, tokenErr := hex.DecodeString(p.Target.Token)
		if tokenErr != nil || len(p.Target.Token) != 64 || p.Target.ID <= 0 || (p.Mode != "music" && p.Mode != "calls") {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "sound.mode needs a complete target and music or calls")
		}
		modes, ok := api.(SoundModeAPI)
		if !ok {
			return ErrorResponse(req.ID, ErrCodeMethodNF, "audio modes unavailable; update the service")
		}
		state, err := modes.ChangeSoundMode(p.Target, p.Mode)
		if err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, state)
	case "sound.default":
		var p struct {
			Target pipewire.SoundTarget `json:"target"`
		}
		err = decodeParams(req.Params, &p)
		target = p.Target
		action = "default"
	case "sound.volume":
		var p struct {
			Target  pipewire.SoundTarget `json:"target"`
			Percent *int                 `json:"percent"`
		}
		err = decodeParams(req.Params, &p)
		target = p.Target
		action = "volume"
		if p.Percent == nil || *p.Percent < 0 || *p.Percent > 100 {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "sound.volume requires an integer percent from 0 to 100")
		}
		percent = *p.Percent
	case "sound.mute":
		var p struct {
			Target pipewire.SoundTarget `json:"target"`
			Mode   string               `json:"mode"`
		}
		err = decodeParams(req.Params, &p)
		target = p.Target
		mode = p.Mode
		action = "mute"
		if mode != "on" && mode != "off" && mode != "toggle" {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "sound.mute requires mode on, off or toggle")
		}
	default:
		return ErrorResponse(req.ID, ErrCodeMethodNF, "unknown sound method")
	}
	_, tokenErr := hex.DecodeString(target.Token)
	if err != nil || tokenErr != nil || target.ID <= 0 || len(target.Token) != 64 {
		return ErrorResponse(req.ID, ErrCodeInvalidP, "sound change requires the complete target from sound.list")
	}
	result, err := sound.ChangeSound(target, action, percent, mode)
	if err != nil {
		return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
	}
	return SuccessResponse(req.ID, result)
}
