package main

import "testing"

func TestParseSettingSelector(t *testing.T) {
	scope, key, err := parseSettingSelector("dongle.auto-pairing")
	if err != nil || scope != settingScopeDongle || key != "auto-pairing" {
		t.Fatalf("dongle selector = %v, %q, %v", scope, key, err)
	}
	scope, key, err = parseSettingSelector("headset.sidetone")
	if err != nil || scope != settingScopeHeadset || key != "sidetone" {
		t.Fatalf("headset selector = %v, %q, %v", scope, key, err)
	}
	if _, _, err := parseSettingSelector("sidetone"); err == nil {
		t.Fatal("selector without device was accepted")
	}
}

func TestParseOnOff(t *testing.T) {
	for _, value := range []string{"on", "true", "yes", "1"} {
		if got, err := parseOnOff(value); err != nil || !got {
			t.Errorf("parseOnOff(%q) = %v, %v", value, got, err)
		}
	}
	for _, value := range []string{"off", "false", "no", "0"} {
		if got, err := parseOnOff(value); err != nil || got {
			t.Errorf("parseOnOff(%q) = %v, %v", value, got, err)
		}
	}
	if _, err := parseOnOff("maybe"); err == nil {
		t.Fatal("invalid boolean value was accepted")
	}
}
