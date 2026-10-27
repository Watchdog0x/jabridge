package firmware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const CSRLanguageEnv = "JABRIDGE_ENABLE_CSR17_LANGUAGE"
const CSRLanguageAcknowledgement = "I_ACCEPT_UNTESTED_LANGUAGE_UPDATE"

func checkCSRLanguageOptIn() error {
	if os.Getenv(CSRLanguageEnv) != CSRLanguageAcknowledgement {
		return fmt.Errorf("voice language installation is experimental; testing requires %s=%s", CSRLanguageEnv, CSRLanguageAcknowledgement)
	}
	return nil
}

func extractCSRLanguageOption(args []string) ([]string, uint16, error) {
	var remaining []string
	var language uint16
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--language" || strings.HasPrefix(argument, "--language=") {
			if language != 0 {
				return nil, 0, errors.New("choose one voice language")
			}
			value := strings.TrimPrefix(argument, "--language=")
			if argument == "--language" {
				index++
				if index >= len(args) {
					return nil, 0, errors.New("--language needs a language name or ID")
				}
				value = args[index]
			}
			parsed, err := parseCSRLanguage(value)
			if err != nil {
				return nil, 0, err
			}
			language = parsed
		} else {
			remaining = append(remaining, argument)
		}
	}
	return remaining, language, nil
}

type VoiceLanguage struct {
	ID      uint16
	Name    string
	Version string
}

type VoiceLanguageStatus struct {
	ID          uint16
	Name        string
	Firmware    string
	PackVersion string
}

func ReadVoiceLanguage(ctx context.Context, pid uint16) (VoiceLanguageStatus, error) {
	if pid != 0x2549 {
		return VoiceLanguageStatus{}, errors.New("connect the supported Evolve3 65 directly by USB")
	}
	lease, err := AcquireDeviceAccess()
	if err != nil {
		return VoiceLanguageStatus{}, err
	}
	defer func() { _ = lease.Close() }()
	devices, err := enumerateBoundUSB()
	if err != nil {
		return VoiceLanguageStatus{}, err
	}
	device, err := selectInteractiveUSBTarget(devices, pid)
	if err != nil {
		return VoiceLanguageStatus{}, err
	}
	connection, err := openCSRLanguageConnection(ctx, device)
	if err != nil {
		return VoiceLanguageStatus{}, err
	}
	defer func() { _ = connection.Close() }()
	return VoiceLanguageStatus{ID: connection.Identity.Language, Name: csrLanguageName(connection.Identity.Language), Firmware: connection.Identity.Version, PackVersion: connection.TunesVersion}, nil
}

