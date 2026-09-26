// Package gitenv runs git the one way Cerberus runs it: directed only by
// the directory it is given.
//
// Git exports GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE and more into a hook's
// environment, and cmd.Dir does not win against them. A git subprocess that
// inherits them operates on the repository running the hook instead of the
// one it was pointed at. That has already rewritten this repository's
// identity and committed "Test User" fixtures onto a branch being pushed,
// from a test run by the pre-push hook. So every GIT_* variable is removed,
// not a named list: the set grows between git versions, and missing one is
// silent corruption of someone else's repository.
//
// TestNoBareGitCommands holds the rule: nothing in the module outside this
// package builds a git exec.Cmd itself.
package gitenv

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// Env is the process environment with every GIT_* variable removed.
func Env() []string {
	parent := os.Environ()
	out := make([]string, 0, len(parent))
	for _, entry := range parent {
		if strings.HasPrefix(entry, "GIT_") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// Binary resolves git per call. The daemon's PATH is launchd's minimal one,
// so a PATH lookup alone is not enough; with no git found it is "git", and
// running it fails the ordinary way.
func Binary() string {
	if path, err := exec.LookPath("git"); err == nil {
		return path
	}
	for _, candidate := range []string{"/usr/bin/git", "/opt/homebrew/bin/git", "/usr/local/bin/git"} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return "git"
}

// Command is git with args, run in dir, with the scrubbed environment.
func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, Binary(), args...) //nolint:gosec // git with the caller's fixed arguments, in the caller's directory
	cmd.Dir = dir
	cmd.Env = Env()
	return cmd
}
