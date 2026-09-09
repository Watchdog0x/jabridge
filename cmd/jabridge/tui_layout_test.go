package main

import (
	"strings"
	"testing"
)

func withFirmwareView(t *testing.T) {
	t.Helper()
	firmwareViewMu.Lock()
	old, counter := firmwareView, firmwareRequestCounter
	firmwareViewMu.Unlock()
	t.Cleanup(func() {
		firmwareViewMu.Lock()
		firmwareView, firmwareRequestCounter = old, counter
		firmwareViewMu.Unlock()
	})
}

func TestFirmwareValuesShareOneColumn(t *testing.T) {
	withMenuState(t)
	withFirmwareView(t)
	target := switchDeviceItem{RegistryID: 1, Device: &jabra_DeviceInfo{deviceID: 1, productID: 0x24c7, deviceName: "Jabra Link 380", instance: "unit-a"}}
	firmwareTargetItems = []switchDeviceItem{target}
	firmwareTargetIndex = 0
	firmwareTargetID = 1
	request, key := beginFirmwareView(target)
	applyFirmwareInfo(request, key, "1.16.0", "1.16.0", nil)
	applyFirmwareDownload(request, key, "./firmware/test.zip", "1.16.0")
	f := newRenderTarget(t, 100, 24)
	renderFirmware()
	left, right, bottom := panelBounds()
	column := left + 4 + 20
	for _, value := range []string{"1 of 1", "Jabra Link 380", "0b0e:24c7", "1.16.0", "Up to date", "./firmware/test.zip", "Not needed"} {
		found := false
		for row := 8; row < bottom; row++ {
			line := rowText(f, row)
			if !strings.Contains(line, ":") {
				continue
			} // exclude the numbered picker
			if i := strings.Index(line, value); i >= 0 {
				actual := textColumn(t, f, row, value)
				if actual != column {
					t.Fatalf("%s starts at %d, expected %d: %q", value, actual, column, line)
				}
				found = true
			}
		}
		if !found {
			t.Fatal("missing firmware value", value)
		}
	}
	if f.cells[(bottom-1)*f.width+left-1].ch != '┗' || f.cells[(bottom-1)*f.width+right-1].ch != '┛' {
		t.Fatal("border overwritten")
	}
}

func TestHeaderSeparatesCallControlAndBattery(t *testing.T) {
	withMenuState(t)
	d := engageFixture(0x4052)
	d.deviceName = "Jabra Engage 50 II"
	d.batteryStatus = &batteryStatus{levelInPercent: 48, charging: true}
	withDeviceState(t, devices{7: d}, 7, -1)
	for _, w := range []int{50, 80, 120} {
		f := newRenderTarget(t, w, 24)
		header()
		left, right, _ := panelBounds()
		row2, row3 := rowText(f, 2), rowText(f, 3)
		if w >= 80 && !strings.Contains(row2, "Link Call Control: connected") {
			t.Fatal(row2)
		}
		if !strings.Contains(row3, "48%") || !strings.Contains(row3, "charging") {
			t.Fatal(row3)
		}
		if strings.Contains(row2, "48%") || strings.Contains(row3, "Link Call Control") {
			t.Fatal("header sections overlap", row2, row3)
		}
		for row := 1; row <= 3; row++ {
			line := []rune(rowText(f, row))
			if strings.TrimSpace(string(line[:max(0, left-1)])) != "" || strings.TrimSpace(string(line[right:])) != "" {
				t.Fatalf("header escaped panel bounds at width %d", w)
			}
		}
	}
}

func TestWiredCallControlDoesNotInventBatteryOrHeadset(t *testing.T) {
	withMenuState(t)
	oldScan := firstScanComplete.Load()
	firstScanComplete.Store(true)
	t.Cleanup(func() { firstScanComplete.Store(oldScan) })
	d := engageFixture(0x4052)
	d.batteryStatus = nil
	withDeviceState(t, devices{7: d}, 7, -1)
	f := newRenderTarget(t, 100, 24)
	header()
	if strings.Contains(rowText(f, 3), "%") || strings.Contains(strings.ToLower(rowText(f, 3)), "battery") {
		t.Fatal(rowText(f, 3))
	}
	updateDeviceByID(7, func(device *jabra_DeviceInfo) {
		parts := append([]controlPart(nil), device.controlParts...)
		parts[0].Ready = false
		applyControlParts(device, parts)
	})
	f = newRenderTarget(t, 100, 24)
	header()
	if !strings.Contains(rowText(f, 3), "Headset: not detected") {
		t.Fatal(rowText(f, 3))
	}
}

