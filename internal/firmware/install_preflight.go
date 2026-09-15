package firmware

import (
	"fmt"
)

// ValidateInstallInput performs file-only checks before the CLI interrupts a
// running service. Device matching, release authenticity and confirmation are
// still checked by the installer; this function never authorizes a write.
func ValidateInstallInput(args []string) error {
	path, _, err := parseInstallArgs(args)
	if err != nil {
		return err
	}
	snapshot, err := freezeFirmwareFile(path)
	if err != nil {
		return fmt.Errorf("firmware file: %w", err)
	}
	defer func() { _ = snapshot.Close() }()
	path = snapshot.path
	format, err := detectFormat(path)
	if err != nil {
		return err
	}
	if format == FormatCSRDFU2 {
		_, err := loadJabraDFUImage(path)
		return err
	}
	if format != FormatGnVArchive {
		return fmt.Errorf("unsupported firmware file format: %s", format.String())
	}
	manifest, err := parseFirmwareManifest(path)
	if err != nil {
		return err
	}
	if isUSBDFUManifest(manifest) {
		_, err := loadJabraDFUImage(path)
		return err
	}
	if isSitelManifest(manifest) {
		_, _, err := loadSitelImages(path)
		return err
	}
	if isExtendedCSRManifest(manifest) {
		return validateExtendedCSRArchive(path)
	}
	return validateNativeCSRArchive(path)
}
