package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var errLink390Uncertain = errors.New("configuration completion was not acknowledged")

type link390ChangeStatus string

const (
	link390Applied    link390ChangeStatus = "applied"
	link390NotApplied link390ChangeStatus = "not applied"
	link390Unknown    link390ChangeStatus = "unknown"
)

type link390Identity struct {
	Port, USBSerial, GNPSerial, Generation, HIDPath string
	VendorID, ProductID                             uint16
	Firmware, Variant                               string
}

func (i link390Identity) valid() bool {
	serial := strings.TrimSpace(i.USBSerial)
	return i.VendorID == jabraVendorID && scopedControlPID(i.ProductID) && i.Firmware == "3.2.0" && i.Variant == "04-10" &&
		filepath.IsAbs(i.Port) && filepath.Clean(i.Port) != "/" &&
		filepath.IsAbs(i.HIDPath) && i.Generation != "" &&
		serial != "" && serial == i.USBSerial && serial == i.GNPSerial &&
		strings.Trim(strings.TrimPrefix(strings.ToLower(serial), "0x"), "0-:") != ""
}

func (i link390Identity) sameDevice(other link390Identity) bool {
	return i.valid() && other.valid() && i.Port == other.Port &&
		i.VendorID == other.VendorID && i.ProductID == other.ProductID &&
		i.USBSerial == other.USBSerial && i.GNPSerial == other.GNPSerial && i.Firmware == other.Firmware && i.Variant == other.Variant
}

type link390ConfigResult struct {
	Key       string
	Attempted bool
	Status    link390ChangeStatus
	ReadBack  *byte
	Identity  link390Identity
}

func (r link390ConfigResult) failure(cause error) (link390ConfigResult, error) {
	value := "unknown"
	if r.ReadBack != nil {
		value = link390ConfigValueName(r.Key, *r.ReadBack)
	}
	return r, fmt.Errorf("change %s (last read: %s); read the setting again: %w", r.Status, value, cause)
}

type link390ConfigSession interface {
	Identity() (link390Identity, error)
	Attachment() (string, error)
	Read() (byte, error)
	// The first result is true if permission was granted or might have been
	// granted. An uncertain reply still needs original-handle cleanup.
	Enter() (bool, error)
	Write(byte) error
	End() error
	Close()
}

type link390ConfigSource interface {
	Open(target link390Identity, readOnly bool) (link390ConfigSession, error)
	Now() time.Time
	Sleep(context.Context, time.Duration) error
}

// The caller owns the per-port exclusive permit across the complete flow.
// Reopened handles are read-only; only the first handle can send configuration.
func runLink390ConfigChange(ctx context.Context, source link390ConfigSource, target link390Identity, key string, desired byte, expected *byte) (link390ConfigResult, error) {
	result := link390ConfigResult{Key: key, Status: link390NotApplied}
	definition, known := link390ConfigDefinitionForKey(key)
	if !known || !definition.allows(desired) {
		return result.failure(ErrNotSupported)
	}
	if !target.valid() {
		return result.failure(errLink390Identity)
	}
	if err := ctx.Err(); err != nil {
		return result.failure(err)
	}
	initial, err := source.Open(target, false)
	if err != nil {
		return result.failure(err)
	}
	var cleanupOnce sync.Once
	var endErr error
	mayBeOpen := false
	cleanup := func() {
		cleanupOnce.Do(func() {
			defer initial.Close()
			if !mayBeOpen {
				return
			}
			generation, err := initial.Attachment()
			if err != nil {
				// Retry only the local identity observation, never the setting
				// write. Cleanup remains bounded even after caller cancellation.
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				if source.Sleep(cleanupCtx, 50*time.Millisecond) == nil {
					generation, err = initial.Attachment()
				}
				cancel()
			}
			if err != nil {
				endErr = fmt.Errorf("cannot verify the original attachment for cleanup: %w", err)
				return
			}
			if generation != target.Generation {
				endErr = errLink390Changed
				return
			}
			endErr = initial.End()
		})
	}
	defer cleanup()
	identity, err := initial.Identity()
	if err != nil || !target.sameDevice(identity) || identity.Generation != target.Generation {
		return result.failure(errors.Join(errLink390Identity, err))
	}
	value, err := initial.Read()
	if err != nil {
		return result.failure(err)
	}
	if generation, err := initial.Attachment(); err != nil || generation != identity.Generation {
		return result.failure(errors.Join(errLink390Changed, err))
	}
	result.ReadBack = &value
	if expected != nil && value != *expected {
		return result.failure(errors.New("setting changed while editing; reload before saving"))
	}
	if value == desired {
		result.Status, result.Identity = link390Applied, identity
		return result, nil
	}
	mayBeOpen, err = initial.Enter()
	if err == nil && !mayBeOpen {
		err = errors.New("the device did not grant configuration permission")
	}
	if err != nil {
		cleanup()
		return result.failure(errors.Join(err, endErr))
	}
	generation, err := initial.Attachment()
	if err != nil || generation != target.Generation {
		cleanup()
		return result.failure(errors.Join(errLink390Changed, err, endErr))
	}
	writeErr := initial.Write(desired) // Exactly one setting write; never replay it.
	result.Attempted = true
	result.Status = link390Unknown
	result.ReadBack = nil
	cleanup()
	if err := source.Sleep(ctx, 3*time.Second); err != nil {
		return result.failure(errors.Join(writeErr, endErr, err))
	}
	deadline := source.Now().Add(30 * time.Second)
	var lastErr error
	lastGeneration := ""
	streak := 0
	lastValue := byte(0)
	for source.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return result.failure(errors.Join(writeErr, endErr, err))
		}
		current, openErr := source.Open(target, true)
		if openErr == nil {
			fresh, identityErr := current.Identity()
			if identityErr == nil && !target.sameDevice(fresh) {
				current.Close()
				return result.failure(errors.Join(writeErr, endErr, errLink390Identity))
			}
			if identityErr == nil {
				value, readErr := current.Read()
				// A detach during the read invalidates both its value and serial.
				after, attachmentErr := current.Attachment()
				if readErr == nil && attachmentErr == nil && after == fresh.Generation {
					copyValue := value
					result.ReadBack = &copyValue
					if lastGeneration == fresh.Generation && lastValue == value {
						streak++
					} else {
						streak = 1
					}
					lastGeneration, lastValue, lastErr = fresh.Generation, value, nil
					if streak >= 2 && value == desired {
						current.Close()
						if writeErr == nil && (endErr == nil || errors.Is(endErr, errLink390Uncertain) || errors.Is(endErr, errLink390Changed) && fresh.Generation != target.Generation) {
							result.Status, result.Identity = link390Applied, fresh
							return result, nil
						}
						return result.failure(errors.Join(writeErr, endErr))
					}
				} else {
					streak = 0
					lastErr = errors.Join(readErr, attachmentErr)
				}
			} else {
				streak, lastErr = 0, identityErr
			}
			current.Close()
		} else {
			streak, lastErr = 0, openErr
			if errors.Is(openErr, errLink390Identity) {
				return result.failure(errors.Join(writeErr, endErr, openErr))
			}
		}
		if err := source.Sleep(ctx, time.Second); err != nil {
			return result.failure(errors.Join(writeErr, endErr, lastErr, err))
		}
	}
	if streak >= 2 && lastValue != desired {
		result.Status = link390NotApplied
	}
	return result.failure(errors.Join(writeErr, endErr, lastErr, errors.New("setting read-back was not confirmed before timeout")))
}
