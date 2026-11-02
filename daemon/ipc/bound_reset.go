package ipc

import (
	"encoding/hex"
)

type ResetCheck struct {
	Key    string `json:"key"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}
type BoundResetResult struct {
	Outcome     string       `json:"outcome"`
	Attempted   bool         `json:"attempted"`
	Reconnected bool         `json:"reconnected"`
	Message     string       `json:"message"`
	Checks      []ResetCheck `json:"checks,omitempty"`
}
type BoundResetAPI interface {
	FactoryResetBound(SettingTarget) (BoundResetResult, error)
}

func dispatchBoundReset(req Request, api API) Response {
	var params struct {
		Confirm string         `json:"confirm"`
		Target  *SettingTarget `json:"target"`
	}
	if err := decodeParams(req.Params, &params); err != nil || params.Confirm != "RESET_LINK390_DONGLE" || params.Target == nil || len(params.Target.Instance) != 32 {
		return ErrorResponse(req.ID, ErrCodeInvalidP, "device.reset.bound requires confirmation and a current target")
	}
	if _, err := hex.DecodeString(params.Target.Instance); err != nil {
		return ErrorResponse(req.ID, ErrCodeInvalidP, "invalid target instance")
	}
	resetter, ok := api.(BoundResetAPI)
	if !ok {
		return ErrorResponse(req.ID, ErrCodeMethodNF, "service does not support a bound reset")
	}
	result, err := resetter.FactoryResetBound(*params.Target)
	if err != nil {
		return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
	}
	return SuccessResponse(req.ID, result)
}
