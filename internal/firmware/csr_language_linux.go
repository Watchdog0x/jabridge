package firmware

import (
	"context"
	"errors"
	"time"
)

type nativeCSRLanguageBackend struct {
	device             USBDevice
	wantedLanguage     uint16
	wantedTunesVersion string
}

func (backend *nativeCSRLanguageBackend) Open(ctx context.Context) (csrLanguageConnection, error) {
	return openCSRLanguageConnection(ctx, backend.device)
}

func openCSRLanguageConnection(ctx context.Context, device USBDevice) (csrLanguageConnection, error) {
	if device.ProductID != 0x2549 || device.ViaDongle {
		return csrLanguageConnection{}, errors.New("connect the Evolve3 65 directly by USB to change its voice language")
	}
	transport, err := openBoundCSR(device)
	if err != nil {
		return csrLanguageConnection{}, err
	}
	connection, err := identifyCSRLanguageConnection(ctx, csrContextHID{transport}, device, transport.reportSize, transport.Close)
	if err != nil {
		_ = transport.Close()
	}
	return connection, err
}

func identifyCSRLanguageConnection(ctx context.Context, transport csrStageIO, device USBDevice, reportSize int, closeConnection func() error) (csrLanguageConnection, error) {
	core, err := identifyCSRConnectionForLanguage(ctx, transport, device, reportSize, closeConnection, false, 0x74)
	if err != nil {
		return csrLanguageConnection{}, err
	}
	session := &csrStageTransfer{io: transport, stage: csrExtendedStage{Address: core.Address, ReportSize: reportSize, Timeout: time.Second}, seq: core.Address*16 + 64}
	version, err := session.query(ctx, 2, 0x21)
	if err != nil {
		return csrLanguageConnection{}, err
	}
	text, err := decodeExtendedIdentityString(version)
	if err != nil {
		return csrLanguageConnection{}, err
	}
	result := csrLanguageConnection{csrExtendedConnection: core, TunesVersion: text}
	result.Battery = func(ctx context.Context) (int, bool, error) {
		data, err := session.query(ctx, 0x12, 2)
		if err != nil {
			return 0, false, err
		}
		if len(data) < 2 || data[1] > 100 {
			return 0, false, errors.New("invalid headset battery reply")
		}
		return int(data[1]), data[0]&1 != 0, nil
	}
	if err := validateCSRLanguageConnection(result); err != nil {
		return csrLanguageConnection{}, err
	}
	return result, nil
}

func (backend *nativeCSRLanguageBackend) Reconnect(ctx context.Context, before csrExtendedIdentity) (csrLanguageConnection, error) {
	for {
		if err := ctx.Err(); err != nil {
			return csrLanguageConnection{}, err
		}
		devices, err := enumerateBoundUSB()
		if err != nil {
			return csrLanguageConnection{}, err
		}
		for _, device := range devices {
			if device.SysPath != backend.device.SysPath || device.ProductID != before.PID || device.VendorID != JabraVendorID {
				continue
			}
			if device.attachment == nil || device.attachment.fingerprint == before.Attachment {
				continue
			}
			if backend.device.Serial != "" && device.Serial != backend.device.Serial {
				return csrLanguageConnection{}, errors.New("USB identity changed while changing voice language")
			}
			connection, err := openCSRLanguageConnection(ctx, device)
			if ctx.Err() != nil {
				if connection.Close != nil {
					_ = connection.Close()
				}
				return csrLanguageConnection{}, ctx.Err()
			}
			if err != nil {
				continue
			}
			if connection.Identity.Serial != before.Serial || connection.Identity.Variant != before.Variant {
				_ = connection.Close()
				return csrLanguageConnection{}, errors.New("headset identity changed while changing voice language")
			}
			if connection.Identity.Version != before.Version {
				_ = connection.Close()
				return csrLanguageConnection{}, errors.New("main headset firmware changed during the voice update")
			}
			if backend.wantedLanguage != 0 && (connection.Identity.Language != backend.wantedLanguage || connection.TunesVersion != backend.wantedTunesVersion) {
				_ = connection.Close()
				continue
			}
			return connection, nil
		}
		// Full quiet interval between failed read-only observations.
		if err := waitDFU(ctx, time.Second); err != nil {
			return csrLanguageConnection{}, err
		}
	}
}
