package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cerberus/internal/buildlock"
)

func TestBuildLockCoversStrategyAndNestedDeployment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("build:\n\t@echo built > result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	spec := ProcessSpec{Dir: dir, BuildStrategy: &BuildStrategyConfig{Kind: "make_standard"}}
	ctx, release, err := WithBuildLock(context.Background(), spec, "app")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = BuildProcessResultContext(context.Background(), spec)
	var busy *buildlock.ErrBuildInProgress
	if !errors.As(err, &busy) {
		t.Fatalf("build ignored source lock: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "result")); !os.IsNotExist(statErr) {
		t.Fatal("blocked build executed")
	}
	if _, err = BuildProcessResultContext(ctx, spec); err != nil {
		t.Fatalf("nested deploy build: %v", err)
	}
	_, err = buildlock.Acquire(dir, buildlock.Holder{})
	if !errors.As(err, &busy) {
		t.Fatal("build released deployment's outer lock")
	}
	release()
	if _, err = BuildProcessResultContext(context.Background(), spec); err != nil {
		t.Fatalf("lock not released: %v", err)
	}
}
