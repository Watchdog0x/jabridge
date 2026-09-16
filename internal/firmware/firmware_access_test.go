package firmware

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/Watchdog0x/jabridge/internal/assets"
)

// Keep installed OS permissions in sync with native transport support. A
// parser/transfer test alone cannot notice an updater that users cannot open.
func TestFirmwareUSBProfilesHaveInstalledAccessRules(t *testing.T) {
	allowed := map[string]bool{}
	pattern := regexp.MustCompile(`ATTR\{idProduct\}=="([0-9a-f|]+)"`)
	for _, line := range strings.Split(string(assets.UdevRule), "\n") {
		if !strings.HasPrefix(line, `SUBSYSTEM=="usb"`) {
			continue
		}
		if !strings.Contains(line, `ATTR{idVendor}=="0b0e"`) || !strings.Contains(line, `ENV{DEVTYPE}=="usb_device"`) || !strings.Contains(line, `TAG+="uaccess"`) {
			t.Fatal("USB firmware rule does not restrict vendor/device or grant desktop access")
		}
		match := pattern.FindStringSubmatch(line)
		if len(match) != 2 {
			t.Fatal("USB access rule has an unbounded product match")
		}
		for _, pid := range strings.Split(match[1], "|") {
			allowed[pid] = true
		}
	}
	wanted := map[string]bool{}
	for _, profile := range usbDFUProfiles {
		for _, pid := range append(append([]uint16(nil), profile.RuntimePIDs...), profile.DFUPID) {
			wanted[fmt.Sprintf("%04x", pid)] = true
		}
	}
	for _, profile := range bulkCameraProfiles {
		for _, pid := range profile.RuntimePIDs {
			wanted[fmt.Sprintf("%04x", pid)] = true
		}
	}
	for pid := range wanted {
		if !allowed[pid] {
			t.Errorf("native firmware PID %s has no installed USB access rule", pid)
		}
	}
	for pid := range allowed {
		if !wanted[pid] {
			t.Errorf("USB rule grants firmware access to unregistered PID %s", pid)
		}
	}
}

func TestPanaCast20VideoAccessRuleMatchesNativeModels(t *testing.T) {
	var allowed []string
	pattern := regexp.MustCompile(`ATTRS\{idProduct\}=="([0-9a-f|]+)"`)
	for _, line := range strings.Split(string(assets.UdevRule), "\n") {
		if !strings.HasPrefix(line, `SUBSYSTEM=="video4linux"`) {
			continue
		}
		if !strings.Contains(line, `ATTRS{idVendor}=="0b0e"`) || !strings.Contains(line, `TAG+="uaccess"`) {
			t.Fatal("camera rule is not vendor-bound desktop access")
		}
		match := pattern.FindStringSubmatch(line)
		if len(match) != 2 {
			t.Fatal("unbounded camera access rule")
		}
		allowed = append(allowed, strings.Split(match[1], "|")...)
	}
	if len(allowed) != len(uvcCameraRuntimePIDs) {
		t.Fatal("camera access and native model counts differ")
	}
	for _, pid := range uvcCameraRuntimePIDs {
		found := false
		for _, value := range allowed {
			if value == fmt.Sprintf("%04x", pid) {
				found = true
			}
		}
		if !found {
			t.Errorf("camera %04x has no video access rule", pid)
		}
	}
}
