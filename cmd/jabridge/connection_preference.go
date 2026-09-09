package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const connectionPreferenceFileName = "connection.json"

type connectionPreference struct {
	Connection string `json:"connection"`
	ProductID  uint16 `json:"productId"`
	Name       string `json:"name,omitempty"`
}

var (
	connectionPreferenceMu      sync.Mutex
	connectionPreferenceLoaded  bool
	connectionPreferencePresent bool
	connectionPreferenceValue   connectionPreference
	connectionUserConfigDir     = os.UserConfigDir
)

func saveSelectedConnectionPreference() error {
	headset, exists := selectedHeadsetSnapshot()
	if !exists || headset == nil || headset.isDongle {
		return nil
	}
	connection := "usb"
	if headset.deviceConnection == deviceConnectionType_BT {
		connection = "dongle"
	}
	preference := connectionPreference{
		Connection: connection,
		ProductID:  headset.productID,
		Name:       strings.TrimSpace(headset.deviceName),
	}
	path, err := connectionPreferencePath()
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create Jabridge settings directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".connection-*.tmp")
	if err != nil {
		return fmt.Errorf("create connection preference: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure connection preference: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(preference); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write connection preference: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync connection preference: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close connection preference: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install connection preference: %w", err)
	}

	connectionPreferenceMu.Lock()
	connectionPreferenceLoaded = true
	connectionPreferencePresent = true
	connectionPreferenceValue = preference
	connectionPreferenceMu.Unlock()
	return nil
}

func applySelectedConnectionPreference() {
	preference, exists := loadConnectionPreference()
	if !exists {
		return
	}

	deviceStateMu.Lock()
	candidate := preferredHeadsetLocked(preference)
	if candidate < 0 {
		deviceStateMu.Unlock()
		return
	}
	changed := selectedHeadset != candidate
	selectedHeadset = candidate
	if device := deviceManager[candidate]; device != nil && device.deviceConnection == deviceConnectionType_BT {
		for id, possibleDongle := range deviceManager {
			if possibleDongle != nil && possibleDongle.isDongle && possibleDongle.deviceID == device.parentDeviceID {
				if selectedDongle != id {
					changed = true
				}
				selectedDongle = id
				break
			}
		}
	}
	deviceStateMu.Unlock()
	if changed {
		requestUIRedraw()
	}
}

func preferredHeadsetLocked(preference connectionPreference) int {
	wantedConnection := deviceConnectionType_USB
	if preference.Connection == "dongle" {
		wantedConnection = deviceConnectionType_BT
	}
	type rankedDevice struct {
		id   int
		rank int
	}
	best := rankedDevice{id: -1, rank: -1}
	wantedName := strings.ToLower(strings.TrimSpace(preference.Name))
	for id, device := range deviceManager {
		if device == nil || device.isDongle || device.deviceConnection != wantedConnection {
			continue
		}
		rank := 0
		if preference.ProductID != 0 && device.productID == preference.ProductID {
			rank += 2
		}
		if wantedName != "" && strings.EqualFold(strings.TrimSpace(device.deviceName), wantedName) {
			rank++
		}
		if best.id < 0 || rank > best.rank || (rank == best.rank && id < best.id) {
			best = rankedDevice{id: id, rank: rank}
		}
	}
	if best.id < 0 {
		return -1
	}
	// When identity was stored, do not silently choose an unrelated headset.
	if (preference.ProductID != 0 || wantedName != "") && best.rank == 0 {
		return -1
	}
	return best.id
}

func loadConnectionPreference() (connectionPreference, bool) {
	connectionPreferenceMu.Lock()
	defer connectionPreferenceMu.Unlock()
	if connectionPreferenceLoaded {
		return connectionPreferenceValue, connectionPreferencePresent
	}
	connectionPreferenceLoaded = true
	path, err := connectionPreferencePath()
	if err != nil {
		return connectionPreference{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return connectionPreference{}, false
	}
	var preference connectionPreference
	if err := json.Unmarshal(data, &preference); err != nil ||
		(preference.Connection != "usb" && preference.Connection != "dongle") {
		return connectionPreference{}, false
	}
	connectionPreferenceValue = preference
	connectionPreferencePresent = true
	return preference, true
}

func connectionPreferencePath() (string, error) {
	directory, err := connectionUserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user settings directory: %w", err)
	}
	if strings.TrimSpace(directory) == "" {
		return "", errors.New("user settings directory is empty")
	}
	return filepath.Join(directory, "jabridge", connectionPreferenceFileName), nil
}
