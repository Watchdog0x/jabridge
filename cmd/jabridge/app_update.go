package main

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// completeAppUpdate runs only after the release has been verified and installed.
// Completion is independent of the service's state. Do not use this process's
// embedded script: this process still contains the previous release's data.
func completeAppUpdate(executable string, serviceWasActive bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	completion, err := exec.CommandContext(ctx, executable, "completion", "bash").Output()
	if err != nil {
		return fmt.Errorf("read Bash completion from updated app: %w", err)
	}
	if err := installBashCompletion(completion); err != nil {
		return fmt.Errorf("install Bash completion: %w", err)
	}
	if err := syncInstalledUserBinary(executable); err != nil {
		return fmt.Errorf("sync updated service binary: %w", err)
	}
	if !serviceWasActive {
		return nil
	}
	if err := restartUserServiceAfterUpdate(); err != nil {
		return fmt.Errorf("restart updated service: %w", err)
	}
	return nil
}
