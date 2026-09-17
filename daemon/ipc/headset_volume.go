package ipc

import "github.com/Watchdog0x/jabridge/internal/headsetvolume"

type HeadsetVolumeAPI interface {
	SetHeadsetVolume(SettingTarget, int) (headsetvolume.Value, error)
}

type HeadsetVolumeReadAPI interface {
	GetHeadsetVolume(SettingTarget) (headsetvolume.Value, error)
	HeadsetVolumeCapabilities(SettingTarget) (headsetvolume.Capabilities, error)
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

func dispatchHeadsetVolumeRead(req Request, api API) Response {
	var params struct {
		Target *SettingTarget `json:"target"`
	}
	if err := decodeParams(req.Params, &params); err != nil || params.Target == nil || len(params.Target.Instance) != 32 {
		return ErrorResponse(req.ID, ErrCodeInvalidP, "headset volume requires a captured target")
	}
	volume, ok := api.(HeadsetVolumeReadAPI)
	if !ok {
		return ErrorResponse(req.ID, ErrCodeMethodNF, "headset volume reads are unavailable in this service")
	}
	if req.Method == "device.volume.info" {
		value, err := volume.HeadsetVolumeCapabilities(*params.Target)
		if err != nil {
			return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
		}
		return SuccessResponse(req.ID, value)
	}
	value, err := volume.GetHeadsetVolume(*params.Target)
	if err != nil {
		return ErrorResponse(req.ID, ErrCodeInternal, err.Error())
	}
	return SuccessResponse(req.ID, value)
}
