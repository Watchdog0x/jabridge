package modelcatalog

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// ProfileEvidence is public metadata collected for development. It must never
// select a device, authorize a write, or enter the live settings cache.
type ProfileEvidence struct {
	PID                 uint16
	Variant, Firmware   string
	Protocols           []string
	FirmwareProtocol    *int
	Definitions         []Definition
	Sections            map[string]int
	OmittedDefinitions  int
	Failure             string
	UninspectedSections []string
}

type Definition struct {
	Section, ID, Name, Kind, Access, Group string
	Properties, Choices                    []string
	Restart                                *bool
	OmittedChoices                         int
	Limits                                 map[string]string
}

type DeviceEvidence struct {
	PID             uint16
	Profiles        []ProfileEvidence
	TotalProfiles   int
	OmittedProfiles int
}

const maxInspectionProfiles = 32
const maxInspectionDefinitions = 512

// InspectProfiles collects the latest non-revoked profile for every matching
// variant, even when no management transport or device variant can be read.
// The caller supplies a time budget; all omissions and fetch failures remain
// visible. Network work is bounded to 32 schemas in one report.
func (client *Client) InspectProfiles(ctx context.Context, pids []uint16) ([]DeviceEvidence, error) {
	products, _, err := client.loadProducts(ctx)
	if err != nil {
		return nil, err
	}
	requested := map[uint16]bool{}
	for _, pid := range pids {
		requested[pid] = true
	}
	byPID := map[uint16][]ProfileEvidence{}
	seen := map[string]bool{}
	for _, product := range products {
		versions := firmwareVersionsNewestFirst(product.FirmwareReleases)
		latest := ""
		if len(versions) > 0 {
			latest = versions[0]
		}
		for _, variant := range product.Variants {
			if variant.VendorID != 0x0b0e || variant.ProductID < 0 || variant.ProductID > 65535 || !requested[uint16(variant.ProductID)] {
				continue
			}
			key := fmt.Sprintf("%04x:%s:%s", variant.ProductID, variant.VariantType, latest)
			if seen[key] {
				continue
			}
			seen[key] = true
			pid := uint16(variant.ProductID)
			byPID[pid] = append(byPID[pid], ProfileEvidence{PID: pid, Variant: variant.VariantType, Firmware: latest, Protocols: product.SupportedProtocols, FirmwareProtocol: variant.FirmwareProtocol})
		}
	}
	var ids []int
	for pid := range requested {
		ids = append(ids, int(pid))
	}
	sort.Ints(ids)
	var results []DeviceEvidence
	fetched := 0
	for _, id := range ids {
		pid := uint16(id)
		profiles := byPID[pid]
		sort.Slice(profiles, func(i, j int) bool { return profiles[i].Variant < profiles[j].Variant })
		result := DeviceEvidence{PID: pid, TotalProfiles: len(profiles)}
		for _, profile := range profiles {
			if fetched >= maxInspectionProfiles || ctx.Err() != nil {
				result.OmittedProfiles++
				continue
			}
			fetched++
			if !safeCatalogVariant(strings.ToUpper(profile.Variant)) || profile.Firmware == "" {
				profile.Failure = "missing or invalid variant/firmware identifier"
			} else {
				endpoint := strings.TrimRight(client.ModelsBaseURL, "/") + fmt.Sprintf("/vendors/2830/products/%d/variants/%s/firmware-versions/%s/device-models/%s/schema-versions/%s.json",
					pid, url.PathEscape(profile.Variant), url.PathEscape(profile.Firmware), url.PathEscape(client.ModelName), url.PathEscape(client.SchemaVersion))
				var schema struct {
					Device map[string]any `json:"device"`
				}
				if err := client.getJSON(ctx, endpoint, &schema); err != nil {
					profile.Failure = "schema unavailable (network, timeout or missing profile)"
				} else if schema.Device == nil {
					profile.Failure = "schema has no device definition"
				} else if !profileIdentityMatches(schema.Device, profile) {
					profile.Failure = "schema identity does not match requested PID/variant"
				} else {
					profile.Definitions, profile.Sections, profile.OmittedDefinitions = inspectDefinitions(schema.Device)
					for key := range schema.Device {
						switch key {
						case "settings", "compositeSettings", "commands", "compositeCommands", "attributes", "deviceEvents", "telemetry", "productId", "vendorId", "variantType", "sdk", "entityId", "clientProperties":
						default:
							profile.UninspectedSections = append(profile.UninspectedSections, EvidenceText(key))
						}
					}
					sort.Strings(profile.UninspectedSections)
				}
			}
			result.Profiles = append(result.Profiles, profile)
		}
		results = append(results, result)
	}
	return results, nil
}

