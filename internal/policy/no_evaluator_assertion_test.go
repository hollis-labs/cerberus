package policy

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The daemon's decision point is a Reloading. A type assertion to
// *Evaluator outside tests silently misses it: egress policy was never
// applied because of one (H1). Read a PDP's file through FileOf.
func TestNoConcreteEvaluatorAssertions(t *testing.T) {
	root := filepath.Join("..", "..")
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "web") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path) //nolint:gosec // the repo's own sources
		if rerr == nil && (strings.Contains(string(data), ".(*policy.Evaluator)") || strings.Contains(string(data), ".(*Evaluator)")) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Fatalf("type assertions to the concrete *Evaluator miss the daemon's Reloading; use policy.FileOf: %v", hits)
	}
}
