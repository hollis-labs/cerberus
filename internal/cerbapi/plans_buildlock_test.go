package cerbapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/buildlock"
	"github.com/hollis-labs/cerberus/internal/gitenv"
)

func TestStatusDirtyIgnoresOnlyTheBuildLock(t *testing.T) {
	cases := []struct {
		out  string
		want bool
	}{
		{"", false},
		{"?? .cerberus-build.lock\x00", false},
		{"?? sub/.cerberus-build.lock\x00", false},
		{"?? .cerberus-build.lock\x00?? new.go\x00", true},
		{" M main.go\x00", true},
		{"R  new.go\x00old.go\x00", true},
	}
	for _, c := range cases {
		if got := statusDirty([]byte(c.out)); got != c.want {
			t.Errorf("statusDirty(%q) = %v, want %v", c.out, got, c.want)
		}
	}
}

// A deploy reads the checkout, then takes the build lock, which creates the
// lock file; reading the checkout again must not report a change.
func TestBuildLockFileDoesNotChangeCheckedSource(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := gitenv.Command(ctx, dir, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")

	ctx = withCheckedSource(ctx, dir)
	if err := os.WriteFile(filepath.Join(dir, buildlock.Filename), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := sourceChanged(ctx, dir); msg != "" {
		t.Fatalf("lock file reported as a change: %s", msg)
	}
	if err := os.WriteFile(filepath.Join(dir, "extra.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := sourceChanged(ctx, dir); msg == "" {
		t.Fatal("a real untracked file was not reported")
	}
}
