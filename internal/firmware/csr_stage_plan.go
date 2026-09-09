package firmware

import (
	"errors"
	"strings"
)

type csrPlannedStage struct {
	Kind               string
	Files              []GnVFile
	RequiresConfigExit bool
}

// selected must already contain exactly the intended language/model images.
// Change decisions come from verified device metadata; unknown state is not
// substituted with a default. This plans stages, not reconnect/commit behavior.
func planCSRStages(protocol int, selected []GnVFile, firmwareChanged, languageChanged bool) ([]csrPlannedStage, error) {
	if len(selected) == 0 {
		return nil, errors.New("no CSR images selected")
	}
	if protocol == 16 {
		return []csrPlannedStage{{Kind: "firmware", Files: append([]GnVFile(nil), selected...)}}, nil
	}
	if protocol != 17 {
		return nil, errors.New("not an extended CSR protocol")
	}
	var language, firmware []GnVFile
	for _, file := range selected {
		content := strings.ToLower(strings.TrimSpace(file.Content))
		switch {
		case content == "langpack" || content == "voiceprompt" || content == "tunepack" || content == "" && file.Language.ID != "":
			language = append(language, file)
		case content == "firmware" || content == "":
			firmware = append(firmware, file)
		default:
			return nil, errors.New("unknown CSR stage content")
		}
	}
	var stages []csrPlannedStage
	if len(language) > 0 && (languageChanged || firmwareChanged) {
		stages = append(stages, csrPlannedStage{Kind: "language", Files: language, RequiresConfigExit: true})
	}
	if len(firmware) > 0 && firmwareChanged {
		stages = append(stages, csrPlannedStage{Kind: "firmware", Files: firmware, RequiresConfigExit: true})
	}
	if len(stages) == 0 {
		// Explicit reinstall/no separate applicable stages uses the original
		// selection. This must not be interpreted as an automatic update request.
		stages = []csrPlannedStage{{Kind: "combined", Files: append([]GnVFile(nil), selected...), RequiresConfigExit: true}}
	}
	return stages, nil
}
