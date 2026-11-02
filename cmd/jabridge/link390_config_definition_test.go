package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLink390ByteConfigurationModelsAndLimits(t *testing.T) {
	for _, key := range []string{"wireless-range", "softphone-integration"} {
		definition, ok := link390ConfigDefinitionForKey(key)
		if !ok || !definition.valid() {
			t.Fatal("missing configuration descriptor", key)
		}
		for raw := 0; raw < 256; raw++ {
			value, err := decodeLink390ConfigValue(key, []byte{byte(raw)})
			valid := key == "wireless-range" && raw >= 1 && raw <= 3 || key == "softphone-integration" && raw <= 1
			if (err == nil) != valid || valid && value != byte(raw) {
				t.Fatal("model range widened", key, raw, value, err)
			}
		}
		for _, bad := range [][]byte{nil, {0, 1}, {1, 2, 3}} {
			if _, err := decodeLink390ConfigValue(key, bad); err == nil {
				t.Fatal("wrong reply size", key, bad)
			}
		}
	}
	for _, key := range []string{"bluetooth-radio", "factory-reset", "other"} {
		if _, err := decodeLink390ConfigValue(key, []byte{1}); err == nil {
			t.Fatal("unknown configuration field", key)
		}
	}
	definition, _ := link390ConfigDefinitionForKey("wireless-range")
	if _, err := link390ConfigPayload(definition, 4); !errors.Is(err, ErrNotSupported) {
		t.Fatal("ultra-low escaped exact model", err)
	}
	definition.Op = 0x11
	if _, err := link390ConfigPayload(definition, 1); !errors.Is(err, ErrNotSupported) {
		t.Fatal("forged descriptor reached reset opcode", err)
	}
	if _, err := changeLink390Config(nil, definition, 1, nil); !errors.Is(err, ErrNotSupported) {
		t.Fatal("ineligible configuration entered transport", err)
	}
}

func TestLink390ConfigCountsOnlySettledSameGenerationReadbacks(t *testing.T) {
	f := newConfigFlowFake()
	f.value = 1
	f.openHook = func(f *configFlowFake) {
		if f.opens == 3 {
			f.id.Generation = "attachment-B"
			f.id.HIDPath = "/dev/hidraw9"
		}
	}
	result, err := runLink390ConfigChange(context.Background(), f, f.id, "wireless-range", 2, nil)
	if err != nil || result.Status != link390Applied || result.Identity.Generation != "attachment-B" || len(f.readTimes) < 3 {
		t.Fatal("late restart was not reverified", result, err, f.readTimes)
	}
	if f.readTimes[0].Sub(f.endAt) < 3*time.Second || f.readTimes[len(f.readTimes)-1].Sub(f.readTimes[len(f.readTimes)-2]) < time.Second {
		t.Fatal("software settle policy was bypassed", f.readTimes)
	}
	if strings.Join(f.frames, ",") != "permission,write:true@attachment-A,end@attachment-A" {
		t.Fatal("command or END replayed", f.frames)
	}
}

func TestLink390ConfigFirmwareMismatchBeforeAndAfterWrite(t *testing.T) {
	for _, before := range []bool{true, false} {
		f := newConfigFlowFake()
		target := f.id
		if before {
			f.id.Firmware = "3.3.0"
		} else {
			f.openHook = func(f *configFlowFake) {
				if f.opens == 2 {
					f.id.Firmware = "3.3.0"
				}
			}
		}
		result, err := runLink390ConfigChange(context.Background(), f, target, "softphone-integration", 1, nil)
		if err == nil || before && len(f.frames) != 0 || !before && result.Status != link390Unknown {
			t.Fatal("firmware mismatch was ignored", before, result, err, f.frames)
		}
	}
}

func FuzzLink390ConfigReadback(f *testing.F) {
	f.Add("wireless-range", []byte{1})
	f.Add("wireless-range", []byte{4})
	f.Add("softphone-integration", []byte{0})
	f.Add("factory-reset", []byte{1})
	f.Fuzz(func(t *testing.T, key string, payload []byte) {
		value, err := decodeLink390ConfigValue(key, payload)
		if err != nil {
			return
		}
		if len(payload) != 1 || value != payload[0] || key == "wireless-range" && (value < 1 || value > 3) || key == "softphone-integration" && value > 1 || key != "wireless-range" && key != "softphone-integration" {
			t.Fatal("invalid configuration reply accepted", key, payload)
		}
	})
}
