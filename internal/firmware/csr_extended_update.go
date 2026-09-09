package firmware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The extended driver uses 52 data bytes even on a 64-byte report. HID report
// capacity is not a substitute for the update protocol's block size.
const extendedCSRChunkBytes = 52

type csrExtendedImage struct {
	Partition byte
	Data      []byte
}

type csrExtendedInstallStage struct {
	Kind       string
	Images     []csrExtendedImage
	ConfigExit bool
}

type csrExtendedPlan struct {
	Protocol int
	Version  [3]byte
	Language uint16
	Preload  uint16
	Stages   []csrExtendedInstallStage
}

// Select the requested language exactly. Updating must not silently change a
// headset's language or choose the first of several ambiguous partition images.
func prepareExtendedCSRPlan(manifest *BuildVector, contents map[string][]byte, protocol int, language uint16, firmwareChanged, languageChanged bool) (csrExtendedPlan, error) {
	if manifest == nil || (protocol != 16 && protocol != 17) || language == 0 || manifest.MaxPreloadCount < 1 || manifest.MaxPreloadCount > 10 {
		return csrExtendedPlan{}, errors.New("incomplete extended CSR update metadata")
	}
	version, err := parseVersionTriplet(manifest.Version)
	if err != nil {
		return csrExtendedPlan{}, err
	}
	byPartition := map[int][]GnVFile{}
	for _, file := range manifest.Files {
		if file.Partition < 0 || file.Partition > 255 || file.SitelHidTargetID != "" || (file.Target != "headset" && file.Target != "dongle") {
			return csrExtendedPlan{}, errors.New("invalid extended CSR image target")
		}
		byPartition[file.Partition] = append(byPartition[file.Partition], file)
	}
	var selected []GnVFile
	for partition, candidates := range byPartition {
		var matches []GnVFile
		for _, file := range candidates {
			if file.Language.ID == "" || strings.EqualFold(file.Language.ID, fmt.Sprintf("0x%04x", language)) {
				matches = append(matches, file)
			}
		}
		if len(matches) != 1 {
			return csrExtendedPlan{}, fmt.Errorf("partition %d needs exactly one image for language 0x%04x", partition, language)
		}
		selected = append(selected, matches[0])
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].UpdateOrder != selected[j].UpdateOrder {
			return selected[i].UpdateOrder < selected[j].UpdateOrder
		}
		return selected[i].Partition < selected[j].Partition
	})
	planned, err := planCSRStages(protocol, selected, firmwareChanged, languageChanged)
	if err != nil {
		return csrExtendedPlan{}, err
	}
	result := csrExtendedPlan{Protocol: protocol, Version: version, Language: language, Preload: uint16(manifest.MaxPreloadCount)}
	var total int64
	for _, stage := range planned {
		output := csrExtendedInstallStage{Kind: stage.Kind, ConfigExit: stage.RequiresConfigExit}
		for _, file := range stage.Files {
			data := contents[file.Name]
			total += int64(len(data))
			if len(data) == 0 || total > MaxExpandedArchiveSize || file.Version != manifest.Version {
				return csrExtendedPlan{}, fmt.Errorf("missing, oversized or mismatched image %q", file.Name)
			}
			if file.CRC != "" {
				crc, err := parseHexCRC(file.CRC)
				if err != nil || csrImageCRC(data) != crc {
					return csrExtendedPlan{}, errors.New("extended CSR image CRC mismatch")
				}
			}
			output.Images = append(output.Images, csrExtendedImage{Partition: byte(file.Partition), Data: append([]byte(nil), data...)})
		}
		result.Stages = append(result.Stages, output)
	}
	return result, nil
}

type csrExtendedIdentity struct {
	PID                      uint16
	Port, Attachment, Serial string
	Variant, Version         string
	Language                 uint16
	FirmwareProtocols        []byte
}

func extendedRecoveryIdentity(identity csrExtendedIdentity) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%04x\n%s\n%s\n%s", identity.PID, identity.Port, identity.Serial, identity.Variant))))
}

type csrExtendedConnection struct {
	IO         csrStageIO
	Identity   csrExtendedIdentity
	Address    byte
	ReportSize int
	Close      func() error
}

// Open/Reconnect must return fresh read-only IDENT and language responses from
// the bound endpoint. They may not copy the expected values into the result.
type csrExtendedBackend interface {
	Open(context.Context) (csrExtendedConnection, error)
	Reconnect(context.Context, csrExtendedIdentity) (csrExtendedConnection, error)
}

