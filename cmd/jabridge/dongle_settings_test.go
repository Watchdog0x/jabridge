package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/completion"
	"github.com/Watchdog0x/jabridge/internal/history"
	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func dongleDeviceForTest(pid uint16) *jabra_DeviceInfo {
	return &jabra_DeviceInfo{deviceID: 7, productID: pid, isDongle: true, instance: "simulated-attachment", serialNumber: "simulated-serial", variantType: "04-10", firmwareVersion: "3.2.0"}
}

func dongleProfileForTest(t *testing.T, pid uint16) *modelcatalog.Capabilities {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bundles" {
			_, _ = fmt.Fprintf(w, `{"unbundledProducts":[{"variants":[{"vendorId":2830,"productId":%d,"variantType":"04-10"}],"firmwareReleases":[{"version":"3.2.0"}]}]}`, pid)
			return
		}
		_, _ = w.Write([]byte(`{"device":{"settings":[{"type":"Enum","sdkProperties":["leAudioCallProfile"],"settingAccess":"ReadWrite","possibleValues":[{"value":"widebandMono"},{"value":"superWidebandMono"},{"value":"superWidebandStereo"}]},{"type":"Enum","sdkProperties":["automaticAudioDetection"],"settingAccess":"ReadWrite","possibleValues":[{"value":"disabled"},{"value":"enabled"}]}]}}`))
	}))
	defer server.Close()
	client := modelcatalog.NewClient()
	client.HTTPClient, client.BundlesURL, client.ModelsBaseURL = server.Client(), server.URL+"/bundles", server.URL
	profile, err := client.Lookup(context.Background(), pid, "04-10", "3.2.0")
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func dongleSnapshotForTest(device *jabra_DeviceInfo, profile *modelcatalog.Capabilities, read func(*jabra_DeviceInfo, choiceSettingDefinition) ([]byte, error)) []deviceSettingValue {
	var result []deviceSettingValue
	for _, value := range readChoiceSettingsSnapshot(device, choiceSettingDefinitions(settingScopeDongle), profile, nil, read) {
		copy := value
		result = append(result, deviceSettingValue{Choice: &copy})
	}
	return result
}

func TestDongleExactModelsExposeOnlyTheirChoicesThroughIPC(t *testing.T) {
	for _, pid := range []uint16{0x2e56, 0x2e57} {
		t.Run(fmt.Sprintf("%04x", pid), func(t *testing.T) {
			device := dongleDeviceForTest(pid)
			values := dongleSnapshotForTest(device, dongleProfileForTest(t, pid), func(*jabra_DeviceInfo, choiceSettingDefinition) ([]byte, error) { return []byte{1}, nil })
			api := &editablePolicyAPI{device: device, scope: "dongle", values: values}
			client := policyIPCClient(t, api)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var settings []ipc.SettingInfo
			if err := client.Call(ctx, "settings.list", map[string]string{"device": "dongle"}, &settings); err != nil || len(settings) != 2 {
				t.Fatal("missing dongle controls", settings, err)
			}
			for _, setting := range settings {
				if !setting.Editable || setting.Support.State != "available" || !slices.Contains(setting.Support.Sources, "exact-firmware-profile") || slices.Contains(setting.Support.Sources, "hardware-report") {
					t.Fatal("wrong support evidence", setting)
				}
				if setting.Device != "dongle" || !strings.Contains(setting.Help, "not tested this on hardware") {
					t.Fatal("scope or qualification warning lost", setting)
				}
			}
			mode, _ := findDeviceSettingValue(values, "preferred-connection-mode")
			if mode.Choice.Definition.ExpectedFirmware != "3.2.0" || len(mode.Choice.Definition.Choices) != 3 {
				t.Fatal("wrong model choices or missing firmware binding")
			}
			for _, invalid := range []string{"usbAdapt", "superWidebandStereo48K", "4"} {
				if _, ok := findChoiceIndex(mode.Choice.Definition, invalid); ok {
					t.Fatal("generic enum escaped model restrictions", invalid)
				}
			}
			audio, _ := findDeviceSettingValue(values, "computer-audio")
			if len(audio.Choice.Definition.Choices) != 2 {
				t.Fatal("extra audio choices")
			}
			for _, invalid := range []string{"fast", "always", "ultraLow"} {
				if _, ok := findChoiceIndex(audio.Choice.Definition, invalid); ok {
					t.Fatal("unsupported computer audio mode", invalid)
				}
			}
		})
	}
}

type settingLookupTripwire struct{ calls int }

