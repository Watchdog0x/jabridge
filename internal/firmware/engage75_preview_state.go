package firmware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// This is deliberately separate from the ordinary DECT identity scheme. Only
// fields actually measured in Main are selected. Unsupported identities stay
// explicitly unavailable; a boot image ID is never a runtime product ID.
const engage75SelectionV2 = "base-ident-usb/head-serial-variant/display-variant/resources-v2"

type engage75SelectedIdentity struct {
	Scheme           string `json:"scheme"`
	RuntimePID       uint16 `json:"runtimePid"`
	BootPID          uint16 `json:"bootPid"`
	USBPort          string `json:"usbPort"`
	USBSerialSHA256  string `json:"usbSerialSha256"`
	HeadSerialSHA256 string `json:"headSerialSha256"`
	BaseVariant      string `json:"baseVariant"`
	HeadVariant      string `json:"headVariant"`
	DisplayVariant   string `json:"displayVariant"`
	// Class2 IDENT is unavailable on these components in the original runtime.
	// This fixed provenance cannot be weakened with a caller-selected mask.
	Unavailable      string           `json:"unavailable"`
	HeadRuntimeInfo  sitelDeviceInfo  `json:"headRuntimeInfo"`
	OriginalVersions engage75Versions `json:"originalVersions"`
	LanguageRegion   byte             `json:"languageRegion"`
	HeadRegion       byte             `json:"headRegion"`
	BaseRegion       byte             `json:"baseRegion"`
}

type engage75Versions struct {
	Base, Head, Display, Radio, Graphics, Language, HeadTunes, BaseTunes string
}

const engage75Unavailable = "class2:10/11,10/13,3/11,3/01,3/13;runtime-class15:3/00=NAK"

type engage75MeasuredImage struct {
	Address      byte   `json:"address"`
	ImageID      uint32 `json:"imageId"`
	ReportedMode byte   `json:"reportedMode"`
}

// Unlike a pending journal, this schema can be created only after the measured
// boot image sweep. The loader still cannot authorize a new process to flash:
// live dock continuity cannot be recovered from a stored serial digest alone.
type engage75RecoveryV2 struct {
	Version          int                      `json:"version"`
	Selected         engage75SelectedIdentity `json:"selected"`
	Images           []engage75MeasuredImage  `json:"images"`
	RadioRevision    uint16                   `json:"radioRevision,omitempty"`
	SettingsVerified bool                     `json:"settingsVerified"`
}

type engage75Pending struct {
	Version          int                      `json:"version"`
	Purpose          string                   `json:"purpose"`
	ArchiveSHA256    string                   `json:"archiveSha256"`
	Phase            string                   `json:"phase"`
	Selected         engage75SelectedIdentity `json:"selected"`
	Entry            string                   `json:"entry"`
	HeadReturn       string                   `json:"headReturn"`
	HeadMainVerified bool                     `json:"headMainVerified"`
	BaseReturn       string                   `json:"baseReturn"`
	Unresolved       bool                     `json:"unresolved"`
}

func engage75Digest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func validEngage75Digest(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == sha256.Size && value == strings.ToLower(value)
}

func (s engage75SelectedIdentity) validate() error {
	if s.Scheme != engage75SelectionV2 || s.RuntimePID != 0x1113 || s.BootPID != 0x1111 || s.USBPort == "" || s.USBPort == "." || s.USBPort == ".." || filepath.Base(s.USBPort) != s.USBPort ||
		!validEngage75Digest(s.USBSerialSHA256) || !validEngage75Digest(s.HeadSerialSHA256) || s.BaseVariant != "001b" || s.HeadVariant != "0148" || s.DisplayVariant != "0304" ||
		s.HeadRuntimeInfo.ID != 0x0b0e1116 || s.HeadRuntimeInfo.Mode != 1 || s.HeadRuntimeInfo.SectorSize != 65536 || s.HeadRuntimeInfo.WriteSize != 1001 || s.Unavailable != engage75Unavailable || s.LanguageRegion != 7 || s.HeadRegion != 1 || s.BaseRegion != 1 {
		return errors.New("invalid Engage 75 selected-field identity scheme")
	}
	v := s.OriginalVersions
	if v.Base != "4.4.1" || v.Head != "4.4.1" || v.Display != "4.4.1" || v.Radio != "4.4.1" || v.Graphics != "4.4.1" || v.Language != "4.4.1" || validateDECTVersion(v.HeadTunes) != nil || validateDECTVersion(v.BaseTunes) != nil {
		return errors.New("invalid Engage 75 original version evidence")
	}
	return nil
}