func validateExtendedConnection(connection csrExtendedConnection) error {
	id := connection.Identity
	if connection.IO == nil || connection.Close == nil || (connection.Address != 1 && connection.Address != 8) || (connection.ReportSize != 63 && connection.ReportSize != 64) || id.PID == 0 || id.Port == "" || id.Attachment == "" || id.Serial == "" || id.Variant == "" || id.Language == 0 || len(id.FirmwareProtocols) == 0 {
		return errors.New("extended CSR connection identity is incomplete")
	}
	_, err := parseVersionTriplet(id.Version)
	return err
}

// A complete install sequence over an identity-bound backend. The caller must
// first authenticate the sealed archive against this exact PID and protocol,
// obtain user confirmation and hold the device lease. checkpoint must durably
// record the archive/stage before the first destructive command of each stage.
// A checkpoint is retained on every error; only the caller clears it on success.
func runExtendedCSRUpdate(ctx context.Context, backend csrExtendedBackend, plan csrExtendedPlan, checkpoint func(int) error, progress func(int, uint32, uint32)) error {
	if backend == nil || checkpoint == nil || (plan.Protocol != 16 && plan.Protocol != 17) || len(plan.Stages) == 0 || plan.Language == 0 || plan.Preload == 0 || plan.Preload > 10 {
		return errors.New("incomplete extended CSR install plan")
	}
	// Check every image before opening a device or recording recovery state.
	var total int64
	for _, stage := range plan.Stages {
		if len(stage.Images) == 0 || (stage.Kind != "language" && stage.Kind != "firmware" && stage.Kind != "combined") || (plan.Protocol == 17 && !stage.ConfigExit) {
			return errors.New("invalid extended CSR stage")
		}
		for _, image := range stage.Images {
			total += int64(len(image.Data))
			if total > MaxExpandedArchiveSize {
				return errors.New("extended CSR image exceeds limit")
			}
			if _, err := otaChunkCountForPayload(len(image.Data), extendedCSRChunkBytes, true); err != nil {
				return err
			}
		}
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
	if err := validateExtendedConnection(connection); err != nil {
		return err
	}
	original := connection.Identity
	for index, stage := range plan.Stages {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !bytes.Contains(connection.Identity.FirmwareProtocols, []byte{byte(plan.Protocol)}) {
			return errors.New("device-reported firmware protocol does not match the selected release")
		}
		if err := checkpoint(index); err != nil {
			return err
		}
		session := &csrStageTransfer{io: connection.IO}
		for _, image := range stage.Images {
			transfer := csrExtendedStage{Partition: image.Partition, Image: image.Data, Version: plan.Version, Address: connection.Address, ReportSize: connection.ReportSize, ChunkBytes: extendedCSRChunkBytes, Preload: plan.Preload, Timeout: 30 * time.Second}
			if err := session.transfer(ctx, transfer, func(sent, total uint32) {
				if progress != nil {
					progress(index, sent, total)
				}
			}); err != nil {
				return fmt.Errorf("stage %d transfer: %w", index+1, err)
			}
		}
		session.events = nil
		session.sent, session.total = 0, 1 // no image events may complete a commit
		if err := session.command(ctx, OtaOpDfuFromSquif, nil); err != nil {
			return fmt.Errorf("stage %d commit: %w", index+1, err)
		}
		if stage.ConfigExit {
			permission, err := session.query(ctx, 0x0d, 0x11)
			if err != nil {
				return fmt.Errorf("restart permission: %w", err)
			}
			if len(permission) != 1 || permission[0] != 1 {
				return errors.New("device did not grant configuration-mode exit")
			}
			if err := session.commandClass(ctx, 0x0d, 0x12, nil); err != nil {
				return fmt.Errorf("configuration-mode exit: %w", err)
			}
		}
		before := connection.Identity
		if err := connection.Close(); err != nil {
			connection.Close = nil
			return err
		}
		connection.Close = nil
		reconnect, cancel := context.WithTimeout(ctx, 2*time.Minute)
		connection, err = backend.Reconnect(reconnect, before)
		cancel()
		if err != nil {
			return fmt.Errorf("stage %d reconnect: %w", index+1, err)
		}
		if err := validateExtendedConnection(connection); err != nil {
			return err
		}
		after := connection.Identity
		if after.Attachment == before.Attachment || after.Port != original.Port || after.PID != original.PID || after.Serial != original.Serial || after.Variant != original.Variant {
			return errors.New("updated device did not return with the selected identity")
		}
		wanted := fmt.Sprintf("%d.%d.%d", plan.Version[0], plan.Version[1], plan.Version[2])
		if stage.Kind == "language" {
			wanted = before.Version
		}
		if after.Version != wanted || after.Language != plan.Language {
			return fmt.Errorf("stage %d readback does not match expected firmware %s and language 0x%04x", index+1, wanted, plan.Language)
		}
	}
	return nil
}
