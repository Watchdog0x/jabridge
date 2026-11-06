package firmware

import (
	"context"
	"errors"
)

// Reported states (0 loader, 1 Main) are separate from the request selector3
// (Other). This policy belongs only to the preview, not generic DECT decoding.
type engage75PreviewPeer struct {
	next           sitelRequest
	address        byte
	transitionFrom byte
	loaderInfo     *sitelDeviceInfo
}

func (p engage75PreviewPeer) request(ctx context.Context, op byte, data []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, managementNotSent(err)
	}
	if op == 0 {
		return p.next.request(ctx, op, data)
	}
	// An information request immediately before every effect catches mode/image
	// changes after preparation. Never use runtime geometry for loader transfers.
	raw, err := p.next.request(ctx, 0, nil)
	if err != nil {
		return nil, managementNotSent(err)
	}
	info, err := decodeSitelInfo(raw)
	expected := uint32(0x0b0e1111)
	switch p.address {
	case 10:
		expected = 0x0b0e1116
	case 3:
		expected = 0x0b0e1117
	case 1:
	default:
		return nil, managementNotSent(errors.New("unsupported Engage 75 component"))
	}
	if err != nil || info.ID != expected || info.Mode > 1 {
		return nil, managementNotSent(errors.New("engage 75 current image or mode did not verify"))
	}
	if op == 5 {
		if len(data) != 1 || data[0] != 3 || info.Mode != p.transitionFrom || p.transitionFrom == 1 && p.address == 1 || p.transitionFrom == 0 && p.address == 3 {
			return nil, managementNotSent(errors.New("engage 75 program transition is not qualified"))
		}
	} else {
		if op > 4 || info.Mode != 0 {
			return nil, managementNotSent(errors.New("engage 75 operation needs its verified loader"))
		}
		if p.loaderInfo != nil && info != *p.loaderInfo {
			return nil, managementNotSent(errors.New("engage 75 loader geometry changed after preparation"))
		}
	}
	return p.next.request(ctx, op, data)
}

func prepareEngage75Preview(ctx context.Context, backend sitelDECTBackend, connection *sitelDECTConnection, archive *sitelDECTArchive) ([]sitelDECTPrepared, error) {
	return prepareSitelDECTTransferPolicy(ctx, backend, connection, archive, true)
}