func engage75Selection(a dectRuntimeAssessment, device USBDevice) (engage75SelectedIdentity, error) {
	if a.State != dectAssessmentNeedsBootloaderIdentity || a.phase != "runtime-management" || device.attachment == nil || device.VendorID != JabraVendorID || device.ProductID != 0x1113 || device.ViaDongle || device.Serial == "" || a.Identity.Base.Address != 1 || a.Identity.Base.Serial != device.Serial || a.Headset.Serial.State != dectMeasured || a.Headset.Serial.Value == "" {
		return engage75SelectedIdentity{}, errors.New("engage 75 preview needs fresh original runtime evidence and serials")
	}
	// Revalidate observation states at this authority boundary, not only in the
	// runtime planner. The expected IDs do not fill missing observation values.
	if a.Headset.PID.State != dectRejected || a.Headset.DFUPID.State != dectRejected || a.Display.PID.State != dectRejected || a.Display.Serial.State != dectRejected || a.Display.DFUPID.State != dectRejected || a.Display.Image.State != dectRejected || a.Headset.Image.State != dectMeasured || a.Headset.Image.Value.ID != 0x0b0e1116 || a.Headset.Image.Value.Mode != 1 {
		return engage75SelectedIdentity{}, errors.New("engage 75 original NAK and image evidence is incomplete")
	}
	i := a.Identity
	s := engage75SelectedIdentity{Scheme: engage75SelectionV2, RuntimePID: i.Base.PID, BootPID: i.Base.BootPID, USBPort: filepath.Base(device.SysPath), USBSerialSHA256: engage75Digest(device.Serial), HeadSerialSHA256: engage75Digest(i.Headset.Serial), BaseVariant: i.Base.Variant, HeadVariant: i.Headset.Variant, DisplayVariant: i.MMI.Variant, Unavailable: engage75Unavailable,
		HeadRuntimeInfo: a.Headset.Image.Value, OriginalVersions: engage75Versions{Base: i.Base.Version, Head: i.Headset.Version, Display: i.MMI.Version, Radio: i.RadioVersion, Graphics: i.Graphics, Language: i.Language, HeadTunes: i.HeadsetTunes, BaseTunes: i.BaseTunes}, LanguageRegion: i.LanguageRegion, HeadRegion: i.HeadsetRegion, BaseRegion: i.BaseRegion}
	return s, s.validate()
}

func validEngage75Images(images []engage75MeasuredImage) bool {
	if len(images) != 3 {
		return false
	}
	for n, expected := range []struct {
		address byte
		image   uint32
	}{{1, 0x0b0e1111}, {10, 0x0b0e1116}, {3, 0x0b0e1117}} {
		image := images[n]
		if image.Address != expected.address || image.ImageID != expected.image || image.ReportedMode > 1 || n == 0 && image.ReportedMode != 0 {
			return false
		}
	}
	return true
}

func validEngage75RecoveryV2(state firmwareRecoveryState) error {
	v := state.SitelDECTV2
	if v == nil || state.FormatVersion != firmwareRecoveryStateVersion || v.Version != 2 || v.Selected.validate() != nil || !validEngage75Images(v.Images) || state.Protocol != 4 || state.SitelDECT != nil || state.SitelOTA != nil || state.Conexant != nil || state.Camera != nil || state.UVCCamera != nil || state.PanaCast50 != nil || state.CSRLanguage != nil || state.ControllerIdentitySHA256 != "" || state.TargetIdentitySHA256 != "" || state.Language != 0 ||
		state.RuntimePID != v.Selected.RuntimePID || state.BootPID != v.Selected.BootPID || state.USBPort != v.Selected.USBPort || state.USBSerialSHA256 != v.Selected.USBSerialSHA256 || !validEngage75Digest(state.ArchiveSHA256) || !reflect.DeepEqual(state.TargetUSBPIDs, []string{"0x1111"}) || state.ProductName != "Jabra_Engage_75" || state.FirmwareVersion != "5.20.1" || state.Attempt != 1 {
		return errors.New("invalid Engage 75 V2 recovery evidence")
	}
	if v.RadioRevision != 0 && byte(v.RadioRevision) != 0x28 && byte(v.RadioRevision) != 0x35 || v.SettingsVerified && v.RadioRevision == 0 {
		return errors.New("invalid Engage 75 V2 radio evidence")
	}
	if _, err := time.Parse(time.RFC3339, state.StartedAt); err != nil {
		return errors.New("invalid Engage 75 V2 start time")
	}
	switch state.Phase {
	case "images-verified", "transfer", "returning", "final-verified", "unresolved":
	default:
		return errors.New("invalid Engage 75 V2 recovery phase")
	}
	return nil
}

