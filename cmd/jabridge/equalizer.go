package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

const (
	equalizerBands      = 5
	equalizerMinimumRaw = -2160 // -36 dB, vendor gain scale 60.
	equalizerMaximumRaw = 720   // +12 dB before the model's band limit.
	equalizerStepRaw    = 30    // UI choice: half a dB, encoded exactly.
)

type equalizerBandProfile struct {
	frequency uint16
	q         uint16
	maximum   byte
}

type equalizerProfile [equalizerBands]equalizerBandProfile

type equalizerBandCodec struct {
	band    int
	profile equalizerProfile
}

func isEqualizerBand(key string) bool {
	switch key {
	case "equalizer-60hz", "equalizer-250hz", "equalizer-1khz", "equalizer-4khz", "equalizer-7.6khz":
		return true
	}
	return false
}

func equalizerDefinitions() []choiceSettingDefinition {
	keys := []string{"60hz", "250hz", "1khz", "4khz", "7.6khz"}
	labels := []string{"60 Hz", "250 Hz", "1 kHz", "4 kHz", "7.6 kHz"}
	var result []choiceSettingDefinition
	for band := range equalizerBands {
		result = append(result, choiceSettingDefinition{
			Key: "equalizer-" + keys[band], Label: "Equalizer " + labels[band], Scope: settingScopeHeadset,
			Help:  "Adjust this band in 0.5 dB steps. Other bands keep their current values.",
			Class: gnpClassConfig, Op: 0x7d, Request: []byte{0}, Writable: true,
			CatalogProperties: []string{"equalizerConfig"}, Codec: equalizerBandCodec{band: band},
		})
	}
	return result
}

func fixedModelValue(value string, scale float64, minimum, maximum int) (int, error) {
	if len(value) == 0 || len(value) > 32 {
		return 0, errors.New("invalid equalizer model value")
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, errors.New("invalid equalizer model value")
	}
	raw := number * scale
	if math.Trunc(raw) != raw || raw < float64(minimum) || raw > float64(maximum) {
		return 0, errors.New("equalizer model value cannot be encoded exactly")
	}
	return int(raw), nil
}

func equalizerModelProfile(property modelcatalog.Property) (equalizerProfile, error) {
	var profile equalizerProfile
	if len(property.CompositeSettings) != 21 {
		return profile, errors.New("equalizer layout is not supported on this firmware")
	}
	seen := map[string]bool{}
	for _, member := range property.CompositeSettings {
		if member.ID == "" || member.SetPath == "" || member.IsStatic == nil || seen[member.SetPath] {
			return profile, errors.New("equalizer model is incomplete or ambiguous")
		}
		seen[member.SetPath] = true
		if *member.IsStatic {
			if len(member.Choices) != 1 || member.Choices[0].Key != "SET" || member.GetPath != "" {
				return profile, errors.New("equalizer model is missing a fixed value")
			}
			value := member.Choices[0].Value
			if member.SetPath == "masterGain" {
				master, err := fixedModelValue(value, 60, equalizerMinimumRaw, equalizerMaximumRaw)
				if member.Kind != "Decimal" || err != nil || master != 0 {
					return profile, errors.New("unsupported equalizer master gain profile")
				}
				continue
			}
			var band int
			var field string
			parts := strings.Split(member.SetPath, "/")
			if len(parts) != 3 || parts[0] != "bands" {
				return profile, errors.New("unknown equalizer model field")
			}
			band, err := strconv.Atoi(parts[1])
			if err != nil || band < 0 || band >= equalizerBands {
				return profile, errors.New("unknown equalizer band")
			}
			field = parts[2]
			switch field {
			case "centerFrequency":
				raw, err := fixedModelValue(value, 3, 60, 60000)
				if err != nil || member.Kind != "Integer" {
					return profile, errors.New("invalid equalizer frequency profile")
				}
				profile[band].frequency = uint16(raw)
			case "_Q":
				raw, err := fixedModelValue(value, 4096, 1024, 32768)
				if err != nil || member.Kind != "Decimal" {
					return profile, errors.New("invalid equalizer bandwidth profile")
				}
				profile[band].q = uint16(raw)
			case "maxGain":
				raw, err := fixedModelValue(value, 4, 1, 48)
				if err != nil || member.Kind != "Decimal" {
					return profile, errors.New("invalid equalizer gain limit")
				}
				profile[band].maximum = byte(raw)
			default:
				return profile, errors.New("unknown fixed equalizer field")
			}
		} else {
			valid := false
			for band := range equalizerBands {
				path := fmt.Sprintf("bands/%d/gain", band)
				if member.SetPath == path && member.GetPath == path && member.ID == fmt.Sprintf("EQUALIZER_BAND_%d_GAIN", band+1) && member.Kind == "Number" && len(member.Choices) == 0 {
					valid = true
				}
			}
			if !valid {
				return profile, errors.New("unsupported editable equalizer field")
			}
		}
	}
	if !seen["masterGain"] {
		return profile, errors.New("missing equalizer master profile")
	}
	wantedFrequencies := [5]uint16{180, 750, 3000, 12000, 22800}
	for band, fixed := range profile {
		if fixed.frequency != wantedFrequencies[band] || fixed.q == 0 || fixed.maximum == 0 || !seen[fmt.Sprintf("bands/%d/gain", band)] {
			return profile, errors.New("equalizer layout is not supported on this firmware")
		}
	}
	return profile, nil
}

