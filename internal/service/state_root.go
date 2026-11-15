package service

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
)

// stateRoot is where pids/ and locks/ live. Empty means ~/.cerberus, the old build's tree. The
// Substrate build sets its data directory, because it never reads or writes the old tree's
// pids/ or locks/ (docs/substrate-port-plan.md, section 3).
var stateRoot atomic.Pointer[string]

// SetStateRoot puts pids/ and locks/ under dir. Call it once at start, before any service runs.
func SetStateRoot(dir string) { stateRoot.Store(&dir) }

// stateDir returns the directory holding pids/ and locks/.
func stateDir() (string, error) {
	if p := stateRoot.Load(); p != nil && *p != "" {
		return *p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".cerberus"), nil
}
