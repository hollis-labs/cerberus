package userpresence

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// helper writes a stand-in for cerberus-presence: a script, so no test
// ever raises a real Touch ID prompt.
func helper(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cerberus-presence")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil { //nolint:gosec // the test's own helper
		t.Fatal(err)
	}
	return path
}

func TestTheHelperAnswers(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		body string
		want error
	}{
		"verified":      {"exit 0", nil},
		"refused":       {"echo 'Canceled by user.' >&2; exit 1", ErrRefused},
		"not available": {"echo 'No user interaction allowed.' >&2; exit 2", ErrUnavailable},
		"asked why":     {`[ "$1" = "--reason" ] && [ -n "$2" ] || exit 1; exit 0`, nil},
	} {
		t.Run(name, func(t *testing.T) {
			err := (&Helper{Path: helper(t, c.body)}).Verify(ctx, "allow a new passkey")
			if (c.want == nil) != (err == nil) || (c.want != nil && !errors.Is(err, c.want)) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	err := (&Helper{Path: helper(t, "echo 'No user interaction allowed.' >&2; exit 2")}).Verify(ctx, "x")
	if !strings.Contains(err.Error(), "No user interaction allowed") || !strings.Contains(err.Error(), Recovery) {
		t.Fatalf("an unavailable check does not name why and the recovery: %v", err)
	}
}

// A missing helper, a helper that never answers, and one that changed since
// the daemon first used it all refuse.
func TestTheHelperFailsClosed(t *testing.T) {
	ctx := context.Background()
	if err := (&Helper{Path: filepath.Join(t.TempDir(), "absent")}).Verify(ctx, "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a missing helper: %v", err)
	}
	if err := (&Helper{Path: helper(t, "sleep 5"), Timeout: 200 * time.Millisecond}).Verify(ctx, "x"); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "nobody answered") {
		t.Fatalf("a helper that never answers: %v", err)
	}
	path := helper(t, "exit 0")
	var pinned []string
	h := &Helper{Path: path, OnPin: func(d string) { pinned = append(pinned, d) }}
	if err := h.Verify(ctx, "x"); err != nil || len(pinned) != 1 || !strings.HasPrefix(pinned[0], "sha256:") {
		t.Fatalf("first use: %v %v", err, pinned)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0 # replaced\n"), 0o700); err != nil { //nolint:gosec // as above
		t.Fatal(err)
	}
	if err := h.Verify(ctx, "x"); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "changed since this daemon first used it") {
		t.Fatalf("a replaced helper: %v", err)
	}
	if err := (Refuse{Why: "not macOS"}).Verify(ctx, "x"); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "not macOS") {
		t.Fatalf("refuse: %v", err)
	}
}
