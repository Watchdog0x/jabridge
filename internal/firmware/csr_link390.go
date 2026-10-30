package firmware

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const link390FirmwareEnv = "JABRIDGE_ENABLE_LINK390_CSR7_FIRMWARE"
const link390FirmwareAck = "I_ACCEPT_UNTESTED_FIRMWARE"

// This process-start opt-in is separate from settings and broad device writes.
var link390FirmwareEnabled = os.Getenv(link390FirmwareEnv) == link390FirmwareAck

const csr7Transferring = "csr7-transferring"
const csr7Committing = "csr7-committing"

type link390CSR7Image struct {
	Data              []byte
	Version           [3]byte
	Language, Preload uint16
}

func isLink390PID(pid uint16) bool {
	switch pid {
	case 0x2e50, 0x2e51, 0x2e56, 0x2e57:
		return true
	}
	return false
}

func isLink390BinaryManifest(manifest *BuildVector) bool {
	if manifest == nil || !strings.EqualFold(strings.TrimSpace(manifest.ProductName), "Jabra Link 390") || len(manifest.Files) != 1 {
		return false
	}
	return strings.EqualFold(filepath.Ext(manifest.Files[0].Name), ".bin")
}

func loadLink390CSR7Archive(path string) (link390CSR7Image, error) {
	manifest, contents, err := parseGnVArchive(path)
	if err != nil {
		return link390CSR7Image{}, err
	}
	if !isLink390BinaryManifest(manifest) {
		return link390CSR7Image{}, errors.New("not a supported Link 390 monolithic package")
	}
	pids, err := parseTargetPIDs(manifest.TargetUSBPIDs)
	if err != nil || len(pids) == 0 {
		return link390CSR7Image{}, errors.New("the Link 390 firmware target is missing")
	}
	for _, pid := range pids {
		if !isLink390PID(pid) {
			return link390CSR7Image{}, errors.New("the Link 390 package names an unrelated USB target")
		}
	}
	file := manifest.Files[0]
	if filepath.Base(file.Name) != file.Name || file.Partition != 0 || file.SitelHidTargetID != "" || file.Version != manifest.Version || file.Content != "firmware" || file.Target != "headset" || manifest.MaxPreloadCount < 1 || manifest.MaxPreloadCount > 10 {
		return link390CSR7Image{}, errors.New("unsupported Link 390 monolithic image metadata")
	}
	updater := strings.Split(manifest.MinFirmwareUpdaterAppVersion, ".")
	if len(updater) != 4 {
		return link390CSR7Image{}, errors.New("invalid minimum firmware updater version")
	}
	for index, part := range updater {
		value, err := strconv.ParseUint(part, 10, 16)
		if err != nil || index == 0 && value < 5 {
			return link390CSR7Image{}, errors.New("unsupported minimum firmware updater version")
		}
	}
	language, err := strconv.ParseUint(file.Language.ID, 0, 16)
	if err != nil || language != 0x0409 {
		return link390CSR7Image{}, errors.New("this candidate requires one unambiguous English Link 390 image")
	}
	version, err := parseVersionTriplet(manifest.Version)
	if err != nil {
		return link390CSR7Image{}, err
	}
	data := contents[file.Name]
	if _, err := otaChunkCountForPayload(len(data), 52, false); err != nil {
		return link390CSR7Image{}, err
	}
	if file.CRC != "" {
		want, err := parseHexCRC(file.CRC)
		if err != nil || csrImageCRC(data) != want {
			return link390CSR7Image{}, errors.New("the Link 390 image CRC mismatch")
		}
	}
	return link390CSR7Image{Data: data, Version: version, Language: uint16(language), Preload: uint16(manifest.MaxPreloadCount)}, nil
}

func validateCSRInstallArchive(path string, protocol int) error {
	if protocol == 7 {
		manifest, err := parseFirmwareManifest(path)
		if err != nil {
			return err
		}
		if isLink390BinaryManifest(manifest) {
			_, err := loadLink390CSR7Archive(path)
			return err
		}
		return validateNativeCSRArchive(path)
	}
	return validateExtendedCSRArchive(path)
}

