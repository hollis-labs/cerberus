package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/hollis-labs/cerberus/internal/secretref"
	"github.com/hollis-labs/cerberus/internal/secrets"
)

// runSecretsTimeout bounds the whole resolution pass. Keychain reads can block
// on an ACL prompt; a managed service should fail loudly rather than hang in
// launchd's spawn state forever.
const runSecretsTimeout = 30 * time.Second

// runSecretsCmd is the exec-time half of Cerberus's secret handling.
//
// Cerberus renders a managed service's environment into its launchd plist, so a
// literal credential in a project file ends up plaintext in ~/Library/LaunchAgents
// as well. Writing a secretref reference instead keeps both files clean, and this
// shim is what turns the reference back into a credential — in the service's own
// process, at the moment it starts, never on disk.
//
// The plist for a service whose environment carries references invokes:
//
//	cerberus run-secrets -- <program> [args...]
//
// The shim resolves every reference in its own environment and then replaces
// itself with the target via execve, so launchd supervises the real process:
// PID, exit status, and KeepAlive all behave exactly as they do without it.
var runSecretsCmd = &cobra.Command{
	Use:   "run-secrets -- <program> [args...]",
	Short: "Resolve secret references in the environment, then exec a program",
	Long: "Resolve secretref references (keychain://…, helper://…) found in this\n" +
		"process's environment and exec the given program with the resolved values.\n\n" +
		"This is how Cerberus keeps credentials out of generated launchd plists:\n" +
		"the plist carries references, and resolution happens in the service's own\n" +
		"process at start time. Values that are not references are passed through\n" +
		"untouched.\n\n" +
		"Resolved secrets are never printed. A reference that cannot be resolved is\n" +
		"a fatal error — the program is not started with a blank credential.",
	Args:         cobra.MinimumNArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSecrets(cmd.Context(), args)
	},
}

func runSecrets(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("run-secrets requires a program to exec")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, runSecretsTimeout)
	defer cancel()

	resolver := secretref.NewResolver(secrets.NewKeychainProvider())

	env := environMap()
	resolved, err := resolver.ResolveEnv(ctx, env)
	if err != nil {
		return fmt.Errorf("resolve service environment: %w", err)
	}

	program, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("locate %s: %w", args[0], err)
	}

	// execve replaces this process so launchd supervises the target directly
	// rather than this shim. On success it does not return.
	if err := syscall.Exec(program, args, environSlice(resolved)); err != nil {
		return fmt.Errorf("exec %s: %w", program, err)
	}
	return nil
}

// environMap reads the current environment into a map. A variable whose value
// contains "=" keeps it; only the first separator splits name from value.
func environMap() map[string]string {
	entries := os.Environ()
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		out[name] = value
	}
	return out
}

// environSlice renders an environment map back into execve form, sorted so the
// child's environment is deterministic.
func environSlice(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+env[name])
	}
	return out
}