// VoiceLanguages inspects only a file. It neither authorizes nor starts a write.
func VoiceLanguages(path string) ([]VoiceLanguage, error) {
	snapshot, err := freezeFirmwareFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = snapshot.Close() }()
	manifest, contents, err := parseGnVArchive(snapshot.path)
	if err != nil {
		return nil, err
	}
	var result []VoiceLanguage
	for _, file := range manifest.Files {
		if !strings.EqualFold(file.Content, "tunepack") {
			continue
		}
		language, err := parseCSRLanguage(file.Language.ID)
		if err != nil {
			return nil, err
		}
		plan, err := prepareCSRLanguagePlan(manifest, contents, 17, language)
		if err != nil {
			return nil, err
		}
		result = append(result, VoiceLanguage{ID: language, Name: csrLanguageName(language), Version: csrTuneVersionString(plan.TuneVersion)})
	}
	if len(result) == 0 {
		return nil, errors.New("this package does not offer the supported voice language update")
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func PrepareInteractiveLanguageInstall(path string, pid uint16, attachment string, language uint16) (*PreparedInstall, error) {
	if err := checkCSRLanguageOptIn(); err != nil {
		return nil, err
	}
	if pid != 0x2549 || attachment == "" {
		return nil, errors.New("connect the supported Evolve3 65 directly by USB")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	snapshot, err := freezeFirmwareFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = snapshot.Close() }()
	manifest, contents, err := parseGnVArchive(snapshot.path)
	if err != nil {
		return nil, err
	}
	if _, err := prepareCSRLanguagePlan(manifest, contents, 17, language); err != nil {
		return nil, err
	}
	devices, err := enumerateUSB()
	if err != nil {
		return nil, err
	}
	device, err := selectInteractiveUSBTarget(devices, pid)
	if err != nil {
		return nil, err
	}
	binding, err := interactiveBindingDigest(device, snapshot.digest)
	if err != nil {
		return nil, err
	}
	prepared := &PreparedInstall{path: path, pid: pid, binding: binding, attachment: attachment, archiveSHA: snapshot.digest, language: language}
	if err := prepared.validate(); err != nil {
		return nil, err
	}
	return prepared, nil
}

func installCSRLanguageChecked(path string, language uint16, accepted bool, validateTarget func() error, expectedDigest string, preferredPID uint16) error {
	if err := checkCSRLanguageOptIn(); err != nil {
		return err
	}
	if preferredPID != 0 && preferredPID != 0x2549 {
		return errors.New("connect the supported Evolve3 65 directly by USB")
	}
	lock, err := acquireFirmwareInstallLock()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	snapshot, err := freezeFirmwareFile(path)
	if err != nil {
		return err
	}
	defer func() { _ = snapshot.Close() }()
	if expectedDigest != "" && snapshot.digest != expectedDigest {
		return errors.New("voice language file changed after selection")
	}
	manifest, contents, err := parseGnVArchive(snapshot.path)
	if err != nil {
		return err
	}
	plan, err := prepareCSRLanguagePlan(manifest, contents, 17, language)
	if err != nil {
		return err
	}
	if validateTarget != nil {
		if err := validateTarget(); err != nil {
			return err
		}
	}
	devices, err := enumerateBoundUSB()
	if err != nil {
		return err
	}
	transfer, err := prepareFirmwareTransfer(snapshot.path, manifest)
	if err != nil {
		return err
	}
	previous, previousErr := loadFirmwareRecoveryState()
	var device USBDevice
	readOnlyRecovery := transfer.Recovery && previousErr == nil && previous.CSRLanguage != nil && previous.Phase == csrLanguageCommitting
	if readOnlyRecovery {
		device, err = selectInteractiveUSBTarget(devices, 0x2549)
	} else {
		var protocol int
		device, protocol, err = selectCSRInstallTarget(devices, 0x2549, func(device USBDevice) (int, error) { return validatedCSRProtocol(snapshot.path, device) })
		if err == nil && protocol != 17 {
			err = errors.New("voice language package needs official protocol-17 support")
		}
	}
	if err != nil {
		return fmt.Errorf("connect the Evolve3 65 directly by USB: %w", err)
	}
	phase, sourceAttachment, identity := "", "", ""
	if transfer.Recovery {
		if previousErr != nil || previous.CSRLanguage == nil || previous.Language != language || previous.CSRLanguage.TuneVersion != csrTuneVersionString(plan.TuneVersion) || previous.Protocol != 17 || previous.RuntimePID != 0x2549 {
			return errors.New("unfinished update needs its original command, language and archive")
		}
		if previous.USBPort != filepath.Base(device.SysPath) {
			return errors.New("reconnect the headset to its original USB port")
		}
		phase, sourceAttachment, identity = previous.Phase, previous.CSRLanguage.SourceAttachment, previous.TargetIdentitySHA256
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	backend := &nativeCSRLanguageBackend{device: device, wantedLanguage: language, wantedTunesVersion: csrTuneVersionString(plan.TuneVersion)}
	observed, err := backend.Open(ctx)
	if err != nil {
		return err
	}
	if observed.Identity.Version != plan.Firmware {
		_ = observed.Close()
		return errors.New("voice language package must match the installed main firmware")
	}
	if !readOnlyRecovery {
		capabilities, profileErr := firmwareModelCatalog.Lookup(ctx, 0x2549, "01-A3", observed.Identity.Version)
		err = profileErr
		if err == nil {
			property, exists := capabilities.Properties["currentVoiceLanguage"]
			allowed := false
			for _, value := range property.PossibleValues {
				allowed = allowed || value == strconv.FormatUint(uint64(language), 10)
			}
			if !exists || !capabilities.ExactFirmwareProfile || property.SpecialHandling != "OnWriteFirmwareUpdateTunesPack" || !allowed {
				err = errors.New("voice language is not supported by the exact headset model profile")
			}
		}
	}
	currentLanguage := observed.Identity.Language
	if identity == "" {
		identity = extendedRecoveryIdentity(observed.Identity)
	}
	_ = observed.Close()
	if err != nil {
		return err
	}
	confirmation := "INSTALL"
	if transfer.Recovery {
		confirmation = "RECOVER"
	}
	if !accepted {
		fmt.Fprintf(os.Stderr, "Voice prompt language: %s -> %s\nThe headset will restart. Keep USB connected.\n", csrLanguageName(currentLanguage), csrLanguageName(language))
		if phase == csrLanguageCommitting {
			fmt.Fprintln(os.Stderr, "An earlier commit is unconfirmed. This retry will only read and verify the headset.")
		}
		if !confirmFirmwareAction(os.Stdin, os.Stderr, confirmation) {
			return errors.New("voice language update cancelled")
		}
	}
	if validateTarget != nil {
		if err := validateTarget(); err != nil {
			return err
		}
	}
	commandLineRiskAccepted.Store(true)
	defer commandLineRiskAccepted.Store(false)
	if err := requireHardwareWrites(); err != nil {
		return err
	}
	lastPercent := -1
	err = runCSRLanguageUpdate(ctx, backend, plan, phase, sourceAttachment, identity, func(phase string, connection csrLanguageConnection) error {
		transfer.State.USBPort = filepath.Base(device.SysPath)
		transfer.State.RuntimePID = 0x2549
		transfer.State.Protocol = 17
		transfer.State.Language = language
		transfer.State.Phase = phase
		transfer.State.TargetIdentitySHA256 = extendedRecoveryIdentity(connection.Identity)
		transfer.State.CSRLanguage = &csrLanguageRecovery{SourceAttachment: connection.Identity.Attachment, SourceLanguage: connection.Identity.Language, SourceTunesVersion: connection.TunesVersion, TuneVersion: csrTuneVersionString(plan.TuneVersion)}
		return saveFirmwareRecoveryState(transfer.State)
	}, func(sent, total uint32) {
		percent := int(uint64(sent) * 100 / uint64(total))
		if percent != lastPercent {
			fmt.Fprintf(os.Stderr, "\rVoice pack: %3d%%", percent)
			lastPercent = percent
			if sent == total {
				fmt.Fprintln(os.Stderr)
			}
		}
	}, previous.CSRLanguage)
	if errors.Is(err, errCSRLanguageNotApplied) {
		if cleanupErr := clearFirmwareRecoveryState(); cleanupErr != nil {
			return fmt.Errorf("headset is unchanged but recovery cleanup failed: %w", cleanupErr)
		}
		return errors.New("the update did not apply; the headset is unchanged. Recovery record cleared; run the language command again to start a fresh update")
	}
	if err != nil {
		return err
	}
	if err := clearFirmwareRecoveryState(); err != nil {
		return fmt.Errorf("voice language verified but recovery cleanup failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Voice prompt language %s and pack %s read back from the same headset.\n", csrLanguageName(language), csrTuneVersionString(plan.TuneVersion))
	return nil
}