func TestMultipleBatteriesShowOnlyValidPercentages(t *testing.T) {
	withMenuState(t)
	d := &jabra_DeviceInfo{deviceID: 1, deviceName: "Wireless headset", batteryStatus: &batteryStatus{levelInPercent: 70, components: []batteryComponentStatus{{label: "Left", levelInPercent: 70}, {label: "Right", levelInPercent: 65}, {label: "Case", levelInPercent: 90, charging: true}, {label: "Bad", levelInPercent: 230}}}}
	withDeviceState(t, devices{1: d}, 1, -1)
	f := newRenderTarget(t, 100, 24)
	header()
	row := rowText(f, 3)
	for _, want := range []string{"Left 70%", "Right 65%", "Case 90%"} {
		if !strings.Contains(row, want) {
			t.Fatal(row)
		}
	}
	if strings.Contains(row, "230") {
		t.Fatal(row)
	}
}

func TestFirmwareCycleDiscardsOldMetadataAndDownloads(t *testing.T) {
	withMenuState(t)
	withFirmwareView(t)
	a := switchDeviceItem{RegistryID: 1, Device: &jabra_DeviceInfo{deviceID: 1, productID: 0x24c7, instance: "a", deviceName: "Link"}}
	b := switchDeviceItem{RegistryID: 2, Device: &jabra_DeviceInfo{deviceID: 2, productID: 0x24b7, instance: "b", deviceName: "Headset"}}
	c := switchDeviceItem{RegistryID: 3, Device: &jabra_DeviceInfo{deviceID: 3, productID: 0x4052, instance: "c", deviceName: "Engage"}}
	firmwareTargetItems = []switchDeviceItem{a, b, c}
	firmwareTargetIndex = 0
	firmwareTargetID = 1
	oldRequest, oldKey := beginFirmwareView(a)
	for _, want := range []int{2, 3, 1} {
		if !advanceFirmwareTarget() || firmwareTargetID != want {
			t.Fatal(firmwareTargetID)
		}
	}
	newRequest, newKey := beginFirmwareView(a)
	if applyFirmwareInfo(oldRequest, oldKey, "stale", "stale", nil) || applyFirmwareDownload(oldRequest, oldKey, "wrong.zip", "stale") {
		t.Fatal("old A response accepted after A/B/C/A switch")
	}
	if !applyFirmwareInfo(newRequest, newKey, "1.16.0", "1.16.0", nil) {
		t.Fatal("current response rejected")
	}
	replacement := a
	replacement.Device = cloneDeviceInfo(a.Device)
	replacement.Device.instance = "replacement"
	beginFirmwareView(replacement)
	if applyFirmwareDownload(newRequest, newKey, "old-device.zip", "1.16.0") {
		t.Fatal("reused ID accepted old download")
	}
}

func TestFirmwareRefreshPreservesTargetAndHandlesRemoval(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, devices{4: {deviceID: 4, productID: 0x24c7}, 7: {deviceID: 7, productID: 0x24b7}}, 7, 4)
	firmwareTargetID = 7
	refreshFirmwareTargets()
	if firmwareTargetID != 7 || firmwareTargetIndex != 1 {
		t.Fatal("target selection changed")
	}
	deviceStateMu.Lock()
	delete(deviceManager, 7)
	deviceStateMu.Unlock()
	refreshFirmwareTargets()
	if firmwareTargetID != 4 || firmwareTargetIndex != 0 {
		t.Fatal("removed target retained")
	}
}

func TestCompactHomeMenuScrollsToSelectedAction(t *testing.T) {
	withMenuState(t)
	withDeviceState(t, nil, -1, -1)
	startMenu = []menuItem{{label: "One"}, {label: "Two"}, {label: "Three"}, {label: "Four"}, {label: "Quit"}}
	currentSelection = 4
	f := newRenderTarget(t, 80, 16)
	menu()
	_, _, bottom := panelBounds()
	if !strings.Contains(rowText(f, bottom-1), "Quit") {
		t.Fatal("selected action hidden")
	}
}
