package firmware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// File-shape routing is used for preflight only, never to authorize a protocol.
func isExtendedCSRManifest(manifest *BuildVector) bool {
	if manifest == nil || len(manifest.Files) == 0 || manifest.MaxPreloadCount == 0 {
		return false
	}
	hasFooter := false
	hasBinary := false
	for _, file := range manifest.Files {
		if file.SitelHidTargetID != "" {
			return false
		}
		if strings.EqualFold(filepath.Ext(file.Name), ".bin") {
			hasBinary = true
		}
		hasFooter = hasFooter || file.Partition == 254
	}
	return hasBinary || !hasFooter
}

func validateExtendedCSRArchive(path string) error {
	manifest, contents, err := parseGnVArchive(path)
	if err != nil {
		return err
	}
	if _, err := parseTargetPIDs(manifest.TargetUSBPIDs); err != nil {
		return err
	}
	if manifest.MaxPreloadCount < 1 || manifest.MaxPreloadCount > 10 || len(manifest.Files) == 0 {
		return errors.New("invalid extended CSR manifest")
	}
	if _, err := parseVersionTriplet(manifest.Version); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, file := range manifest.Files {
		ext := strings.ToLower(filepath.Ext(file.Name))
		if (ext != ".bin" && ext != ".gnv") || filepath.Base(file.Name) != file.Name || seen[file.Name] || file.SitelHidTargetID != "" || file.Partition < 0 || file.Partition > 255 || file.Version != manifest.Version || (file.Target != "headset" && file.Target != "dongle") {
			return errors.New("invalid extended CSR image metadata")
		}
		seen[file.Name] = true
		if file.Language.ID != "" {
			value, err := strconv.ParseUint(file.Language.ID, 0, 16)
			if err != nil || value == 0 {
				return errors.New("invalid firmware language ID")
			}
		}
		data := contents[file.Name]
		if _, err := otaChunkCountForPayload(len(data), extendedCSRChunkBytes, true); err != nil {
			return err
		}
		if file.CRC != "" {
			crc, err := parseHexCRC(file.CRC)
			if err != nil || csrImageCRC(data) != crc {
				return errors.New("extended CSR image CRC mismatch")
			}
		}
	}
	return nil
}

type preparedExtendedBackend struct {
	initial *csrExtendedConnection
	native  *nativeExtendedCSRBackend
}

func (b *preparedExtendedBackend) Open(ctx context.Context) (csrExtendedConnection, error) {
	if err := ctx.Err(); err != nil {
		return csrExtendedConnection{}, err
	}
	if b.initial == nil {
		return csrExtendedConnection{}, errors.New("extended CSR initial connection already consumed")
	}
	value := *b.initial
	b.initial = nil
	return value, nil
}
func (b *preparedExtendedBackend) Reconnect(ctx context.Context, before csrExtendedIdentity) (csrExtendedConnection, error) {
	return b.native.Reconnect(ctx, before)
}

func flashBoundExtendedCSR(snapshot *firmwareSnapshot, device USBDevice, protocol int, begin func(csrExtendedIdentity) error) error {
	if err := requireHardwareWrites(); err != nil {
		return err
	}
	if protocol != 16 && protocol != 17 {
		return errors.New("not an extended CSR protocol")
	}
	if err := validateExtendedCSRArchive(snapshot.path); err != nil {
		return err
	}
	manifest, contents, err := parseGnVArchive(snapshot.path)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	native := &nativeExtendedCSRBackend{device: device}
	connection, err := native.Open(ctx)
	if err != nil {
		return err
	}
	backend := &preparedExtendedBackend{initial: &connection, native: native}
	defer func() {
		if backend.initial != nil {
			_ = backend.initial.Close()
		}
	}()
	plan, err := prepareExtendedCSRPlan(manifest, contents, protocol, connection.Identity.Language, connection.Identity.Version != manifest.Version, false)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Native firmware protocol %d; keeping language 0x%04x. Keep USB connected.\n", protocol, plan.Language)
	lastPercent := -1
	err = runExtendedCSRUpdate(ctx, backend, plan, func(stage int) error {
		lastPercent = -1
		fmt.Fprintf(os.Stderr, "Firmware stage %d of %d: %s\n", stage+1, len(plan.Stages), plan.Stages[stage].Kind)
		return begin(connection.Identity)
	}, func(stage int, sent, total uint32) {
		percent := int(uint64(sent) * 100 / uint64(total))
		if percent != lastPercent {
			fmt.Fprintf(os.Stderr, "\rStage %d: %3d%%", stage+1, percent)
			lastPercent = percent
			if sent == total {
				fmt.Fprintln(os.Stderr)
			}
		}
	})
	if err == nil {
		fmt.Fprintf(os.Stderr, "Firmware %s installed and read back from the same device.\n", manifest.Version)
	}
	return err
}
