package firmware

import (
	"slices"
	"testing"
)

func TestCSRLanguageOptionPreservesInstallArguments(t *testing.T) {
	for _, args := range [][]string{{"firmware.zip", "--language", "German", HardwareWriteFlag}, {"--language=0x0407", HardwareWriteFlag, "firmware.zip"}} {
		remaining, language, err := extractCSRLanguageOption(args)
		if err != nil || language != 0x407 {
			t.Fatal(language, err)
		}
		file, accepted, err := parseInstallArgs(remaining)
		if err != nil || file != "firmware.zip" || !accepted {
			t.Fatal("language option damaged normal risk/file parsing", file, accepted, err)
		}
	}
	args := []string{"file.zip", HardwareWriteFlag}
	remaining, language, err := extractCSRLanguageOption(args)
	if err != nil || language != 0 || !slices.Equal(remaining, args) {
		t.Fatal("ordinary install arguments changed")
	}
	for _, args := range [][]string{{"--language"}, {"--language=invalid"}, {"--language=German", "--language=English"}, {"--language", "--i-accept-risk"}} {
		if _, _, err := extractCSRLanguageOption(args); err == nil {
			t.Fatal("invalid language options were accepted", args)
		}
	}
}

func TestCSRLanguageExperimentalGateDefaultsOff(t *testing.T) {
	t.Setenv(CSRLanguageEnv, "")
	if checkCSRLanguageOptIn() == nil {
		t.Fatal("language hardware path enabled by default")
	}
	t.Setenv(CSRLanguageEnv, "yes")
	if checkCSRLanguageOptIn() == nil {
		t.Fatal("ambiguous acknowledgement accepted")
	}
	t.Setenv(CSRLanguageEnv, CSRLanguageAcknowledgement)
	if err := checkCSRLanguageOptIn(); err != nil {
		t.Fatal(err)
	}
}
