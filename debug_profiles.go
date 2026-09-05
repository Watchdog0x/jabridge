package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func writeProfileEvidence(out *bytes.Buffer, pids []uint16) {
	fmt.Fprintln(out, "\nPublic profiles for detected models (development evidence):")
	if len(pids) == 0 {
		fmt.Fprintln(out, "No detected product IDs to look up.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	results, err := deviceModelClient.InspectProfiles(ctx, pids)
	if err != nil {
		fmt.Fprintln(out, "UNAVAILABLE: public catalog lookup:", diagnosticError(err))
		return
	}
	fmt.Fprintln(out, "Profiles are candidates, not device-read values. Each uses its latest published firmware; no candidate is applied to the device.")
	for _, result := range results {
		fmt.Fprintf(out, "USB/HID 0b0e:%04x: %d matching profiles; omitted=%d (32-schema/25-second budget)\n", result.PID, result.TotalProfiles, result.OmittedProfiles)
		if result.TotalProfiles == 0 {
			fmt.Fprintln(out, "  UNAVAILABLE: PID absent from current catalog; HID evidence still applies.")
		}
		for _, profile := range result.Profiles {
			writeCandidateProfile(out, profile)
		}
	}
}

func writeCandidateProfile(out *bytes.Buffer, profile modelcatalog.ProfileEvidence) {
	fmt.Fprintf(out, "  Candidate variant=%s firmware=%s", modelcatalog.EvidenceText(profile.Variant), modelcatalog.EvidenceText(profile.Firmware))
	if profile.FirmwareProtocol != nil {
		fmt.Fprintf(out, " firmware-protocol=%d", *profile.FirmwareProtocol)
	}
	for _, protocol := range profile.Protocols {
		fmt.Fprintf(out, " transport=%s", modelcatalog.EvidenceText(protocol))
	}
	fmt.Fprintln(out)
	if profile.Failure != "" {
		fmt.Fprintln(out, "    UNAVAILABLE:", profile.Failure)
		return
	}
	for _, section := range []string{"settings", "compositeSettings", "commands", "compositeCommands", "attributes", "deviceEvents", "telemetry"} {
		if count, ok := profile.Sections[section]; ok {
			fmt.Fprintf(out, "    %s: %d published entries\n", section, count)
		} else {
			fmt.Fprintf(out, "    %s: not published\n", section)
		}
	}
	if len(profile.UninspectedSections) > 0 {
		fmt.Fprintln(out, "    NOT INSPECTED schema sections:", profile.UninspectedSections)
	}
	for _, entry := range profile.Definitions {
		fmt.Fprintf(out, "    %s id=%s name=%s type=%s properties=%v", entry.Section, entry.ID, entry.Name, entry.Kind, entry.Properties)
		if entry.Section == "settings" || entry.Section == "compositeSettings" {
			restart, access := "unknown", entry.Access
			if entry.Restart != nil {
				restart = fmt.Sprint(*entry.Restart)
			}
			if access == "" {
				access = "unknown"
			}
			fmt.Fprintf(out, " choices=%v access=%s restart=%s", entry.Choices, access, restart)
			if entry.Group != "" {
				fmt.Fprintf(out, " group=%s", entry.Group)
			}
			mappings := knownDiagnosticMappings(entry.Properties)
			if len(mappings) == 0 {
				fmt.Fprint(out, " native-mapping=missing")
			} else {
				fmt.Fprintf(out, " native-definitions=%s (requires transport/model validation)", strings.Join(mappings, ","))
			}
		}
		if entry.OmittedChoices > 0 {
			fmt.Fprintf(out, " omitted-choices=%d", entry.OmittedChoices)
		}
		if len(entry.Limits) > 0 {
			fmt.Fprintf(out, " limits=%v", entry.Limits)
		}
		fmt.Fprintln(out)
	}
	if profile.OmittedDefinitions > 0 {
		fmt.Fprintf(out, "    INCOMPLETE: %d definitions omitted by parser/budget\n", profile.OmittedDefinitions)
	}
}

func knownDiagnosticMappings(properties []string) []string {
	var result []string
	match := func(key string, names []string, class, op byte) {
		for _, name := range names {
			for _, property := range properties {
				if name == property {
					result = append(result, fmt.Sprintf("%s(query=%02x/%02x)", key, class, op))
					return
				}
			}
		}
	}
	for _, scope := range []settingScope{settingScopeDongle, settingScopeHeadset} {
		for _, definition := range settingDefinitions(scope) {
			match(definition.Key, definition.CatalogProperties, definition.Class, definition.Op)
		}
		for _, definition := range choiceSettingDefinitions(scope) {
			match(definition.Key, definition.CatalogProperties, definition.Class, definition.Op)
		}
	}
	for _, definition := range headsetTextSettingDefinitions {
		match(definition.Key, definition.CatalogProperties, definition.Class, definition.Op)
	}
	return result
}
