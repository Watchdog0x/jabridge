package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUSBAudioTopologyReportsHubsAndControllerWithoutPrivateText(t *testing.T) {
	root := t.TempDir()
	pci := filepath.Join(root, "devices/pci0000:00/0000:05:00.2")
	hub := filepath.Join(pci, "usb5/5-2")
	device := filepath.Join(hub, "5-2.4")
	write := func(dir, name, value string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{"class": "0x0c0330", "vendor": "0x10de", "device": "0x1ad8"} {
		write(pci, key, value)
	}
	for key, value := range map[string]string{"bDeviceClass": "09", "idVendor": "1234", "idProduct": "5678", "serial": "PRIVATE_HUB"} {
		write(hub, key, value)
	}
	for key, value := range map[string]string{"idVendor": "0b0e", "idProduct": "0e36", "serial": "PRIVATE_HEADSET", "product": "PRIVATE_NAME"} {
		write(device, key, value)
	}
	bus := filepath.Join(root, "bus/usb/devices")
	if err := os.MkdirAll(bus, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(device, filepath.Join(bus, "5-2.4")); err != nil {
		t.Fatal(err)
	}
	rows, err := readUSBAudioTopology(root)
	var out bytes.Buffer
	writeUSBAudioTopology(&out, rows, err)
	text := out.String()
	for _, want := range []string{"0e36", "5-2.4", "1234:5678", "PCI 10de:1ad8 (xHCI)"} {
		if !strings.Contains(text, want) {
			t.Fatal(text)
		}
	}
	if strings.Contains(text, "PRIVATE") || strings.Contains(text, root) {
		t.Fatal("private fields exported", text)
	}
}

func TestStickyMixerWarningsMatchExactPortAndKeepCausalLimits(t *testing.T) {
	rows := []usbAudioTopology{{PID: 0x0e36, Port: "5-2.4"}}
	log := []byte("usb 5-2.40: 2:0: sticky mixer values (PRIVATE), disabling\nusb 5-2.4: 2:0: sticky mixer values (-12288/3072/1024 => 3072), disabling\nPRIVATE unrelated kernel log\n")
	var out bytes.Buffer
	writeStickyMixerDiagnostic(&out, rows, log, nil)
	text := out.String()
	if !strings.Contains(text, "1 sticky-mixer") || !strings.Contains(text, "may predate") || strings.Contains(text, "PRIVATE") || strings.Contains(text, "3072") || strings.Contains(text, "5-2.40") {
		t.Fatal(text)
	}
	out.Reset()
	writeStickyMixerDiagnostic(&out, rows, nil, errors.New("PRIVATE_PERMISSION"))
	if !strings.Contains(out.String(), "unavailable") || strings.Contains(out.String(), "none found") || strings.Contains(out.String(), "PRIVATE") {
		t.Fatal(out.String())
	}
}
