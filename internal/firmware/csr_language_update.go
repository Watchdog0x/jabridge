package firmware

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const (
	csrLanguageTransferring = "csr17-language-transferring"
	csrLanguageCommitting   = "csr17-language-committing"
)

type csrLanguageConnection struct {
	csrExtendedConnection
	TunesVersion string
	Battery      func(context.Context) (int, bool, error)
}

type csrLanguageBackend interface {
	Open(context.Context) (csrLanguageConnection, error)
	Reconnect(context.Context, csrExtendedIdentity) (csrLanguageConnection, error)
}

type csrLanguageRecovery struct {
	SourceAttachment   string `json:"sourceAttachment"`
	SourceLanguage     uint16 `json:"sourceLanguage"`
	SourceTunesVersion string `json:"sourceTunesVersion"`
	TuneVersion        string `json:"tuneVersion"`
}

var errCSRLanguageNotApplied = errors.New("the update did not apply; the headset is unchanged")

func validCSRLanguageRecovery(state firmwareRecoveryState) error {
	info := state.CSRLanguage
	if info == nil || state.Protocol != 17 || state.RuntimePID != 0x2549 || state.Language == 0 || state.USBPort == "" || (state.Phase != csrLanguageTransferring && state.Phase != csrLanguageCommitting) {
		return errors.New("voice language recovery record is incomplete")
	}
	if info.SourceAttachment == "" || len(info.SourceAttachment) > 256 || info.SourceLanguage == 0 {
		return errors.New("voice language source identity is incomplete")
	}
	identity, err := hex.DecodeString(state.TargetIdentitySHA256)
	if err != nil || len(identity) != 32 {
		return errors.New("voice language recovery identity is invalid")
	}
	if _, err := parseVersionTriplet(info.TuneVersion); err != nil {
		return errors.New("voice language target pack version is invalid")
	}
	if _, err := parseVersionTriplet(info.SourceTunesVersion); err != nil {
		return errors.New("voice language source pack version is invalid")
	}
	targets, err := parseTargetPIDs(state.TargetUSBPIDs)
	if err != nil || len(targets) != 1 || targets[0] != 0x2549 {
		return errors.New("voice language recovery archive targets another device")
	}
	return nil
}

func csrTuneVersionString(version [3]byte) string {
	return fmt.Sprintf("%d.%d.%d", version[0], version[1], version[2])
}

func validateCSRLanguageConnection(connection csrLanguageConnection) error {
	if err := validateExtendedConnection(connection.csrExtendedConnection); err != nil {
		return err
	}
	id := connection.Identity
	if id.PID != 0x2549 || id.Variant != "01a3" || !bytes.Contains(id.FirmwareProtocols, []byte{17}) {
		return errors.New("voice language update requires the supported Evolve3 65 USB endpoint and protocol")
	}
	if _, err := parseVersionTriplet(connection.TunesVersion); err != nil {
		return errors.New("headset voice pack version could not be read")
	}
	return nil
}

func csrLanguageInstalled(connection csrLanguageConnection, plan csrLanguagePlan) bool {
	return connection.Identity.Version == plan.Firmware && connection.Identity.Language == plan.Language && connection.TunesVersion == csrTuneVersionString(plan.TuneVersion)
}

