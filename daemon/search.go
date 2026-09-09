package daemon

import (
	"errors"
	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

func (a *busylightAPI) GetSearchState() ipc.SearchState {
	if api, ok := a.API.(ipc.SearchAPI); ok {
		return api.GetSearchState()
	}
	return ipc.SearchState{State: "unsupported"}
}
func (a *busylightAPI) StopSearch() error {
	if api, ok := a.API.(ipc.SearchAPI); ok {
		return api.StopSearch()
	}
	return errors.New("search status is not supported by this service")
}

func (a *busylightAPI) ConnectSearchDeviceBound(index int, session string) error {
	if api, ok := a.API.(ipc.BoundSearchAPI); ok {
		return api.ConnectSearchDeviceBound(index, session)
	}
	return errors.New("bound search connection is not supported by this service")
}