func (r *settingLookupTripwire) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls++
	return nil, errors.New("unexpected catalog lookup")
}

func TestDongleIdentityExclusionsHappenBeforeCatalogOrTransport(t *testing.T) {
	tripwire := &settingLookupTripwire{}
	oldClient, oldCache := deviceModelClient, deviceModelCache
	deviceModelClient = modelcatalog.NewClient()
	deviceModelClient.HTTPClient = &http.Client{Transport: tripwire}
	deviceModelCache = map[string]modelCacheEntry{}
	t.Cleanup(func() { deviceModelClient, deviceModelCache = oldClient, oldCache })
	for _, change := range []func(*jabra_DeviceInfo){
		func(d *jabra_DeviceInfo) { d.productID = 0x2e50 }, func(d *jabra_DeviceInfo) { d.productID = 0x24c7 },
		func(d *jabra_DeviceInfo) { d.variantType = "01-A3" }, func(d *jabra_DeviceInfo) { d.firmwareVersion = "1.3.0" },
		func(d *jabra_DeviceInfo) { d.firmwareVersion = "3.0.13" }, func(d *jabra_DeviceInfo) { d.firmwareVersion = "" },
		func(d *jabra_DeviceInfo) { d.isDongle = false }, func(d *jabra_DeviceInfo) { d.serialNumber = "" },
		func(d *jabra_DeviceInfo) { d.instance = "" },
	} {
		device := dongleDeviceForTest(0x2e56)
		change(device)
		if rows := readSupportedChoiceSettings(device, settingScopeDongle); len(rows) != 0 || tripwire.calls != 0 {
			t.Fatal("ineligible dongle reached catalog or reader", len(rows), tripwire.calls)
		}
	}
	profile := dongleProfileForTest(t, 0x2e56)
	for _, change := range []func(*modelcatalog.Capabilities){
		func(p *modelcatalog.Capabilities) { p.ExactFirmwareProfile = false }, func(p *modelcatalog.Capabilities) { p.DeviceFirmware = "" },
		func(p *modelcatalog.Capabilities) { p.PID = 0x2e57 }, func(p *modelcatalog.Capabilities) { p.Variant = "different" },
		func(p *modelcatalog.Capabilities) { p.Firmware = "3.0.13" }, func(p *modelcatalog.Capabilities) { p.DeviceFirmware = "3.0.13" },
	} {
		candidate := *profile
		change(&candidate)
		reads := 0
		rows := dongleSnapshotForTest(dongleDeviceForTest(0x2e56), &candidate, func(*jabra_DeviceInfo, choiceSettingDefinition) ([]byte, error) { reads++; return []byte{1}, nil })
		if len(rows) != 0 || reads != 0 {
			t.Fatal("wrong catalog binding reached transport", len(rows), reads)
		}
	}
}

func TestDongleRepliesAndForgedWritesStayBound(t *testing.T) {
	profile := dongleProfileForTest(t, 0x2e56)
	for _, definition := range dongleChoiceSettingDefinitions {
		if _, supported := firstCatalogProperty(profile, definition.CatalogProperties); !supported {
			continue
		}
		for _, payload := range [][]byte{nil, {1, 0}} {
			if _, err := decodeChoiceSettingPayload(definition, payload); err == nil {
				t.Fatal("wrong reply size accepted", definition.Key, payload)
			}
		}
		unknown := []byte{4}
		if definition.Key == "computer-audio" {
			unknown[0] = 3
		}
		rows := readChoiceSettingsSnapshot(dongleDeviceForTest(0x2e56), []choiceSettingDefinition{definition}, profile, nil, func(*jabra_DeviceInfo, choiceSettingDefinition) ([]byte, error) { return unknown, nil })
		if len(rows) != 1 || rows[0].Editable || !strings.HasPrefix(choiceSettingName(rows[0]), "Unknown") || !strings.Contains(rows[0].Provenance.Reason, "does not list") {
			t.Fatal("unknown reply became a fallback edit", rows)
		}
		state := t.TempDir()
		t.Setenv("JABRIDGE_FIRMWARE_STATE_DIR", state)
		value := choiceSettingValue{Definition: definition, Editable: true, ChoiceIndex: 0, Raw: definition.Choices[0].Raw}
		for _, device := range []*jabra_DeviceInfo{nil, {instance: "headset"}, {isDongle: true, productID: 0x24c7}} {
			if err := writeChoiceSetting(device, value, 0); !errors.Is(err, ErrNotSupported) {
				t.Fatal("forged row reached transport", err)
			}
			if _, _, err := applyDeviceSettingFromText(device, deviceSettingValue{Choice: &value}, definition.Choices[0].Name); !errors.Is(err, ErrNotSupported) {
				t.Fatal("forged row became an accepted no-op", err)
			}
		}
		value.Definition.DongleExactProfile = false
		if err := writeChoiceSetting(dongleDeviceForTest(0x2e56), value, 0); !errors.Is(err, ErrNotSupported) {
			t.Fatal("unprofiled dongle choice reached transport", err)
		}
		entries, err := os.ReadDir(state)
		if err != nil || len(entries) != 0 {
			t.Fatal("refused scalar acquired a lease", entries, err)
		}
	}
	for key, property := range profile.Properties {
		property.Access = "ReadOnly"
		profile.Properties[key] = property
	}
	values := dongleSnapshotForTest(dongleDeviceForTest(0x2e56), profile, func(*jabra_DeviceInfo, choiceSettingDefinition) ([]byte, error) { return []byte{1}, nil })
	for _, setting := range values {
		if setting.editable() || !strings.Contains(setting.help(), "marks this control read-only") {
			t.Fatal("vendor read-only access was ignored", setting.help())
		}
		setting.Choice.Editable = true
		if err := writeChoiceSetting(dongleDeviceForTest(0x2e56), *setting.Choice, 0); !errors.Is(err, ErrNotSupported) {
			t.Fatal("forged editable bypassed the bound definition", err)
		}
	}
}

