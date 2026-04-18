package procscan

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ResolveCommandBinary returns the absolute path of the executable that
// command[0] would invoke. The returned path is suitable for Capture().
//
// Rules:
//   - Empty command → ("", error). Caller should skip cascade-kill.
//   - command[0] starts with "/" → returned as-is.
//   - command[0] starts with "./" or "../" or contains a "/" → resolved
//     relative to dir if dir is non-empty; otherwise returned as-is.
//   - bare name → resolved via exec.LookPath (uses PATH).
//
// Skip rules (returned with ErrSkipFingerprint, intended as "no-op"):
//   - command[0] is a known interpreter (go, sh, bash, python, node).
//     Fingerprinting these would match the toolchain binary, not the
//     service binary, which is wrong: rebuild doesn't replace the
//     toolchain. Operators wrap services with `go run` for development;
//     cascade-kill is a no-op for those services.
func ResolveCommandBinary(command []string, dir string) (string, error) {
	if len(command) == 0 || command[0] == "" {
		return "", fmt.Errorf("procscan: empty command")
	}
	bin := command[0]
	if isInterpreter(bin) {
		return "", ErrSkipFingerprint
	}
	switch {
	case filepath.IsAbs(bin):
		return bin, nil
	case strings.ContainsRune(bin, '/'):
		// Relative path with directory component.
		if dir != "" {
			return filepath.Join(dir, bin), nil
		}
		return bin, nil
	default:
		path, err := exec.LookPath(bin)
		if err != nil {
			return "", fmt.Errorf("procscan: lookpath %q: %w", bin, err)
		}
		return path, nil
	}
}

// ErrSkipFingerprint is returned by ResolveCommandBinary when the
// command's binary should be skipped for cascade-kill — typically
// because command[0] is an interpreter (go, sh, etc.) whose binary
// the build step does not replace.
var ErrSkipFingerprint = errors.New("procscan: skip fingerprint (interpreter command)")

// isInterpreter returns true for commands that wrap rather than
// directly execute the service binary. Cascade-kill is meaningless
// for these because the build step does not replace the interpreter
// itself.
func isInterpreter(bin string) bool {
	base := filepath.Base(bin)
	switch base {
	case "go", "sh", "bash", "zsh", "python", "python3", "node", "deno", "bun":
		return true
	}
	return false
}
