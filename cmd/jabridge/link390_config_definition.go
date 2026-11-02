package main

import (
	"errors"
	"fmt"
)

type link390ConfigDefinition struct {
	Key                  string
	Class, Op            byte
	Request, WritePrefix []byte
	Choices              []settingChoice
}

func link390ConfigDefinitionForKey(key string) (link390ConfigDefinition, bool) {
	switch key {
	case "wireless-range":
		return link390ConfigDefinition{Key: key, Class: gnpClassConfig, Op: 0x0e, Choices: []settingChoice{
			{Name: "Normal", Raw: 1, CatalogValue: "normal"},
			{Name: "Low", Raw: 2, CatalogValue: "low"},
			{Name: "Very low", Raw: 3, CatalogValue: "veryLow"},
		}}, true
	case "softphone-integration":
		return link390ConfigDefinition{Key: key, Class: gnpClassConfig, Op: 0x4c, Choices: []settingChoice{{Name: "Off", Raw: 0, CatalogValue: "false"}, {Name: "On", Raw: 1, CatalogValue: "true"}}}, true
	default:
		return link390ConfigDefinition{}, false
	}
}

func (d link390ConfigDefinition) valid() bool {
	canonical, ok := link390ConfigDefinitionForKey(d.Key)
	return ok && d.Class == canonical.Class && d.Op == canonical.Op && len(d.Request) == 0 && len(d.WritePrefix) == 0
}

func (d link390ConfigDefinition) allows(value byte) bool {
	canonical, ok := link390ConfigDefinitionForKey(d.Key)
	if !ok {
		return false
	}
	for _, choice := range canonical.Choices {
		if choice.Raw == value {
			return true
		}
	}
	return false
}

func link390ConfigValueName(key string, value byte) string {
	d, _ := link390ConfigDefinitionForKey(key)
	for _, choice := range d.Choices {
		if choice.Raw == value {
			return choice.Name
		}
	}
	return fmt.Sprintf("Unknown (%d)", value)
}

func decodeLink390ConfigValue(key string, payload []byte) (byte, error) {
	d, ok := link390ConfigDefinitionForKey(key)
	if !ok || len(payload) != 1 || !d.allows(payload[0]) {
		return 0, errors.New("configuration reply has an unsupported value or length")
	}
	return payload[0], nil
}

func link390ConfigPayload(d link390ConfigDefinition, value byte) ([]byte, error) {
	if !d.valid() || !d.allows(value) {
		return nil, ErrNotSupported
	}
	return []byte{value}, nil
}
