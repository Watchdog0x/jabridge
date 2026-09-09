package ipc

import "github.com/Watchdog0x/jabridge/daemon/buttons"

type ButtonsAPI interface {
	GetButtons() buttons.Status
	ConfigureButtons(string) (buttons.Status, error)
}

func dispatchButtons(req Request, api API) Response {
	control, ok := api.(ButtonsAPI)
	if !ok {
		return ErrorResponse(req.ID, ErrCodeMethodNF, "button IPC unavailable; update the service")
	}
	if req.Method == "buttons.status" {
		if err := decodeParams(req.Params, &struct{}{}); err != nil {
			return ErrorResponse(req.ID, ErrCodeInvalidP, "buttons.status takes no parameters")
		}
		return SuccessResponse(req.ID, control.GetButtons())
	}
	var params struct {
		Mode string `json:"mode"`
	}
	if err := decodeParams(req.Params, &params); err != nil || (params.Mode != "off" && params.Mode != "play-pause") {
		return ErrorResponse(req.ID, ErrCodeInvalidP, "buttons.configure requires mode off or play-pause")
	}
	state, err := control.ConfigureButtons(params.Mode)
	if err != nil {
		return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
	}
	return SuccessResponse(req.ID, state)
}
