package firmware

import (
	"context"
	"errors"
	"fmt"

	"github.com/Watchdog0x/jabridge/internal/modelcatalog"
)

func nativeCSRReleaseMatches(checksum string, pid uint16, evidence *modelcatalog.ReleaseEvidence) bool {
	return firmwareReleaseMatchesDevice(checksum, pid, evidence) && !evidence.HasUnspecifiedFirmwareProtocol &&
		len(evidence.FirmwareProtocols) == 1 && evidence.FirmwareProtocols[0] == 7
}

// A matching archive suffix never authorizes borrowing another protocol's
// commands. Selection requires current, unambiguous official release evidence.
func validatedCSRProtocol(path string, device USBDevice) (int, error) {
	manifest, err := parseFirmwareManifest(path)
	if err != nil {
		return 0, err
	}
	pids, err := parseTargetPIDs(manifest.TargetUSBPIDs)
	if err != nil || device.VendorID != JabraVendorID || device.ViaDongle {
		return 0, errors.New("CSR archive does not match the selected direct USB device")
	}
	checksum, err := firmwareFileMD5(path)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), MetadataTimeout)
	defer cancel()
	evidence, err := firmwareModelCatalog.FirmwareRelease(ctx, device.ProductID, manifest.Version)
	if err != nil {
		return 0, err
	}
	if evidence == nil {
		return 0, errors.New("official firmware release evidence missing")
	}
	// Official archives can name a canonical PID while the connected UC/MS
	// variant has a sibling PID. Require the exact same published release to
	// explicitly cover both the connected device and a manifest target.
	manifestMatch := false
	for _, pid := range pids {
		manifestMatch = manifestMatch || containsPID(evidence.CompatiblePIDs, pid)
	}
	if !manifestMatch {
		return 0, errors.New("official release does not cover the archive target")
	}
	return matchedCSRProtocol(checksum, device.ProductID, evidence)
}

func selectCSRInstallTarget(devices []USBDevice, preferredPID uint16, validate func(USBDevice) (int, error)) (USBDevice, int, error) {
	if validate == nil {
		return USBDevice{}, 0, errors.New("firmware release validator missing")
	}
	var selected USBDevice
	protocol, count := 0, 0
	var failures []error
	for _, device := range devices {
		if device.VendorID != JabraVendorID || device.ViaDongle || preferredPID != 0 && device.ProductID != preferredPID {
			continue
		}
		candidate, err := validate(device)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		selected, protocol = device, candidate
		count++
	}
	if count != 1 {
		message := fmt.Sprintf("need exactly one device matching this official firmware release; found %d", count)
		if len(failures) > 0 {
			return USBDevice{}, 0, fmt.Errorf("%s: %w", message, errors.Join(failures...))
		}
		return USBDevice{}, 0, errors.New(message)
	}
	return selected, protocol, nil
}

func matchedCSRProtocol(checksum string, pid uint16, evidence *modelcatalog.ReleaseEvidence) (int, error) {
	if !firmwareReleaseMatchesDevice(checksum, pid, evidence) || evidence.HasUnspecifiedFirmwareProtocol || len(evidence.FirmwareProtocols) != 1 {
		return 0, errors.New("firmware needs one exact matching official update protocol and checksum")
	}
	protocol := evidence.FirmwareProtocols[0]
	if protocol != 7 && protocol != 16 && protocol != 17 {
		return 0, errors.New("this firmware uses a different update protocol")
	}
	return protocol, nil
}