func (p engage75Pending) validate() error {
	if p.Version != 1 || p.Purpose != "engage75-pending-inspection" || !validEngage75Digest(p.ArchiveSHA256) || p.Selected.validate() != nil {
		return errors.New("invalid Engage 75 pending inspection")
	}
	validOutcome := func(s string) bool {
		switch s {
		case "not-sent", "intent", "replied", "possibly-sent", "rejected":
			return true
		}
		return false
	}
	if !validOutcome(p.Entry) || !validOutcome(p.HeadReturn) || !validOutcome(p.BaseReturn) {
		return errors.New("invalid Engage 75 command outcome")
	}
	switch p.Phase {
	case "pending-inspection":
		if p.Entry != "not-sent" {
			return errors.New("unexpected pending entry")
		}
	case "entry-intent", "bound-boot-attachment", "images-verified", "recovery-bound", "transfer", "returning", "final-verified", "unresolved":
		if p.Entry == "not-sent" {
			return errors.New("missing Engage 75 entry intent")
		}
	default:
		return errors.New("invalid Engage 75 pending phase")
	}
	if p.Unresolved != (p.Phase == "unresolved") {
		return errors.New("contradictory Engage 75 pending status")
	}
	if p.Phase != "returning" && p.Phase != "final-verified" && p.Phase != "unresolved" && (p.HeadReturn != "not-sent" || p.BaseReturn != "not-sent") {
		return errors.New("premature Engage 75 return outcome")
	}
	if p.Phase == "final-verified" && (p.HeadReturn != "replied" || !p.HeadMainVerified || p.BaseReturn != "replied" && p.BaseReturn != "possibly-sent") {
		return errors.New("incomplete Engage 75 final return")
	}
	if p.HeadMainVerified && p.HeadReturn != "replied" {
		return errors.New("unconfirmed Engage 75 headset return observation")
	}
	if p.BaseReturn != "not-sent" && (p.HeadReturn != "replied" || !p.HeadMainVerified) {
		return errors.New("base return precedes confirmed headset return")
	}
	return nil
}

func engage75PendingPath() (string, error) {
	path, err := firmwareRecoveryStatePath()
	return filepath.Join(filepath.Dir(path), "engage75-pending-inspection.json"), err
}

func loadEngage75Pending() (engage75Pending, error) {
	var p engage75Pending
	path, err := engage75PendingPath()
	if err != nil {
		return p, err
	}
	// O_NOFOLLOW and file limits match the archive's hardened reader. The state
	// is private, strict JSON; it is never interpreted as firmware authority.
	file, info, err := openFirmwareRead(path)
	if err != nil {
		return p, err
	}
	defer func() { _ = file.Close() }()
	if info.Size() > 16*1024 || info.Mode().Perm()&0o077 != 0 {
		return p, errors.New("invalid Engage 75 pending file permissions or size")
	}
	encoded, err := io.ReadAll(io.LimitReader(file, 16*1024+1))
	if err != nil {
		return p, err
	}
	if err := rejectEngage75DuplicateJSON(encoded); err != nil {
		return p, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return p, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return p, errors.New("trailing Engage 75 pending data")
	}
	return p, p.validate()
}

func saveEngage75Pending(p engage75Pending) error {
	if err := p.validate(); err != nil {
		return err
	}
	path, err := engage75PendingPath()
	if err != nil {
		return err
	}
	return saveFirmwareStateFile(path, p)
}

func refuseEngage75Pending() error {
	_, err := loadEngage75Pending()
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("unfinished Engage 75 preview found; do not retry automatically: the base mode and original docked headset need checking")
}

func clearEngage75Pending() error {
	path, err := engage75PendingPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncFirmwareStateDirectory(filepath.Dir(path))
}

// encoding/json otherwise silently accepts duplicate object fields. V2 and
// pending records are strict evidence, so an overwritten identity is invalid.
// Legacy records retain their existing decoder/comparison behavior.
func rejectEngage75DuplicateJSON(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var readValue func() error
	readValue = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return nil
		}
		switch delimiter {
		case '{':
			var seen []string
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				duplicate := false
				for _, previous := range seen {
					if strings.EqualFold(previous, name) {
						duplicate = true
						break
					}
				}
				if !ok || duplicate {
					return errors.New("duplicate or invalid Engage 75 JSON field")
				}
				seen = append(seen, name)
				if err := readValue(); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := readValue(); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid Engage 75 JSON container")
		}
		_, err = decoder.Token()
		return err
	}
	if err := readValue(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing Engage 75 JSON data")
	}
	return nil
}

// Detect the V2 field before interpreting its pointer value. A case alias
// ending in null must not erase the fact that the document used this schema.
func hasEngage75V2Field(encoded []byte) bool {
	var top map[string]json.RawMessage
	if json.Unmarshal(encoded, &top) != nil {
		return false
	}
	for key := range top {
		if strings.EqualFold(key, "sitelDectV2") {
			return true
		}
	}
	return false
}
