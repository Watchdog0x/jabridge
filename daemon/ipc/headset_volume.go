package ipc

import "github.com/Watchdog0x/jabridge/internal/headsetvolume"

type HeadsetVolumeAPI interface {
	SetHeadsetVolume(SettingTarget, int) (headsetvolume.Value, error)
}

func dispatchHeadsetVolume(req Request, api API) Response {
	var params struct {
		Target  *SettingTarget `json:"target"`
		Percent *int           `json:"percent"`
	}
	if err := decodeParams(req.Params, &params); err != nil || params.Target == nil || len(params.Target.Instance) != 32 || params.Percent == nil || *params.Percent < 0 || *params.Percent > 100 {
		return ErrorResponse(req.ID, ErrCodeInvalidP, "device.volume requires a captured target and percent from 0 to 100")
	}
	volume, ok := api.(HeadsetVolumeAPI)
	if !ok {
		return ErrorResponse(req.ID, ErrCodeMethodNF, "direct headset volume is unavailable in this service")
	}
	value, err := volume.SetHeadsetVolume(*params.Target, *params.Percent)
	if err != nil {
		return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
	}
	return SuccessResponse(req.ID, value)
}
