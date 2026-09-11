package local

import (
	"context"
	"path/filepath"

	"github.com/hollis-labs/cerberus/internal/buildlock"
)

type buildLockContextKey struct{}

// WithBuildLock holds the source-tree lock through build, install and activation.
// BuildProcessResultContext also takes it for standalone builds; nested calls
// using the returned context reuse this operation's lock.
func WithBuildLock(ctx context.Context, spec ProcessSpec, resourceID string) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	if !HasBuildStrategy(spec) {
		return ctx, func() {}, nil
	}
	root := resolveBuildDir(spec.Dir, stringRule(spec.BuildStrategy.Source, "root", "."))
	root, err := filepath.Abs(root)
	if err != nil {
		return ctx, nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return ctx, nil, err
	}
	// Different build subdirectories in a monorepo still share one lock.
	if repoRoot, gitErr := gitOutput(root, "rev-parse", "--show-toplevel"); gitErr == nil {
		root = repoRoot
	}
	if held, _ := ctx.Value(buildLockContextKey{}).(string); held == root {
		return ctx, func() {}, nil
	}
	release, err := buildlock.Acquire(root, buildlock.Holder{ResourceID: resourceID})
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, buildLockContextKey{}, root), release, nil
}
