// Package pausectl provides auto-restart pause/resume flag management.
// This is a standalone package to avoid import cycles between daemon and service.
package pausectl

import (
	"fmt"
	"os"
	"path/filepath"
)

// pauseDir returns ~/.cerberus/, creating it if needed.
func pauseDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	dir := filepath.Join(home, ".cerberus")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create cerberus dir: %w", err)
	}
	return dir, nil
}

// globalPausePath returns the path to ~/.cerberus/paused.
func globalPausePath() (string, error) {
	dir, err := pauseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "paused"), nil
}

// servicePausePath returns the path to ~/.cerberus/paused.<serviceID>.
func servicePausePath(serviceID string) (string, error) {
	dir, err := pauseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "paused."+serviceID), nil
}

// PauseAll creates the global pause flag file, disabling auto-restart for all services.
func PauseAll() error {
	path, err := globalPausePath()
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte("paused\n"), 0644)
}

// ResumeAll removes the global pause flag file, re-enabling auto-restart for all services.
func ResumeAll() error {
	path, err := globalPausePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// PauseService creates a per-service pause flag file.
func PauseService(serviceID string) error {
	path, err := servicePausePath(serviceID)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte("paused\n"), 0644)
}

// ResumeService removes the per-service pause flag file.
func ResumeService(serviceID string) error {
	path, err := servicePausePath(serviceID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// IsGloballyPaused returns true if the global pause flag is set.
func IsGloballyPaused() bool {
	path, err := globalPausePath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// IsServicePaused returns true if a specific service has its auto-restart paused.
// This checks both the global flag and the per-service flag.
func IsServicePaused(serviceID string) bool {
	if IsGloballyPaused() {
		return true
	}
	path, err := servicePausePath(serviceID)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}
