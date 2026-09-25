package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cerberus/internal/redact"
)

// The lifecycle log is the operator's own: a log an earlier version created
// world-readable is narrowed to 0600, and records go through the request's
// redaction scope.
func TestLifecycleLogIsPrivateAndRedacted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".cerberus", "cerberus.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil { //nolint:gosec // the pre-fix mode, on purpose
		t.Fatal(err)
	}
	InitLifecycleLog()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("cerberus.log mode = %o, want 600", mode)
	}

	const sentinel = "q7Zr2mXv9pLw" //nolint:gosec // a test sentinel, not a credential
	ctx, scope := redact.EnsureScope(context.Background())
	scope.Add("github/token", sentinel)
	GetLogger().ErrorContext(ctx, "resource.apply.failed", "error", "vendor said "+sentinel)
	data, err := os.ReadFile(path) //nolint:gosec // the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), sentinel) || !strings.Contains(string(data), "resource.apply.failed") {
		t.Fatalf("cerberus.log = %s", data)
	}
}