func TestDongleDiagnosticsExcludeOtherModelsAndTextReasonsSurvive(t *testing.T) {
	for _, device := range []*jabra_DeviceInfo{dongleDeviceForTest(0x24c7), dongleDeviceForTest(0x2e56)} {
		device.firmwareVersion = "1.3.0"
		for _, check := range diagnoseSettings(device, nil, false) {
			if check.Feature == "setting preferred-connection-mode" || check.Feature == "setting computer-audio" {
				t.Fatal("diagnostics scheduled a new read on another model", check)
			}
		}
	}
	profile := &modelcatalog.Capabilities{ExactFirmwareProfile: true, DeviceFirmware: "1.0.1", Properties: map[string]modelcatalog.Property{
		"deviceName": {Name: "deviceName", Access: "ReadOnly"},
	}}
	values := readTextSettingsSnapshot(nil, profile, func(*jabra_DeviceInfo, textSettingDefinition) (string, error) { return "Desk", nil })
	if len(values) != 1 || values[0].Editable || !strings.Contains(values[0].Provenance.Reason, "marks this control read-only") {
		t.Fatal("text permission had no explanation", values)
	}
}

func TestDongleCompletionTokensAndHistoryRoundTrip(t *testing.T) {
	recorder := &history.Recorder{Dir: filepath.Join(t.TempDir(), "history")}
	for _, definition := range dongleChoiceSettingDefinitions {
		history.RegisterSettings(definition.Key)
		if err := recorder.Append(history.Event{Component: "device", Action: "setting-readback", Setting: definition.Key, Phase: "ok"}); err != nil {
			t.Fatal(err)
		}
		var tokens []string
		for _, choice := range definition.Choices {
			token := choiceValueToken(choice.Name)
			index, ok := findChoiceIndex(definition, token)
			if !ok || definition.Choices[index].Raw != choice.Raw {
				t.Fatal("command token changed the wire value", token)
			}
			tokens = append(tokens, token)
		}
		for _, command := range []struct{ words, index string }{
			{"jabridge settings set dongle." + definition.Key + " ''", "4"},
			{"jabridge ipc set dongle " + definition.Key + " ''", "5"},
		} {
			cmd := exec.Command("bash", "--noprofile", "--norc")
			cmd.Stdin = strings.NewReader(completion.JabridgeBash + "\nCOMP_WORDS=(" + command.words + ")\nCOMP_CWORD=" + command.index + "\n_jabridge_completion\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n")
			output, err := cmd.CombinedOutput()
			if err != nil || !slices.Equal(strings.Fields(string(output)), tokens) {
				t.Fatal("completion did not match actual command tokens", string(output), err, tokens)
			}
		}
	}
	events, _, err := recorder.Read(10)
	if err != nil || len(events) != len(dongleChoiceSettingDefinitions) {
		t.Fatal("history dropped a known key", events, err)
	}
	for _, event := range events {
		if event.Setting == "" {
			t.Fatal("history dropped a known key", events)
		}
	}
}