// Committing recovery is strictly read-only, including after a process dies
// before the commit acknowledgement. The caller saves every checkpoint durably
// and retains its archive/identity record on every error.
func runCSRLanguageUpdate(ctx context.Context, backend csrLanguageBackend, plan csrLanguagePlan, phase string, originalAttachment string, expectedIdentity string, checkpoint func(string, csrLanguageConnection) error, progress func(uint32, uint32), recovery ...*csrLanguageRecovery) error {
	if backend == nil || checkpoint == nil || len(plan.Image) == 0 || plan.Language == 0 || plan.Preload < 1 || plan.Preload > 10 || (phase != "" && phase != csrLanguageTransferring && phase != csrLanguageCommitting) {
		return errors.New("incomplete voice language update plan")
	}
	if phase != "" && (expectedIdentity == "" || originalAttachment == "") {
		return errors.New("voice language recovery identity is incomplete")
	}
	if _, err := parseVersionTriplet(plan.Firmware); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	connection, err := backend.Open(ctx)
	if err != nil {
		if connection.Close != nil {
			_ = connection.Close()
		}
		return err
	}
	defer func() {
		if connection.Close != nil {
			_ = connection.Close()
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateCSRLanguageConnection(connection); err != nil {
		return err
	}
	before := connection.Identity
	if before.Version != plan.Firmware {
		return errors.New("install the matching headset firmware before changing its voice language")
	}
	if expectedIdentity != "" && extendedRecoveryIdentity(before) != expectedIdentity {
		return errors.New("unfinished voice language update belongs to another headset")
	}
	if phase == csrLanguageCommitting {
		if originalAttachment == "" || before.Attachment == originalAttachment {
			return errors.New("voice language commit is unconfirmed; reconnect USB and run the same language update to check again; no update was repeated")
		}
		if csrLanguageInstalled(connection, plan) {
			return nil
		}
		if len(recovery) == 1 && recovery[0] != nil && recovery[0].SourceLanguage == before.Language && recovery[0].SourceTunesVersion != "" && recovery[0].SourceTunesVersion == connection.TunesVersion {
			return errCSRLanguageNotApplied
		}
		return errors.New("voice language state matches neither the source nor target; recovery remains read-only")
	}
	if phase == "" && csrLanguageInstalled(connection, plan) {
		return nil
	}
	if connection.Battery == nil {
		return errors.New("headset battery verification is unavailable")
	}
	level, charging, err := connection.Battery(ctx)
	if err != nil {
		return fmt.Errorf("headset battery check: %w", err)
	}
	if level < 0 || level > 100 {
		return errors.New("headset returned an invalid battery level")
	}
	// Vendor USB precondition: a charging headset must have at least 10%.
	// The separate wireless updater's 30% policy is not used for USB.
	if charging && level < 10 {
		return errors.New("charge the headset to at least 10% before changing its voice language")
	}
	if err := checkpoint(csrLanguageTransferring, connection); err != nil {
		return err
	}
	session := &csrStageTransfer{io: connection.IO}
	stage := csrExtendedStage{Partition: plan.Partition, Image: plan.Image, Version: plan.TuneVersion, Address: connection.Address, ReportSize: connection.ReportSize, ChunkBytes: extendedCSRChunkBytes, Preload: plan.Preload, Timeout: 30 * time.Second, SplitLanguage: true, Language: plan.Language}
	if err := session.transfer(ctx, stage, progress); err != nil {
		return fmt.Errorf("voice pack transfer: %w", err)
	}
	if err := checkpoint(csrLanguageCommitting, connection); err != nil {
		return err
	}
	session.events = nil
	session.sent, session.total = 0, 1
	if err := session.command(ctx, OtaOpDfuFromSquif, nil); err != nil {
		return fmt.Errorf("voice language commit is unconfirmed; no command was repeated: %w", err)
	}
	// Once commit is acknowledged, finish the bounded restart sequence even
	// when the user cancels. Reconnect remains cancellable and read-only.
	restart, finishRestart := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer finishRestart()
	serial, err := session.query(restart, 2, 1)
	if err != nil {
		return fmt.Errorf("verify headset before restart: %w", err)
	}
	serialText, err := decodeExtendedIdentityString(serial)
	if err != nil || serialText != before.Serial {
		return errors.New("headset identity changed before restart; recovery record kept")
	}
	permission, err := session.query(restart, 0x0d, 0x11)
	if err != nil || len(permission) != 1 || permission[0] != 1 {
		return fmt.Errorf("headset did not grant restart permission: %w", errors.Join(errors.New("restart unconfirmed"), err))
	}
	if err := session.commandClass(restart, 0x0d, 0x12, nil); err != nil {
		return fmt.Errorf("headset restart is unconfirmed: %w", err)
	}
	if err := connection.Close(); err != nil {
		connection.Close = nil
		return err
	}
	connection.Close = nil
	verify, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	connection, err = backend.Reconnect(verify, before)
	if err != nil {
		return fmt.Errorf("voice language update is unconfirmed; recovery record kept: %w", err)
	}
	if err := verify.Err(); err != nil {
		return err
	}
	if err := validateCSRLanguageConnection(connection); err != nil {
		return err
	}
	after := connection.Identity
	if after.Attachment == before.Attachment || after.Port != before.Port || after.PID != before.PID || after.Serial != before.Serial || after.Variant != before.Variant {
		return errors.New("headset identity or USB generation changed unexpectedly; recovery record kept")
	}
	if !csrLanguageInstalled(connection, plan) {
		return errors.New("headset voice language or pack version did not match; recovery record kept")
	}
	return nil
}