func profileIdentityMatches(device map[string]any, profile ProfileEvidence) bool {
	vendor, vok := device["vendorId"].(float64)
	pid, pok := device["productId"].(float64)
	variant, tok := device["variantType"].(string)
	return vok && pok && tok && vendor == 0x0b0e && pid == float64(profile.PID) && strings.EqualFold(variant, profile.Variant)
}

func inspectDefinitions(device map[string]any) ([]Definition, map[string]int, int) {
	var definitions []Definition
	sections := map[string]int{}
	omitted := 0
	visited := 0
	var flatten func(any, string, int) []map[string]any
	flatten = func(value any, group string, depth int) []map[string]any {
		if depth > 16 || visited >= maxInspectionDefinitions {
			omitted++
			return nil
		}
		var objects []map[string]any
		switch typed := value.(type) {
		case []any:
			for _, child := range typed {
				if len(objects) >= maxInspectionDefinitions {
					omitted++
					continue
				}
				objects = append(objects, flatten(child, group, depth+1)...)
			}
		case map[string]any:
			visited++
			if name, ok := typed["groupName"].(string); ok {
				group = name
			}
			object := map[string]any{}
			for key, value := range typed {
				object[key] = value
			}
			object["groupName"] = group
			objects = append(objects, object)
			for _, key := range []string{"settings", "groups", "children", "items", "commands", "attributes", "deviceEvents"} {
				if child, ok := typed[key]; ok {
					objects = append(objects, flatten(child, group, depth+1)...)
				}
			}
		default:
			omitted++
		}
		return objects
	}
	for _, section := range []string{"settings", "compositeSettings", "commands", "compositeCommands", "attributes", "deviceEvents", "telemetry"} {
		entries, exists := device[section].([]any)
		if !exists {
			continue
		}
		sections[section] = len(entries)
		for _, object := range flatten(entries, "", 0) {
			if len(definitions) >= maxInspectionDefinitions {
				omitted++
				continue
			}
			text := func(key string) string { value, _ := object[key].(string); return EvidenceText(value) }
			definition := Definition{Section: section, Name: text("name"), Kind: text("type"), Access: text("settingAccess"), Group: text("groupName")}
			for _, key := range []string{"minimum", "maximum", "minValue", "maxValue", "step", "minLength", "maxLength"} {
				if value, ok := object[key].(float64); ok {
					if definition.Limits == nil {
						definition.Limits = map[string]string{}
					}
					definition.Limits[key] = fmt.Sprint(value)
				}
			}
			for _, key := range []string{"settingId", "commandId", "attributeId", "id"} {
				if value := text(key); value != "" {
					definition.ID = value
					break
				}
			}
			for _, name := range propertyNames(object) {
				if len(definition.Properties) < 32 {
					definition.Properties = append(definition.Properties, EvidenceText(name))
				}
			}
			for _, key := range []string{"property", "deviceEventType"} {
				if value := text(key); value != "" {
					definition.Properties = append(definition.Properties, value)
				}
			}
			if restart, ok := object["requiresRestart"].(bool); ok {
				definition.Restart = &restart
			}
			choices, _ := object["possibleValues"].([]any)
			for _, choice := range choices {
				if len(definition.Choices) >= 64 {
					definition.OmittedChoices++
					continue
				}
				item, ok := choice.(map[string]any)
				if !ok {
					definition.OmittedChoices++
					continue
				}
				switch value := item["value"].(type) {
				case string:
					definition.Choices = append(definition.Choices, EvidenceText(value))
				case bool, float64:
					definition.Choices = append(definition.Choices, fmt.Sprint(value))
				default:
					definition.OmittedChoices++
				}
			}
			definitions = append(definitions, definition)
		}
	}
	return definitions, sections, omitted
}

// EvidenceText allows bounded printable catalog labels only. Neither raw
// schema documents nor default/current values enter a support report.
func EvidenceText(value string) string {
	var out strings.Builder
	for _, char := range value {
		if out.Len() >= 120 {
			out.WriteString("...")
			break
		}
		if char >= 32 && char < 127 {
			out.WriteRune(char)
		} else {
			out.WriteByte('?')
		}
	}
	return out.String()
}
