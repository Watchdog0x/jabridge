package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

// Structured settings still appear as ordinary choices to CLI, IPC and TUI.
// Their IDs describe choices, not bytes to send. Encode always receives a
// fresh device reply, and Verify checks the complete resulting setting.
type choicePayloadCodec interface {
	BindProperty(modelcatalog.Property) (choicePayloadCodec, []settingChoice, error)
	Decode([]byte) (byte, string, error)
	Encode(current []byte, choice byte) ([]byte, error)
	Verify(written, read []byte) error
}

type noiseCycleCodec struct{ modes byte }

func (noiseCycleCodec) BindProperty(property modelcatalog.Property) (choicePayloadCodec, []settingChoice, error) {
	var allowed byte
	modes := map[string]struct {
		name string
		bit  byte
	}{
		"ANC_SOUND_MODE_LOOP_OFF":         {"off", 1},
		"ANC_SOUND_MODE_LOOP_HEARTHROUGH": {"hearThrough", 2},
		"ANC_SOUND_MODE_LOOP_ANC":         {"anc", 4},
	}
	if len(property.CompositeSettings) < 1 || len(property.CompositeSettings) > 3 {
		return nil, nil, errors.New("noise button cycle has no complete model profile")
	}
	for _, member := range property.CompositeSettings {
		mode, known := modes[member.ID]
		if !known || allowed&mode.bit != 0 || member.Kind != "Enum" || member.IsStatic == nil || *member.IsStatic || member.GetPath != "/-" || member.SetPath != "/-" || len(member.Choices) != 2 {
			return nil, nil, errors.New("unsupported noise button cycle profile")
		}
		on, off := false, false
		for _, choice := range member.Choices {
			if strings.EqualFold(choice.Key, "True") && choice.Value == mode.name {
				on = true
			} else if strings.EqualFold(choice.Key, "False") && choice.Value == "" {
				off = true
			} else {
				return nil, nil, errors.New("unsupported noise cycle mode value")
			}
		}
		if !on || !off {
			return nil, nil, errors.New("noise cycle mode is missing an on/off choice")
		}
		allowed |= mode.bit
	}
	var choices []settingChoice
	for mask := byte(1); mask < 8; mask++ {
		if mask&allowed != mask {
			continue
		}
		var labels, values []string
		for index, label := range []string{"Off", "HearThrough", "ANC"} {
			if mask&(1<<index) != 0 {
				labels = append(labels, label)
				values = append(values, strings.ToLower(label))
			}
		}
		choices = append(choices, settingChoice{Name: strings.Join(labels, ", "), Raw: mask, CatalogValue: strings.Join(values, ",")})
	}
	return noiseCycleCodec{modes: allowed}, choices, nil
}

func (codec noiseCycleCodec) Decode(payload []byte) (byte, string, error) {
	if codec.modes == 0 || len(payload) < 2 || len(payload) > 4 || int(payload[0]) != len(payload)-1 {
		return 0, "", errors.New("invalid noise button cycle length or model profile")
	}
	var mask byte
	for _, mode := range payload[1:] {
		if mode > 2 {
			return 0, "", errors.New("unknown noise button mode")
		}
		bit := byte(1 << mode)
		if bit&codec.modes == 0 || mask&bit != 0 {
			return 0, "", errors.New("unsupported or repeated noise button mode")
		}
		mask |= bit
	}
	return mask, "", nil
}

func (codec noiseCycleCodec) Encode(current []byte, wanted byte) ([]byte, error) {
	if _, _, err := codec.Decode(current); err != nil {
		return nil, err
	}
	if wanted == 0 || wanted&codec.modes != wanted {
		return nil, errors.New("choose at least one supported noise button mode")
	}
	result := []byte{0}
	var kept byte
	for _, mode := range current[1:] {
		bit := byte(1 << mode)
		if wanted&bit != 0 {
			result = append(result, mode)
			kept |= bit
		}
	}
	// Existing order stays intact; new modes follow the vendor enum order.
	for mode := byte(0); mode <= 2; mode++ {
		bit := byte(1 << mode)
		if wanted&bit != 0 && kept&bit == 0 {
			result = append(result, mode)
		}
	}
	result[0] = byte(len(result) - 1)
	return result, nil
}

func (codec noiseCycleCodec) Verify(written, read []byte) error {
	if _, _, err := codec.Decode(read); err != nil {
		return err
	}
	if !bytes.Equal(written, read) {
		return errors.New("headset returned a different noise button cycle")
	}
	return nil
}

func noiseCycleSelection(value string) (byte, error) {
	var mask byte
	for _, name := range strings.Split(value, ",") {
		var bit byte
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "off":
			bit = 1
		case "hearthrough":
			bit = 2
		case "anc":
			bit = 4
		default:
			return 0, fmt.Errorf("unknown noise button mode %q", name)
		}
		if mask&bit != 0 {
			return 0, errors.New("choose each noise button mode only once")
		}
		mask |= bit
	}
	return mask, nil
}