func (codec equalizerBandCodec) BindProperty(property modelcatalog.Property) (choicePayloadCodec, []settingChoice, error) {
	if codec.band < 0 || codec.band >= equalizerBands {
		return nil, nil, errors.New("unknown equalizer band")
	}
	profile, err := equalizerModelProfile(property)
	if err != nil {
		return nil, nil, err
	}
	codec.profile = profile
	maximum := min(equalizerMaximumRaw, int(profile[codec.band].maximum)*15)
	var choices []settingChoice
	for raw := equalizerMinimumRaw; raw <= maximum; raw += equalizerStepRaw {
		value := strconv.FormatFloat(float64(raw)/60, 'f', -1, 64)
		choices = append(choices, settingChoice{Name: value + " dB", Raw: byte((raw - equalizerMinimumRaw) / equalizerStepRaw), CatalogValue: value})
	}
	return codec, choices, nil
}

func (codec equalizerBandCodec) validateReply(payload []byte) error {
	if codec.band < 0 || codec.band >= equalizerBands || codec.profile[codec.band].frequency == 0 || len(payload) != 38 || payload[0] != equalizerBands {
		return errors.New("equalizer reply has an unsupported layout")
	}
	master := int(int16(binary.BigEndian.Uint16(payload[1:3])))
	if master < equalizerMinimumRaw || master > equalizerMaximumRaw {
		return errors.New("equalizer master gain is outside the vendor range")
	}
	for band, fixed := range codec.profile {
		start := 3 + band*7
		gain := int(int16(binary.BigEndian.Uint16(payload[start+3 : start+5])))
		if payload[start] != fixed.maximum || binary.BigEndian.Uint16(payload[start+1:start+3]) != fixed.frequency || binary.BigEndian.Uint16(payload[start+5:start+7]) != fixed.q {
			return errors.New("equalizer bands do not match this firmware profile")
		}
		if gain < equalizerMinimumRaw || gain > equalizerMaximumRaw {
			return errors.New("equalizer band gain is outside the vendor range")
		}
	}
	return nil
}

func (codec equalizerBandCodec) Decode(payload []byte) (byte, string, error) {
	if err := codec.validateReply(payload); err != nil {
		return 0, "", err
	}
	start := 6 + codec.band*7
	raw := int(int16(binary.BigEndian.Uint16(payload[start : start+2])))
	display := strconv.FormatFloat(float64(raw)/60, 'f', 2, 64) + " dB"
	if raw%equalizerStepRaw != 0 {
		return 255, display, nil
	}
	return byte((raw - equalizerMinimumRaw) / equalizerStepRaw), display, nil
}

func (codec equalizerBandCodec) Encode(current []byte, choice byte) ([]byte, error) {
	if err := codec.validateReply(current); err != nil {
		return nil, err
	}
	raw := int(choice)*equalizerStepRaw + equalizerMinimumRaw
	maximum := min(equalizerMaximumRaw, int(codec.profile[codec.band].maximum)*15)
	if raw < equalizerMinimumRaw || raw > maximum {
		return nil, errors.New("equalizer value is outside this band's allowed range")
	}
	result := []byte{equalizerBands, 0, current[1], current[2]}
	for band := range equalizerBands {
		start := 3 + band*7
		result = append(result, current[start+1:start+7]...)
	}
	start := 6 + codec.band*6
	binary.BigEndian.PutUint16(result[start:start+2], uint16(int16(raw)))
	return result, nil
}

func (codec equalizerBandCodec) Verify(written, read []byte) error {
	if err := codec.validateReply(read); err != nil {
		return err
	}
	if len(written) != 34 || written[0] != equalizerBands || written[1] != 0 || !bytes.Equal(written[2:4], read[1:3]) {
		return errors.New("equalizer master gain did not match the requested change")
	}
	for band := range equalizerBands {
		start := 3 + band*7
		if !bytes.Equal(read[start+1:start+7], written[4+band*6:10+band*6]) {
			return errors.New("headset returned different equalizer values; the change was not confirmed")
		}
	}
	return nil
}

func (equalizerBandCodec) ParseChoice(value string) (byte, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(value, "-db"), "db"))
	raw, err := fixedModelValue(value, 60, equalizerMinimumRaw, equalizerMaximumRaw)
	if err != nil || raw%equalizerStepRaw != 0 {
		return 0, errors.New("choose an equalizer value in 0.5 dB steps")
	}
	return byte((raw - equalizerMinimumRaw) / equalizerStepRaw), nil
}
