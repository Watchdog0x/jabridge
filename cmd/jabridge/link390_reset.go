package main

import (
	"context"
	"errors"
	"strings"
	"time"
)

type link390ResetOutcome string

const (
	resetRefused         link390ResetOutcome = "refused"
	resetRejected        link390ResetOutcome = "rejected"
	resetUnknown         link390ResetOutcome = "unknown"
	resetRequested       link390ResetOutcome = "requested"
	resetRestartVerified link390ResetOutcome = "restart-verified"
)

type resetPairingSummary struct {
	Named, Nameless, Connected int
	Complete                   bool
}
type link390ResetSummary struct {
	WirelessRange                          *byte
	Pairings                               resetPairingSummary
	AutoPairing, BluetoothRadio, Softphone *bool
}
type link390ResetResult struct {
	Outcome                link390ResetOutcome
	Attempted, Reconnected bool
	Identity               link390Identity
	Before, After          link390ResetSummary
	Detail                 string
	cause                  error
}
type link390ResetSession interface {
	Identity() (link390Identity, error)
	Attachment() (string, error)
	Summary() link390ResetSummary
	Reset() error
	Close()
}
type link390ResetSource interface {
	OpenReset(link390Identity, bool) (link390ResetSession, error)
	BeginResetAttempt(link390Identity) error
	SetResetOutcome(link390Identity, link390ResetOutcome)
	Now() time.Time
	Sleep(context.Context, time.Duration) error
}

func resetLink390IdentityAllowed(i link390Identity) bool {
	return i.valid() && i.ProductID == 0x2e57 && i.Variant == "04-10" && i.Firmware == "1.3.0"
}

// A returned attachment proves a restart, never erased pairings or defaults.
// A lost ACK stays unknown even when a matching device returns after a replug.
func runLink390Reset(ctx context.Context, source link390ResetSource, target link390Identity) (result link390ResetResult) {
	result.Outcome = resetRefused
	defer func() {
		if result.Attempted {
			source.SetResetOutcome(target, result.Outcome)
		}
	}()
	fail := func(err error) link390ResetResult { result.Detail = err.Error(); result.cause = err; return result }
	if !resetLink390IdentityAllowed(target) {
		return fail(errLink390Identity)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	initial, err := source.OpenReset(target, false)
	if err != nil {
		return fail(err)
	}
	identity, err := initial.Identity()
	if err != nil || !target.sameDevice(identity) || identity.Generation != target.Generation {
		initial.Close()
		return fail(errors.Join(errLink390Identity, err))
	}
	result.Before = initial.Summary()
	generation, err := initial.Attachment()
	if err != nil || generation != target.Generation {
		initial.Close()
		return fail(errors.Join(errLink390Changed, err))
	}
	if err := ctx.Err(); err != nil {
		initial.Close()
		return fail(err)
	}
	if err := source.BeginResetAttempt(target); err != nil {
		initial.Close()
		return fail(err)
	}
	result.Attempted = true
	sendErr := initial.Reset() // One frame. No permission, END, payload, or replay.
	initial.Close()
	if sendErr != nil && strings.Contains(sendErr.Error(), "GNP NAK") {
		result.Outcome = resetRejected
		return fail(sendErr)
	}
	result.Outcome = resetRequested
	if sendErr != nil {
		result.Outcome = resetUnknown
		result.Detail = sendErr.Error()
		result.cause = sendErr
	}
	deadline := source.Now().Add(45 * time.Second)
	lastGeneration := ""
	streak := 0
	for source.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return fail(errors.Join(sendErr, err))
		}
		current, openErr := source.OpenReset(target, true)
		if openErr != nil {
			streak = 0
			if errors.Is(openErr, errLink390Identity) {
				result.Outcome = resetUnknown
				return fail(openErr)
			}
		} else {
			fresh, identityErr := current.Identity()
			if identityErr == nil && !target.sameDevice(fresh) || errors.Is(identityErr, errLink390Identity) {
				current.Close()
				result.Outcome = resetUnknown
				return fail(errLink390Identity)
			}
			if identityErr == nil && fresh.Generation != target.Generation {
				after, attachmentErr := current.Attachment()
				if attachmentErr == nil && after == fresh.Generation {
					if lastGeneration == fresh.Generation {
						streak++
					} else {
						streak = 1
					}
					lastGeneration = fresh.Generation
					if streak >= 2 {
						result.After = current.Summary()
						final, finalErr := current.Attachment()
						current.Close()
						if finalErr != nil || final != fresh.Generation {
							result.Outcome = resetUnknown
							return fail(errors.Join(errLink390Changed, finalErr))
						}
						result.Identity, result.Reconnected = fresh, true
						if sendErr == nil {
							result.Outcome = resetRestartVerified
						}
						return result
					}
				} else {
					streak = 0
				}
			} else {
				streak = 0
			}
			current.Close()
		}
		if err := source.Sleep(ctx, time.Second); err != nil {
			return fail(errors.Join(sendErr, err))
		}
	}
	if result.Detail == "" {
		result.Detail = "The USB restart and return were not confirmed."
	}
	return result
}
