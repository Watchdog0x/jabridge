package main

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

type slowBoundResetAPI struct {
	jabraAPIBridge
	commits atomic.Int32
}

func (a *slowBoundResetAPI) FactoryResetBound(ipc.SettingTarget) (ipc.BoundResetResult, error) {
	a.commits.Add(1)
	time.Sleep(60 * time.Second)
	return ipc.BoundResetResult{Outcome: "restart-verified", Attempted: true, Reconnected: true, Message: "The simulated dongle returned."}, nil
}
func TestBoundResetIPCWaitsForSlowResult(t *testing.T) {
	api := &slowBoundResetAPI{}
	client := policyIPCClient(t, api)
	var result ipc.BoundResetResult
	params := map[string]any{"confirm": "RESET_LINK390_DONGLE", "target": ipc.SettingTarget{ID: 7, Instance: strings.Repeat("a", 32)}}
	if err := ipcCall(client, "device.reset.bound", params, &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "restart-verified" || !result.Reconnected || api.commits.Load() != 1 {
		t.Fatal(result, api.commits.Load())
	}
}
