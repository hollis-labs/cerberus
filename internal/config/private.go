package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Modes for Cerberus's own state. ~/.cerberus holds the config (which can
// carry literal env values), alerts, pid files, service logs and the state
// every subsystem keeps; on macOS every standard account shares the staff
// group, so 0750 is not private either.
const (
	PrivateDirMode  fs.FileMode = 0o700
	PrivateFileMode fs.FileMode = 0o600
)

// EnsurePrivate makes Cerberus's state private to the operator: the state
// directory (created if absent) is 0700, which puts everything inside out
// of other accounts' reach whatever its own mode, and the config file at
// configPath, when it exists, is 0600. It runs at every start, before any
// writer, so the first writer can no longer decide the directory's mode.
//
// It returns one line per mode it had to change, for the caller to print:
// a state directory that was readable by other accounts is worth saying out
// loud, once, when it is fixed.
func EnsurePrivate(configPath string) ([]string, error) {
	var fixed []string
	dir := filepath.Dir(DefaultPath())
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if mkErr := os.MkdirAll(dir, PrivateDirMode); mkErr != nil {
			return nil, fmt.Errorf("create %s: %w", dir, mkErr)
		}
		// MkdirAll is subject to the umask; state it exactly.
		if chErr := os.Chmod(dir, PrivateDirMode); chErr != nil {
			return nil, chErr
		}
	case err != nil:
		return nil, err
	case !info.IsDir():
		return nil, fmt.Errorf("%s is not a directory", dir)
	case info.Mode().Perm() != PrivateDirMode:
		if chErr := os.Chmod(dir, PrivateDirMode); chErr != nil {
			return nil, fmt.Errorf("make %s private: %w", dir, chErr)
		}
		fixed = append(fixed, fmt.Sprintf("%s was mode %04o, readable by other accounts on this machine; it is now %04o", dir, info.Mode().Perm(), PrivateDirMode))
	}
	if configPath == "" {
		return fixed, nil
	}
	info, err = os.Stat(configPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fixed, err
	case info.Mode().IsRegular() && info.Mode().Perm()&0o077 != 0:
		if err := os.Chmod(configPath, PrivateFileMode); err != nil {
			return fixed, fmt.Errorf("make %s private: %w", configPath, err)
		}
		fixed = append(fixed, fmt.Sprintf("%s was mode %04o, readable by other accounts on this machine; it is now %04o", configPath, info.Mode().Perm(), PrivateFileMode))
	}
	return fixed, nil
}
