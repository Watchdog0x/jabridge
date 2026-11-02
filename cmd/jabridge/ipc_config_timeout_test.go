package main

import (
	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"testing"
	"time"
)

type slowConfigAPI struct{ editablePolicyAPI }

func (*slowConfigAPI) SetSetting(_, key, value string) (ipc.SettingInfo, error) {
	time.Sleep(7 * time.Second)
	return ipc.SettingInfo{Key: key, Value: value}, nil
}

func TestIPCConfigWriteWaitsForSlowVerifiedTransaction(t *testing.T) {
	client := policyIPCClient(t, &slowConfigAPI{})
	var result ipc.SettingInfo
	if err := ipcCall(client, "settings.set", map[string]string{"device": "dongle", "key": "wireless-range", "value": "low"}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Key != "wireless-range" || result.Value != "low" {
		t.Fatal(result)
	}
}
