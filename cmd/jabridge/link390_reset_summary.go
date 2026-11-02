package main

import (
	"encoding/binary"
	"errors"
	"strconv"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

// Unlike the display list, this reader counts nameless records and requires
// every record and namespace to finish. A partial result can never pass a check.
func readPairingSummary(types []byte, query func(byte, []byte) ([]byte, error)) (resetPairingSummary, error) {
	summary := resetPairingSummary{}
	if len(types) == 0 {
		return summary, errors.New("pairing namespaces unavailable")
	}
	for _, btType := range types {
		index := uint16(0)
		seen := map[uint16]bool{}
		ended := false
		for records := 0; records < 256; records++ {
			if seen[index] {
				return summary, errors.New("pairing index loop")
			}
			seen[index] = true
			request := []byte{byte(index), byte(index >> 8), btType}
			payload, err := query(gnpOpGetDBName, request)
			if err != nil {
				return summary, err
			}
			next, name, err := parsePairingName(payload)
			if err != nil {
				return summary, err
			}
			record, err := query(gnpOpGetDBRecord, request)
			if err != nil {
				return summary, err
			}
			if len(record) < 12 || binary.LittleEndian.Uint16(record[:2]) != index {
				return summary, errors.New("pairing record index was not confirmed")
			}
			entry, err := parsePairingRecord(name, record, index, btType)
			if err != nil {
				return summary, err
			}
			if entry.deviceBTAddr == [6]byte{} {
				return summary, errors.New("zero-address pairing record has no qualified interpretation")
			}
			if name == "" {
				summary.Nameless++
			} else {
				summary.Named++
			}
			if entry.isConnected {
				summary.Connected++
			}
			if next == 0xffff {
				ended = true
				break
			}
			if next == index {
				return summary, errors.New("pairing list did not advance")
			}
			index = next
		}
		if !ended {
			return summary, errors.New("pairing list exceeded record limit")
		}
	}
	summary.Complete = true
	return summary, nil
}

func link390ResetChecks(result link390ResetResult) []ipc.ResetCheck {
	before, after := result.Before, result.After
	checks := []ipc.ResetCheck{}
	rangeCheck := ipc.ResetCheck{Key: "wirelessRange", State: "unavailable", Detail: "Before and after reads are unavailable."}
	if before.WirelessRange != nil && after.WirelessRange != nil {
		rangeCheck.State = "differs"
		rangeCheck.Detail = "The last range read differs from Normal."
		if *after.WirelessRange == 1 {
			rangeCheck.State = "pass"
			rangeCheck.Detail = "Range changed to Normal; this checks one setting only."
		}
		if *before.WirelessRange == 1 && *after.WirelessRange == 1 {
			rangeCheck.State = "not-proven"
			rangeCheck.Detail = "Range was already Normal before reset."
		}
	}
	checks = append(checks, rangeCheck)
	pairCheck := ipc.ResetCheck{Key: "namedPairings", State: "unavailable", Detail: "A full pairing summary is unavailable."}
	if after.Pairings.Complete {
		pairCheck.State = "differs"
		pairCheck.Detail = "Named records remain. This does not show whether they can connect."
		if after.Pairings.Named == 0 {
			pairCheck.State = "pass"
			pairCheck.Detail = "No named records were read. This does not prove an empty pairing database."
		}
	}
	checks = append(checks, pairCheck)
	count := ipc.ResetCheck{Key: "addressOnlyRecord", State: "unavailable", Detail: "A full pairing summary is unavailable."}
	if after.Pairings.Complete {
		count.State = "not-proven"
		count.After = strconv.Itoa(after.Pairings.Nameless)
		count.Detail = "Address-only records can remain. Their absence is not a failure."
	}
	checks = append(checks, count)
	for _, item := range []struct {
		key           string
		before, after *bool
	}{{"autoPairing", before.AutoPairing, after.AutoPairing}, {"bluetoothRadio", before.BluetoothRadio, after.BluetoothRadio}, {"softphone", before.Softphone, after.Softphone}} {
		check := ipc.ResetCheck{Key: item.key, State: "unavailable", Detail: "Before and after reads are unavailable."}
		if item.before != nil {
			check.Before = onOff(*item.before)
		}
		if item.after != nil {
			check.After = onOff(*item.after)
		}
		if item.before != nil && item.after != nil {
			check.State = "not-proven"
			check.Detail = "Read before and after; default values are not verified."
		}
		checks = append(checks, check)
	}
	return checks
}

func ipcLink390ResetResult(result link390ResetResult) ipc.BoundResetResult {
	message := "Nothing was sent. Check the selected dongle and try again."
	if errors.Is(result.cause, errSearchActive) {
		message = "Stop Find headset before resetting. Nothing was sent."
	}
	if errors.Is(result.cause, errResetAlreadyAttempted) {
		message = "Reset was already tried on this attachment. Reconnect the dongle before trying again."
	}
	switch result.Outcome {
	case resetRejected:
		message = "The dongle refused the reset. No second reset was sent."
	case resetUnknown:
		message = "Reset result unknown. Reconnect the dongle or restart the service before retrying."
	case resetRequested:
		message = "Reset accepted, but its restart was not confirmed. Reconnect it if needed."
	case resetRestartVerified:
		message = "The dongle restarted and returned. Headsets may need pairing again; an address-only record may remain."
	}
	return ipc.BoundResetResult{Outcome: string(result.Outcome), Attempted: result.Attempted, Reconnected: result.Reconnected, Message: message, Checks: link390ResetChecks(result)}
}