func validateCSR7Connection(connection csrExtendedConnection) error {
	id := connection.Identity
	if connection.IO == nil || connection.Close == nil || connection.Address != 1 || (connection.ReportSize != 63 && connection.ReportSize != 64) || !isLink390PID(id.PID) || id.Port == "" || id.Attachment == "" || id.Serial == "" || id.Variant == "" || !containsProtocol(id.FirmwareProtocols, 7) {
		return errors.New("the Link 390 update endpoint is not fully identified as protocol 7")
	}
	_, err := parseVersionTriplet(id.Version)
	return err
}

func containsProtocol(protocols []byte, want byte) bool {
	for _, p := range protocols {
		if p == want {
			return true
		}
	}
	return false
}

// Commit-started recovery only observes runtime metadata. It never erases,
// replays commit or sends configuration-end to force a restart.
func runLink390CSR7Update(ctx context.Context, backend csrExtendedBackend, image link390CSR7Image, phase, expectedIdentity string, checkpoint func(csrExtendedIdentity, string) error, progress func(uint32, uint32)) error {
	if backend == nil || checkpoint == nil || len(image.Data) == 0 || image.Language != 0x409 || image.Preload < 1 || image.Preload > 10 || phase != "" && phase != csr7Transferring && phase != csr7Committing {
		return errors.New("incomplete Link 390 firmware plan")
	}
	if _, err := otaChunkCountForPayload(len(image.Data), 52, false); err != nil {
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
	if err := validateCSR7Connection(connection); err != nil {
		return err
	}
	original := connection.Identity
	if expectedIdentity != "" && expectedIdentity != extendedRecoveryIdentity(original) {
		return errors.New("firmware recovery belongs to another USB device")
	}
	wanted := fmt.Sprintf("%d.%d.%d", image.Version[0], image.Version[1], image.Version[2])
	if original.Language != 0 && original.Language != image.Language {
		return errors.New("the Link 390 image does not match the device language")
	}
	if original.Version == wanted {
		return nil
	}
	if phase != csr7Committing {
		if err := checkpoint(original, csr7Transferring); err != nil {
			return err
		}
		session := &csrStageTransfer{io: connection.IO}
		stage := csrExtendedStage{Partition: 0, Image: image.Data, Version: image.Version, Address: 1, ReportSize: connection.ReportSize, ChunkBytes: 52, Preload: image.Preload, Timeout: 30 * time.Second, Protocol7: true, Language: image.Language}
		if err := session.transfer(ctx, stage, progress); err != nil {
			return fmt.Errorf("the Link 390 transfer stopped; recovery record kept: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := checkpoint(original, csr7Committing); err != nil {
			return err
		}
		session.events = nil
		session.sent, session.total = 0, 1
		// A failed/lost ACK is uncertain. Confirmation below is read-only.
		_ = session.command(ctx, OtaOpDfuFromSquif, nil)
	}
	if err := connection.Close(); err != nil {
		connection.Close = nil
		return err
	}
	connection.Close = nil
	// This is Jabridge's verification window, not a vendor settling delay.
	verify, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	connection, err = backend.Reconnect(verify, original)
	if err != nil {
		return fmt.Errorf("firmware commit is unconfirmed; no commands were repeated; reconnect USB and run the same install to check again: %w", err)
	}
	if err := verify.Err(); err != nil {
		return fmt.Errorf("firmware commit could not be confirmed before the deadline; recovery record kept: %w", err)
	}
	if err := validateCSR7Connection(connection); err != nil {
		return err
	}
	after := connection.Identity
	if after.PID != original.PID || after.Port != original.Port || after.Serial != original.Serial || after.Variant != original.Variant || after.Version != wanted {
		return errors.New("firmware commit is unconfirmed: runtime identity or firmware version does not match; recovery record kept")
	}
	if after.Language != 0 && after.Language != image.Language {
		return errors.New("firmware language readback does not match; recovery record kept")
	}
	return nil
}

func validLink390CSR7Recovery(state firmwareRecoveryState) error {
	if !isLink390PID(state.RuntimePID) || state.USBPort == "" || state.Language != 0x409 || (state.Phase != csr7Transferring && state.Phase != csr7Committing) {
		return errors.New("invalid Link 390 firmware recovery state")
	}
	digest, err := hex.DecodeString(state.TargetIdentitySHA256)
	if err != nil || len(digest) != 32 {
		return errors.New("invalid Link 390 recovery identity")
	}
	return nil
}
